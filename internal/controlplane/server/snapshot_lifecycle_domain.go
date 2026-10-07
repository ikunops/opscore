package server

// Phase 51 — Lifecycle Subject Domain: "whose window is it?" (ADR-077 scope,
// ADR-078 architecture).
//
// Division of labour, stated once:
//
//	P37 who · P40 where · P41 when · P42 whether · P43 why-absent
//	P45 what-accepted · P46 whether-witnessed · P47 whether-delivered
//	P48 whether-recorded · P49 whether-realized · P50 whether-in-authority
//	P51 whose window it is
//
// P41 answers "when was this key authorized" and P50 answers "was the observer
// in authority when it signed" — and BOTH rest on one authorization interval
// that never said WHICH DOMAIN it was issued to. The write face had always
// decided that domain (it must, to admit or refuse a key at all) and then threw
// the decision away, so a window opened for a manifest signing key satisfied the
// VERIFIER authority face the moment the key changed anchors, and a retired
// verifier leaked back into the signing roll-up. This file is that missing
// dimension: the domain is read out of the evidence (the row's signed `role`)
// instead of guessed from the deployment's live trust file.
//
// What this file is NOT:
//   - it writes nothing, repairs nothing, compacts nothing, dispatches nothing
//     and opens no connection. The ONLY I/O is the existing
//     `loadKeyLifecycleState` the caller already performed (ADR-078 §8: ONE read
//     per status document), plus the P41 interval primitives, which are reused
//     with NOT ONE comparison re-derived (I2);
//   - it does not touch P41's `authorizationFor` / `authorizeByLifecycle` /
//     `validityOf`. The domain split happens HERE, by selecting WHICH rows are
//     folded (ADR-077 §4 A5), never by re-implementing the folding;
//   - it does not replace A6. A6 covers a key that sits in BOTH anchors AT ONCE
//     (a construction-time guard re-checked on read); this face covers SEQUENTIAL
//     migration and retirement — non-overlapping, non-substituting (§3.2).
//
// Two disciplines are load-bearing and easy to get wrong, so they are stated
// where the code is:
//
//	I4  — a row that CARRIES `role` takes its domain from THAT ROW and from
//	      nothing else. Resolving such a row's domain by looking it up in the
//	      current trust file is exactly the defect this Phase exists to remove,
//	      and it is what the pre-Phase-51 code did. (Scope: this invariant does
//	      NOT cover rows without `role` — see I13/A4-2 below. A4-2 is the ONLY
//	      place where reading the live trust file is still correct.)
//
//	I12 — the face-level scalar is a TOTAL ORDER, first hit
//	      (conflict > migrated > declared > undeclared). It is NOT a
//	      conjunction and it is NOT produced by sequential assignment: a later
//	      assignment would overwrite an earlier fail-closed verdict, and the
//	      direction of that overwrite is fail-OPEN.
//
//	I13 — A4-2: a row written BEFORE this Phase has no domain and CANNOT be
//	      given one (that would be rewriting history out of a live config). It
//	      is resolved by the consumer's OWN pre-Phase-51 rule, per consumer:
//	      the P41 roll-up keeps `k ∈ 当前 verifierTrust ⇒ 不列`, the P50 authority
//	      face keeps "a row in the ledger participates" (its whole observation set
//	      was "in the verifier anchor"). Unifying the two into one rule is
//	      provably impossible: on the P50-era rows T351 and T362 pull in opposite
//	      directions (ADR-077 §10 Q2/Q4, probe 5).

import (
	"fmt"
	"strings"
)

// The four DOMAIN values — exactly four, never five (ADR-077 §3, review M2:
// `migrated` is an independent subject-level BOOL, not a fifth value).
const (
	lifecycleDomainSigning    = "signing"
	lifecycleDomainVerifier   = "verifier"
	lifecycleDomainUndeclared = "undeclared"
	lifecycleDomainConflict   = "conflict"
)

// The face-level scalar's vocabulary (ADR-078 §4). `declared` is derived from a
// POSITIVE fact (a row that declares a domain), never from "no counter-example
// was found" (I6) — an empty ledger reads `undeclared`, never `declared`.
const (
	lifecycleDomainStateMigrated = "migrated"
	lifecycleDomainStateDeclared = "declared"
)

// lifecycleRoleOf projects the write face's already-decided anchor membership
// into the row's domain carrier (A1). It is a pure function of the two booleans
// `appendKeyLifecycleEvent` computes for admission, so it can never disagree with
// the admission decision, and it can never be supplied by a caller.
func lifecycleRoleOf(inSigning, inVerifier bool) string {
	switch {
	case inSigning:
		return lifecycleDomainSigning
	case inVerifier:
		return lifecycleDomainVerifier
	}
	return ""
}

// domainStatus is one subject's domain verdict. `Migrated` is deliberately a
// SEPARATE boolean: a subject that was first a verifier and later a signer has a
// perfectly decidable current domain (the last `role` row's value) AND is
// simultaneously "migrated" — collapsing the two into one enum would either lose
// the domain or invent a fifth value that contradicts §4's schema.
type domainStatus struct {
	Domain   string
	Migrated bool
}

// domainOf resolves ONE subject's domain from the LEDGER alone (I4).
//
// Rules, first hit:
//  1. a `role` outside {signing, verifier} (the empty string is the legal
//     "written before Phase 51" marker) ⇒ CONFLICT (fail-closed);
//  2. two rows of the SAME event_seq group disagreeing on `role` ⇒ CONFLICT
//     (one event group is one fact; two domains inside it means the row set was
//     assembled, not appended);
//  3. no non-empty `role` at all ⇒ UNDECLARED (possible only for pre-Phase-51
//     rows — the write face cannot produce such a row after this Phase);
//  4. otherwise the LAST `role` row's value, with `Migrated` set when BOTH
//     domains are present on DIFFERENT event_seqs (a legal operator migration,
//     reported loudly, never fail-closed: both facts are assertable).
func domainOf(st *keyLifecycleState, keyID string) domainStatus {
	if st == nil {
		return domainStatus{Domain: lifecycleDomainUndeclared}
	}
	rows := st.byKey[keyID]
	for i := range rows {
		switch rows[i].Role {
		case "", lifecycleDomainSigning, lifecycleDomainVerifier:
		default:
			return domainStatus{Domain: lifecycleDomainConflict}
		}
	}
	seen := map[int64]string{}
	for i := range rows {
		if prev, ok := seen[rows[i].EventSeq]; ok {
			if prev != rows[i].Role {
				return domainStatus{Domain: lifecycleDomainConflict}
			}
			continue
		}
		seen[rows[i].EventSeq] = rows[i].Role
	}
	last := ""
	sawSigning, sawVerifier := false, false
	for i := range rows {
		switch rows[i].Role {
		case lifecycleDomainSigning:
			sawSigning = true
			last = rows[i].Role
		case lifecycleDomainVerifier:
			sawVerifier = true
			last = rows[i].Role
		}
	}
	if last == "" {
		return domainStatus{Domain: lifecycleDomainUndeclared}
	}
	return domainStatus{Domain: last, Migrated: sawSigning && sawVerifier}
}

// domainRows selects the rows of `keyID` that belong to the domain `want`. It is
// the A4 split, and the A4-2 half is the ONLY place in this Phase that reads a
// live trust file:
//
//	A4-1 — a row carrying `role` participates iff `role == want`;
//	A4-2 — a row with NO `role` follows the CONSUMER's pre-Phase-51 rule:
//	         want == signing  ⇒ participates iff keyID ∉ 当前 verifierTrust
//	                            (the `snapshot_key_lifecycle.go` roll-up filter)
//	         want == verifier ⇒ always participates (the P50 authority face's
//	                            observation set WAS "in the verifier anchor")
//	       Any other `want` (the undeclared / conflict probes) selects no
//	       undeclared row at all: those are not domains a window can be issued to.
func domainRows(c keyLifecycleConfig, st *keyLifecycleState, keyID, want string) []keyLifecycleEntry {
	if st == nil || keyID == "" {
		return nil
	}
	rows := st.byKey[keyID]
	if len(rows) == 0 {
		return nil
	}
	var out, undeclared []keyLifecycleEntry
	for i := range rows {
		switch rows[i].Role {
		case want:
			out = append(out, rows[i])
		case "":
			undeclared = append(undeclared, rows[i])
		}
	}
	if len(undeclared) == 0 {
		return out
	}
	switch want {
	case lifecycleDomainSigning:
		if c.verifierTrust == nil {
			out = append(out, undeclared...)
		} else if _, inVerifier := c.verifierTrust.keys[keyID]; !inVerifier {
			out = append(out, undeclared...)
		}
	case lifecycleDomainVerifier:
		out = append(out, undeclared...)
	}
	return out
}

// rowsForDomain is the CALLER-FACING count: "does this subject have any row IN
// THIS DOMAIN's records?" Every call site decides inclusion with THIS number and
// never with an undefined field (review blocker B1: the earlier draft read
// `.rows` off `keyAuthorization`, which has no such field).
func rowsForDomain(c keyLifecycleConfig, st *keyLifecycleState, keyID, want string) int {
	return len(domainRows(c, st, keyID, want))
}

// authorizationForDomain resolves the interval over ONE domain's rows only, and
// returns how many rows were actually folded. The folding itself is P41's
// `authorizationFor` — VERBATIM (I2): the shallow copy replaces only the row set,
// so the window, the completeness test and every comparison stay P41's.
//
// n == 0 means "this subject has no row in this domain". The returned interval is
// then the P41 UNBOUNDED one (never evaluable), which is exactly the statement
// the evidence supports — and NOT the subject's interval in some other domain.
func authorizationForDomain(c keyLifecycleConfig, st *keyLifecycleState, keyID, want string) (keyAuthorization, int) {
	n := rowsForDomain(c, st, keyID, want)
	if n == 0 || st == nil {
		return unboundedKeyAuthorization(keyID), 0
	}
	cp := *st
	cp.byKey = map[string][]keyLifecycleEntry{keyID: domainRows(c, st, keyID, want)}
	return cp.authorizationFor(keyID), n
}

// ---------------------------------------------------------------------------
// Read surface — the `key_lifecycle_domains` group (ADR-078 §4)
// ---------------------------------------------------------------------------

// lifecycleDomainSubjectStatus is ONE subject's row. `domain` is never omitted
// (a subject with no assertable domain must say so); the witness counts are
// omitempty so a clean subject stays compact.
type lifecycleDomainSubjectStatus struct {
	Domain string `json:"domain"`
	// Migrated is an independent boolean, NOT a domain value (review M2).
	Migrated bool `json:"migrated,omitempty"`
	// Events / DeclaredEvents / UndeclaredEvents are the witness counts behind
	// the verdict: `DeclaredEvents` is where the assertion comes from, and
	// `UndeclaredEvents` is the machine-readable form of "how many rows this
	// face's domain claim does NOT cover" (ADR-078 §4).
	Events           int                `json:"events,omitempty"`
	DeclaredEvents   int                `json:"declared_events,omitempty"`
	UndeclaredEvents int                `json:"undeclared_events,omitempty"`
	Validity         *signatureValidity `json:"validity,omitempty"`
	// Reason is loud for the three undecidable/exceptional verdicts
	// (undeclared / migrated / conflict) and empty for a clean declaration.
	Reason string `json:"reason,omitempty"`
}

// lifecycleDomainStatusSummary is the `key_lifecycle_domains` group. The whole
// group is omitempty at the parent and the pointer is nil unless the lifecycle
// ledger is enabled — the SAME gate as `key_lifecycle`, so the two groups appear
// and disappear together (I3/T364).
type lifecycleDomainStatusSummary struct {
	Enabled bool `json:"enabled"`
	// Subjects holds every subject the ledger mentions, ascending.
	Subjects map[string]lifecycleDomainSubjectStatus `json:"subjects,omitempty"`

	// State is the GLOBAL scalar (ADR-077 §3 — the only authoritative
	// definition): conflict > migrated > declared > undeclared, first hit.
	State string `json:"lifecycle_domain_state"`
	// Declared is a DERIVED convenience value — `State == "declared"` and
	// nothing else, so "an empty set is not true" holds by construction.
	Declared bool `json:"lifecycle_domain_declared"`

	SubjectCount     int `json:"lifecycle_domain_subjects"`
	Events           int `json:"lifecycle_domain_events"`
	DeclaredEvents   int `json:"lifecycle_domain_declared_events"`
	UndeclaredEvents int `json:"lifecycle_domain_undeclared_events"`

	MigratedKeys []string `json:"lifecycle_domain_migrated_keys,omitempty"`
	ConflictKeys []string `json:"lifecycle_domain_conflict_keys,omitempty"`
	// Reason carries the FACE-level fail-closed cause (a ledger that cannot be
	// evaluated). It never silences a subject's own reason.
	Reason string `json:"reason,omitempty"`
}

// keyLifecycleDomainSummary is the standalone entry point (one load).
func keyLifecycleDomainSummary(c keyLifecycleConfig) lifecycleDomainStatusSummary {
	out := lifecycleDomainStatusSummary{}
	if !c.enabled() {
		return out
	}
	st, err := loadKeyLifecycleState(c)
	return keyLifecycleDomainSummaryFromState(c, st, err)
}

// keyLifecycleDomainSummaryFromState derives the group from the SAME loaded
// ledger the P41 roll-up used, so a status document reads the ledger exactly
// once (ADR-078 §8). Strictly read-only: no write, no audit, no network, no
// dispatch, no compaction, no cache (A6/T375).
func keyLifecycleDomainSummaryFromState(c keyLifecycleConfig, st *keyLifecycleState, err error) lifecycleDomainStatusSummary {
	out := lifecycleDomainStatusSummary{}
	if !c.enabled() {
		return out
	}
	out.Enabled = true
	// ⓪ A ledger that cannot be read at all: the group is present (the gate is
	// the lifecycle gate) and it says so loudly. `conflict` is the only
	// fail-closed value in the state vocabulary and it is deliberately the first
	// hit of the total order, so nothing below can wash it out (I12).
	if err != nil {
		out.State = lifecycleDomainConflict
		out.Reason = "the key lifecycle ledger cannot be evaluated: " + err.Error()
		return out
	}
	if st == nil {
		out.State = lifecycleDomainConflict
		out.Reason = "the key lifecycle ledger cannot be evaluated"
		return out
	}

	// A ledger that loaded but is NOT verifiable (a broken chain, a digest
	// mismatch, a foreign stream, a duplicate event group) is untrusted evidence:
	// no domain may be asserted from it. The subjects are still enumerated, so the
	// failure is visible per subject, but EVERY one of them is `conflict` — never
	// `declared` (I8: "账本不可验 … ⇒ 相应主体 conflict、面级 conflict").
	trusted := st.verifiable

	keyIDs := make([]string, 0, len(st.byKey))
	for k := range st.byKey {
		keyIDs = append(keyIDs, k)
	}
	sortStrings(keyIDs)

	out.Subjects = make(map[string]lifecycleDomainSubjectStatus, len(keyIDs))
	for _, k := range keyIDs {
		rows := st.byKey[k]
		row := lifecycleDomainSubjectStatus{Events: len(rows)}
		for i := range rows {
			switch rows[i].Role {
			case lifecycleDomainSigning, lifecycleDomainVerifier:
				row.DeclaredEvents++
			case "":
				row.UndeclaredEvents++
			}
		}
		d := domainOf(st, k)
		row.Domain = d.Domain
		row.Migrated = d.Migrated
		if !trusted {
			row.Domain = lifecycleDomainConflict
			row.Migrated = false
			row.Reason = "the key lifecycle ledger is not verifiable — no domain is asserted for this subject"
		} else {
			a, _ := authorizationForDomain(c, st, k, d.Domain)
			row.Validity = validityOf(a)
			switch d.Domain {
			case lifecycleDomainConflict:
				row.Reason = "the subject's rows declare two domains inside one event group, or carry a role outside {signing, verifier} — the domain is undecidable, so neither is asserted"
			case lifecycleDomainUndeclared:
				row.Reason = fmt.Sprintf("%d row(s) of this subject declare no domain (written before the domain carrier existed) — the domain is permanently unassertable from this evidence", row.Events)
			default:
				if d.Migrated {
					row.Reason = "the subject holds rows in BOTH domains (a sequential migration) — the current domain is the last declared one, and both intervals remain assertable"
				}
			}
		}

		out.Events += row.Events
		out.DeclaredEvents += row.DeclaredEvents
		out.UndeclaredEvents += row.UndeclaredEvents
		out.Subjects[k] = row
		if row.Domain == lifecycleDomainConflict {
			out.ConflictKeys = append(out.ConflictKeys, k)
		}
		if row.Migrated {
			out.MigratedKeys = append(out.MigratedKeys, k)
		}
	}
	out.SubjectCount = len(out.Subjects)

	// The face scalar: first hit of the total order (I12), computed AFTER the
	// loop so no later assignment can overwrite an earlier fail-closed verdict.
	declaredSubjects := 0
	for _, row := range out.Subjects {
		if row.Domain == lifecycleDomainSigning || row.Domain == lifecycleDomainVerifier {
			declaredSubjects++
		}
	}
	switch {
	case !trusted:
		out.State = lifecycleDomainConflict
		out.Reason = "the key lifecycle ledger is not verifiable — no domain assertion is made (" + strings.Join(st.errs, "; ") + ")"
	case len(out.ConflictKeys) > 0:
		out.State = lifecycleDomainConflict
		out.Reason = fmt.Sprintf("%d subject(s) carry a conflicting or invalid domain declaration: %s", len(out.ConflictKeys), strings.Join(out.ConflictKeys, ", "))
	case len(out.MigratedKeys) > 0:
		out.State = lifecycleDomainStateMigrated
		out.Reason = fmt.Sprintf("%d subject(s) migrated between the two domains: %s", len(out.MigratedKeys), strings.Join(out.MigratedKeys, ", "))
	case declaredSubjects > 0:
		// NOTE (ADR-077 §11 M3): `declared` does NOT require an empty
		// `undeclared_events`. Pre-Phase-51 rows stay countable and loud
		// (`UndeclaredEvents`), and demanding zero of them would make this face
		// permanently `undeclared` on every deployment that has history. The
		// non-vacuity anchor is `DeclaredEvents > 0`.
		out.State = lifecycleDomainStateDeclared
	default:
		out.State = lifecycleDomainUndeclared
		out.Reason = fmt.Sprintf("no subject in the ledger declares a domain (%d row(s) carry none) — the empty set is not a declaration", out.UndeclaredEvents)
	}
	out.Declared = out.State == lifecycleDomainStateDeclared
	return out
}
