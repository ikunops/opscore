package server

// Phase 50 — Verifier Authority Lifecycle: the "when" face for the SECOND
// signing identity (ADR-075 scope, ADR-076 architecture).
//
// Division of labour, stated once:
//
//	P37 who · P40 where · P41 when · P42 whether · P43 why-absent
//	P45 what-accepted · P46 whether-witnessed · P47 whether-delivered
//	P48 whether-recorded · P49 whether-realized
//	P50 whether the observer was in authority
//
// Phase 44 introduced a second signing identity — the VAK, the identity that
// signs the verification reports, deliberately a DIFFERENT key from the
// manifest signer and from the KAK — and left its time dimension empty. The
// "when" face (P41) covers ONE key: the manifest signing key. So the system can
// assert who signed a verification report (P44), what it verified (P42), that
// it was delivered and anchored and realized (P47/P49) — and nothing at all
// about whether the identity that signed it was allowed to sign it AT THAT
// MOMENT. That is the gap this file closes, and it is a gap rather than a
// "second instance of P41" because the face is not merely un-wired: the write
// face structurally refused to record a verifier window at all
// (`appendKeyLifecycleEvent` accepted only the P37 signing anchor), so the
// judgement was UNREPRESENTABLE, not unconnected.
//
// What this file is NOT:
//   - it writes nothing, reviews nothing, dispatches nothing, compacts nothing
//     and opens no connection: the ONLY I/O is the two EXISTING loads
//     (`loadVerificationState`, `loadKeyLifecycleState`) plus the two interval
//     primitives P41 already owns (`authorizationFor`, `authorizeByLifecycle`),
//     both of which are reused VERBATIM (I2 — not one comparison is re-derived
//     here);
//   - it does not touch P42's report (its `lifecycle` dimension still names the
//     MANIFEST signer — A7-③), nor P44's verdicts, nor P49's realization face.
//     P50 answers a question none of them answer, and all of them stay readable
//     on the same input (I5).
//
// Two disciplines are load-bearing and easy to get wrong, so they are stated
// where the code is:
//
//	I11 — the signer is `Signature.KeyID`, NEVER `e.KeyID`. The entry's own
//	      `key_id` is covered by the signature but NOT bound to the signer
//	      (P44 verifies against `sb.KeyID`; only the other three families carry
//	      a "disagrees with the signing key id" check). Without this rule the
//	      adversary this Phase exists for — the holder of a REVOKED VAK private
//	      key — fills `key_id` with some other key that IS inside its window and
//	      the face would check THAT key's interval. A disagreement is therefore
//	      fail-closed, and the mismatching key's interval is never consulted.
//
//	I12 — the row verdict is a TOTAL ORDER, first hit, computed AFTER the
//	      loop from counters. Writing `row = ...` inside the loop is sequential
//	      assignment: a later assignment overwrites an earlier `indeterminate`,
//	      and the direction of that overwrite is fail-open (one legal record
//	      would wash a defective one out of the row).

import (
	"fmt"
	"strings"
)

// The row-level verdicts (ADR-075 §3: no `verifier_authority_` prefix — the
// prefixed names are the GLOBAL fields). None of them is a renamed form of
// another: `violated` (this identity was out of its window) must never fold
// into P44's `verification_unauthorized` (this identity is not of this family),
// and `unbounded` (no interval can be asserted) must never fold into either
// `authorized` or `violated` (I7/A5).
const (
	verifierAuthorityAuthorized      = "authorized"
	verifierAuthorityViolated        = "violated"
	verifierAuthorityUnbounded       = "unbounded"
	verifierAuthorityIndeterminate   = "indeterminate"
	verifierAuthorityNothingAssessed = "nothing_assessed"
)

// verifierAuthorityKeyStatus is ONE signer's row of the `verifier_authority`
// group (ADR-076 §4). Every counter is omitempty so a row that counted nothing
// stays compact; `verdict` is never omitted, because "we cannot say" must always
// be visible.
type verifierAuthorityKeyStatus struct {
	Verdict string `json:"verdict"`
	// Entries is the witness count (records observed for this signer); Checked is
	// the assertion count (records whose interval was ACTUALLY evaluated). They
	// are deliberately two fields: I6's non-vacuity is `checked`, and a row that
	// is `unbounded` must show `checked == 0` rather than an empty claim of
	// authority.
	Entries          int                `json:"entries,omitempty"`
	Checked          int                `json:"checked,omitempty"`
	Authorized       int                `json:"authorized,omitempty"`
	BeforeActivation int                `json:"before_activation,omitempty"`
	AfterRotation    int                `json:"after_rotation,omitempty"`
	AfterRevocation  int                `json:"after_revocation,omitempty"`
	Indeterminate    int                `json:"indeterminate,omitempty"`
	Validity         *signatureValidity `json:"validity,omitempty"`
	Reason           string             `json:"reason,omitempty"`
}

// verifierAuthorityStatusSummary is the `verifier_authority` group. The whole
// group is omitempty at the parent, and the parent pointer is nil unless a VAK
// is configured, so a default deployment's status document is byte-identical
// to Phase 49 (I3/T341).
type verifierAuthorityStatusSummary struct {
	// Keys is the observation set: every SIGNER (Signature.KeyID) that appears
	// in the usable verification records, ascending.
	Keys map[string]verifierAuthorityKeyStatus `json:"keys,omitempty"`

	// State is the GLOBAL scalar (ADR-075 §3 — the ONLY authoritative
	// definition): indeterminate > violated > authorized > nothing_assessed,
	// first hit. It is deliberately NOT a conjunction.
	State string `json:"verifier_authority_state"`
	// Authorized is a DERIVED convenience value — `State == "authorized"` and
	// nothing else (an empty set can never be "everything is in position").
	Authorized bool `json:"verifier_authorized"`
	// Claims is Σ checked: the number of records whose interval was actually
	// evaluated in this read. Authorized ⇒ Claims > 0.
	Claims int `json:"verifier_authority_claims"`
	// Entries is Σ entries: the observed usable records (a WITNESS count, not an
	// assertion).
	Entries int `json:"verifier_authority_entries"`
	// Violations is Σ (before_activation + after_rotation + after_revocation).
	Violations int `json:"verifier_authority_violations"`

	UnboundedKeys     []string `json:"verifier_authority_unbounded_keys,omitempty"`
	ViolatingKeys     []string `json:"verifier_authority_violating_keys,omitempty"`
	IndeterminateKeys []string `json:"verifier_authority_indeterminate_keys,omitempty"`
	// Reason carries the FACE-level fail-closed cause (domain ambiguity, a
	// ledger that cannot be evaluated). It never silences a row's own reason.
	Reason string `json:"reason,omitempty"`
}

// verifierConfigured reports whether there is a verifier authority to judge at
// all. Without `--export-verifier-key` / `--export-verifier-trust` there is no
// second signing identity, so the question is not "we cannot say" but "not
// applicable" — the group stays absent entirely (ADR-076 §3 ⓪ / I3).
func (s *HistoryExportScheduler) verifierConfigured() bool {
	return s != nil && s.verifierSigner != nil && s.verifierTrust != nil && len(s.verifierTrust.keys) > 0
}

// verifierTrustOverlap returns the key_ids present in BOTH anchors, ascending.
// An empty result means the domains are decidable.
func verifierTrustOverlap(a, b *exportTrustStore) []string {
	if a == nil || b == nil {
		return nil
	}
	var out []string
	for id := range a.keys {
		if _, ok := b.keys[id]; ok {
			out = append(out, id)
		}
	}
	sortStrings(out)
	return out
}

// VerifierAuthorityStatus derives the face. Strictly read-only (ADR-076 §4 /
// A4): two EXISTING loads and nothing else — no write, no audit record, no
// network, no dispatch, no compaction, no cache.
//
// Fail-closed discipline, in the order ADR-076 §3 fixes it: an unavailable
// verification ledger, a lifecycle ledger that cannot be evaluated, and a
// key_id sitting in two trust anchors each make the WHOLE face `indeterminate`
// with a loud reason — never `authorized`. "The evidence is broken" and "the
// evidence supports no assertion" are DIFFERENT verdicts and are never merged
// (I4): the first is `indeterminate`, the second is `unbounded`.
func (s *HistoryExportScheduler) VerifierAuthorityStatus() verifierAuthorityStatusSummary {
	out := verifierAuthorityStatusSummary{State: verifierAuthorityNothingAssessed}
	if !s.verifierConfigured() {
		// Unreachable through Status() (which omits the group); kept so a direct
		// call can never invent a verdict for a deployment with no verifier.
		out.Reason = "no verifier identity is configured — there is no verifier authority to judge"
		return out
	}

	// ① Domain ambiguity re-check (A6, load-bearing). The V2 construction guard
	// forbids the overlap, but a guard is a construction-time fact while the
	// ledger and the trust files come from disk: a directory whose configuration
	// was changed under it can legally carry a combination the guard refuses.
	// This row's domain — was it a window for a manifest key or for a verifier? —
	// is undecidable, so the whole face fails closed. Never guess which anchor
	// "it looks more like".
	if overlap := verifierTrustOverlap(s.trust, s.verifierTrust); len(overlap) > 0 {
		out.State = verifierAuthorityIndeterminate
		out.Reason = fmt.Sprintf("key(s) in two trust anchors: %s — a row's domain (manifest signing key or verification signer) is undecidable, so no authority claim is made", strings.Join(overlap, ", "))
		return out
	}

	// ② The verification ledger: the OBSERVATION set. Any unclassifiable line,
	// digest mismatch, broken chain, foreign stream or unverifiable signature
	// poisons it (the load is already fail-closed) ⇒ the face is indeterminate.
	vs, err := loadVerificationState(s.verificationConfig())
	if err != nil {
		out.State = verifierAuthorityIndeterminate
		out.Reason = "the verification log cannot be evaluated: " + err.Error()
		return out
	}
	if !vs.verifiable {
		out.State = verifierAuthorityIndeterminate
		out.Reason = "the verification log is not verifiable: " + strings.Join(vs.errs, "; ")
		return out
	}

	// ③ An EMPTY observation set needs no lifecycle ledger at all (I9, the same
	// discipline as P49's empty anchor window). Otherwise a deployment that never
	// signed a report would be dragged to `indeterminate` by a ledger it never
	// needed — "nothing was observed" is silence, not failure.
	if len(vs.entries) == 0 {
		out.State = verifierAuthorityNothingAssessed
		out.Reason = "verification log holds no usable entry"
		return out
	}

	// ④ The lifecycle ledger. "Cannot be read" is fail-closed (indeterminate);
	// "read fine but proves no window for this key" is `unbounded` (I4). These
	// are different statements and are never collapsed.
	ls, lerr := loadKeyLifecycleState(s.keyLifecycleConfig())
	if lerr != nil {
		out.State = verifierAuthorityIndeterminate
		out.Reason = "the key lifecycle ledger cannot be evaluated: " + lerr.Error()
		return out
	}
	if ls == nil || !ls.verifiable {
		out.State = verifierAuthorityIndeterminate
		reason := "the key lifecycle ledger is not verifiable"
		if ls != nil && len(ls.errs) > 0 {
			reason += ": " + strings.Join(ls.errs, "; ")
		}
		out.Reason = reason
		return out
	}

	// ⑤ Per signer, ASCENDING signer id (a total order, so the verdict never
	// depends on map iteration). The signer is `Signature.KeyID` (I11).
	bySigner := map[string][]verificationLogEntry{}
	for i := range vs.entries {
		kid := ""
		if vs.entries[i].Signature != nil {
			kid = vs.entries[i].Signature.KeyID
		}
		bySigner[kid] = append(bySigner[kid], vs.entries[i])
	}
	signers := make([]string, 0, len(bySigner))
	for k := range bySigner {
		signers = append(signers, k)
	}
	sortStrings(signers)

	out.Keys = make(map[string]verifierAuthorityKeyStatus, len(signers))
	for _, kid := range signers {
		row := judgeVerifierAuthoritySigner(kid, bySigner[kid], ls)
		out.Keys[kid] = row
		out.Entries += row.Entries
		out.Claims += row.Checked
		out.Violations += row.BeforeActivation + row.AfterRotation + row.AfterRevocation
		switch row.Verdict {
		case verifierAuthorityIndeterminate:
			out.IndeterminateKeys = append(out.IndeterminateKeys, kid)
		case verifierAuthorityViolated:
			out.ViolatingKeys = append(out.ViolatingKeys, kid)
		case verifierAuthorityUnbounded:
			out.UnboundedKeys = append(out.UnboundedKeys, kid)
		}
	}

	// ⑥ The global scalar: first hit of the total order (ADR-075 §3). Not a
	// conjunction — one indeterminate row makes the face indeterminate, and a
	// violated row outranks any number of authorized ones.
	switch {
	case len(out.IndeterminateKeys) > 0:
		out.State = verifierAuthorityIndeterminate
	case len(out.ViolatingKeys) > 0:
		out.State = verifierAuthorityViolated
	default:
		for _, row := range out.Keys {
			if row.Verdict == verifierAuthorityAuthorized {
				out.State = verifierAuthorityAuthorized
				break
			}
		}
	}
	out.Authorized = out.State == verifierAuthorityAuthorized
	// An unjudgeable face is LOUD at the face level too, not only in the row
	// (A5/I7): "nothing was checked" must never be presented as "no problem".
	if out.State == verifierAuthorityNothingAssessed && out.Reason == "" {
		out.Reason = fmt.Sprintf("no record was checked against an assertable authorization interval (%d observed, %d unjudgeable signer(s))", out.Entries, len(out.UnboundedKeys))
	}
	return out
}

// judgeVerifierAuthoritySigner judges ONE signer from counters (I12) and only
// then decides the row verdict by the total order
// indeterminate > violated > authorized > unbounded > nothing_assessed.
//
// `keyID` is ALWAYS `Signature.KeyID` (I11) — the caller grouped the entries by
// that field. The entries passed here are all usable records of that signer.
func judgeVerifierAuthoritySigner(keyID string, entries []verificationLogEntry, ls *keyLifecycleState) verifierAuthorityKeyStatus {
	row := verifierAuthorityKeyStatus{Entries: len(entries)}
	if keyID == "" {
		// A usable record must carry a signing key id (a malformed signature block
		// never reaches the usable set); if one somehow does, the signer's identity
		// is not established and nothing may be asserted about it.
		row.Indeterminate = len(entries)
		row.Verdict = verifierAuthorityIndeterminate
		row.Reason = "a usable verification entry carries no signing key id"
		return row
	}

	// ①.0 Identity binding (I11 / ADR-075 §4 A9). The entry's own `key_id` is
	// covered by the signature but NOT bound to the signer, so a row where the
	// two disagree is fail-closed — and, critically, we do NOT go and check the
	// interval of the key `e.KeyID` names. That detour is the bypass: the holder
	// of a revoked VAK's private key fills `key_id` with a key that IS inside its
	// window and the face would whitewash the real signer.
	for i := range entries {
		if entries[i].Signature != nil && entries[i].KeyID != entries[i].Signature.KeyID {
			row.Indeterminate++
		}
	}
	if row.Indeterminate > 0 {
		row.Verdict = verifierAuthorityIndeterminate
		row.Reason = fmt.Sprintf("%d record(s) carry a key_id that disagrees with the signing key id — the signer's identity is not bound, so no interval is checked (checking the named key's window would be the bypass)", row.Indeterminate)
		return row
	}

	// Reuse P41 VERBATIM (I2): resolution and comparison are not re-derived here.
	a := ls.authorizationFor(keyID)
	row.Validity = validityOf(a)
	intervalAssertable := a.Source == lifecycleSourceLedger && a.Complete

	if intervalAssertable {
		for i := range entries {
			signedAt := ""
			if entries[i].Signature != nil {
				signedAt = entries[i].Signature.SignedAt
			}
			// R41-7's fourth word, applied to the observation: a defective time
			// declaration is a structural fact, so it is assertable — and it must
			// never leave the row quiet (`authorized` would be a lie, and
			// `nothing_assessed` would hide a defect).
			if _, terr := parseRFC3339Nano(signedAt); terr != nil {
				row.Indeterminate++
				row.Reason = "signed_at is not a parseable RFC3339Nano timestamp: the time declaration itself is defective, so no interval can be evaluated"
				continue
			}
			v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: keyID}, signedAt, a)
			switch v.Verdict {
			case sigVerdictOK:
				row.Authorized++
			case sigVerdictBeforeActivation:
				row.BeforeActivation++
			case sigVerdictAfterRotation:
				row.AfterRotation++
			case sigVerdictAfterRevocation:
				row.AfterRevocation++
			case sigVerdictTimeUnparseable:
				row.Indeterminate++
			default:
				// An unrecognised verdict is fail-closed too: a comparison result we
				// cannot name is not evidence of authority.
				row.Indeterminate++
				if row.Reason == "" {
					row.Reason = fmt.Sprintf("the interval comparison produced an unrecognised verdict %q", v.Verdict)
				}
			}
			row.Checked++
		}
	}

	// The row verdict is produced HERE, from counters, by first hit of the total
	// order (I12). Nothing above writes `row.Verdict`, precisely so that no later
	// step can overwrite an earlier `indeterminate`.
	switch {
	case row.Indeterminate > 0:
		row.Verdict = verifierAuthorityIndeterminate
		if row.Reason == "" {
			row.Reason = "the records of this signer cannot be classified"
		}
	case row.BeforeActivation+row.AfterRotation+row.AfterRevocation > 0:
		row.Verdict = verifierAuthorityViolated
		row.Reason = fmt.Sprintf("the signer was outside its authorization window when it signed: %d before activation, %d after rotation, %d after revocation", row.BeforeActivation, row.AfterRotation, row.AfterRevocation)
	case row.Authorized > 0:
		row.Verdict = verifierAuthorityAuthorized
	case !intervalAssertable:
		row.Verdict = verifierAuthorityUnbounded
		row.Reason = "the signer has no assertable authorization interval in the key lifecycle ledger — the evidence does not support either assertion, so neither is made"
	default:
		// Defensive: reachable only if an assertable interval produced neither a
		// classified record nor a counter — there is no such branch above, so this
		// is a backstop, not a reachable verdict.
		row.Verdict = verifierAuthorityNothingAssessed
		row.Reason = "the interval is assertable but no record was actually checked against it"
	}
	return row
}
