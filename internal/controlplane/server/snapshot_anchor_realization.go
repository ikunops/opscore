package server

// Phase 49 — Anchor Realization (ADR-073 scope, ADR-074 architecture).
//
// What Phases 37~48 cannot say: an anchor entry is an ASSERTION, not a
// credential. It carries the identity + digest of the evidence it claims to
// anchor, and every earlier face reads that claim WITHOUT ever checking it
// against the evidence itself:
//
//	P37 verifies WHO signed the entry (never what it says);
//	P46 compares the entry against an OUT-OF-DOMAIN COPY (never the main ledger);
//	P47 asks whether the entry was DELIVERED (never whether its content is true);
//	P48 recomputes the decision chain locally (only for the sixth family);
//	P43 reads the publication family's claim and BELIEVES it.
//
// This Phase adds the missing comparison — the anchor entry's claim against the
// family's OWN MAIN LEDGER — and with it the first judgement of the form:
//
//	anchor_unrealized — the carrier says this evidence exists; it does not, and
//	                    no completed destruction accounts for its absence.
//
// It is a READ-DERIVED verdict. Nothing here writes, repairs, compacts, audits,
// dispatches or opens a network connection (I4/T330); it reads the anchor stream
// that P46/P47 already read, the five family main ledgers, and ONE destruction
// ledger read shared by every family (ADR-074 §8 — the accounting is loaded once,
// not once per family).
//
// The vocabulary, and the family/global precedence rules, are defined in
// ADR-073 §3 and are NOT redefined here (review M1/M2). Row values carry no
// `anchor_` prefix; the three global fields do.

import (
	"fmt"
)

// ---------------------------------------------------------------------------
// Vocabulary (ADR-073 §3 命名边界 — row/family values carry NO anchor_ prefix)
// ---------------------------------------------------------------------------

// Row-level verdicts: one anchor entry, judged against the family's main ledger.
const (
	realizationRowRealized      = "realized"
	realizationRowCompacted     = "compacted"
	realizationRowOutOfWindow   = "out_of_window"
	realizationRowUnrealized    = "unrealized"
	realizationRowIndeterminate = "indeterminate"
)

// Family-level verdicts (ADR-073 §3, first-hit total order ①②③④).
const (
	realizationFamilyIndeterminate   = realizationRowIndeterminate
	realizationFamilyUnrealized      = realizationRowUnrealized
	realizationFamilyRealized        = realizationRowRealized
	realizationFamilyNothingAssessed = "nothing_assessed"
	// realizationFamilyDelegated is the SIXTH family's permanent value: its
	// realization is judged by a strictly stronger local recompute (ADR-074
	// §2.2), so this face produces NO realized/unrealized claim about it.
	realizationFamilyDelegated = "delegated"
)

// Global scalar values (ADR-073 §3 — first-hit total order, NOT a conjunction).
const (
	realizationStateIndeterminate   = realizationFamilyIndeterminate
	realizationStateUnrealized      = realizationFamilyUnrealized
	realizationStateRealized        = realizationFamilyRealized
	realizationStateNothingAssessed = realizationFamilyNothingAssessed
)

// ---------------------------------------------------------------------------
// Per-family artifact digests (ADR-074 §2.1)
//
// Each function receives the family's OWN record type as produced by that
// family's existing load. A type mismatch is a programming error and is reported
// as such (the caller turns it into `indeterminate`, never into `realized`).
// ---------------------------------------------------------------------------

// realizationLedgerArtifactDigest returns the chain ledger's STORED manifest
// digest. This is the family's declared VACUITY (ADR-074 §2.1/§3, T327): the
// ledger row carries no self-digest, and the ledger is signed with the SAME key
// as the manifests, so a holder of the export key can forge both sides at once.
// The face therefore says `realized` here without proving anything an adversary
// could not reproduce — stated honestly, not hidden.
func realizationLedgerArtifactDigest(rec any) (string, error) {
	r, ok := rec.(ledgerEntry)
	if !ok {
		return "", fmt.Errorf("ledger: unexpected record type %T", rec)
	}
	return r.ManifestDigest, nil
}

// realizationKeyLifecycleArtifactDigest returns the lifecycle event's stored
// event_digest. The load has already re-derived it from the canonical payload
// (loadKeyLifecycleState), so it is content-bound and NOT forgeable without the
// KAK.
func realizationKeyLifecycleArtifactDigest(rec any) (string, error) {
	r, ok := rec.(keyLifecycleEntry)
	if !ok {
		return "", fmt.Errorf("key_lifecycle: unexpected record type %T", rec)
	}
	return r.EventDigest, nil
}

// realizationDestructionArtifactDigest returns the destruction group's
// entry_digest (the group's immutable payload commitment; the load re-derived it
// from the canonical payload, so it is content-bound).
func realizationDestructionArtifactDigest(rec any) (string, error) {
	r, ok := rec.(*destructionGroup)
	if !ok || r == nil {
		return "", fmt.Errorf("destruction: unexpected record type %T", rec)
	}
	return r.entry.EntryDigest, nil
}

// realizationVerificationArtifactDigest RECOMPUTES the report digest from the
// ledger row's fields (I9, load-bearing): the anchor entry stores
// `reportDigest` — sha256 over the report's SEVEN fields (ReportSeq, Subject,
// Evaluable, Items, Overall, Reasons, VerifiedAt; snapshot_verification.go
// reportDigest) — while the ledger row's own `digest` covers ELEVEN fields
// (it adds v, stream_id, key_id, prev_digest). Using verificationEntryDigest
// here would compare two different canonical serializations and make the family
// permanently `unrealized`; T325 pins the difference.
func realizationVerificationArtifactDigest(rec any) (string, error) {
	r, ok := rec.(verificationLogEntry)
	if !ok {
		return "", fmt.Errorf("verification: unexpected record type %T", rec)
	}
	report := &VerificationReport{
		ReportSeq:  r.ReportSeq,
		Subject:    r.Subject,
		Evaluable:  r.Evaluable,
		Items:      r.Items,
		Overall:    r.Overall,
		Reasons:    r.Reasons,
		VerifiedAt: r.VerifiedAt,
	}
	return reportDigest(report)
}

// realizationAcceptanceArtifactDigest returns the acceptance entry's stored
// entry_digest (re-derived by the load from the canonical payload ⇒ KAK-bound).
func realizationAcceptanceArtifactDigest(rec any) (string, error) {
	r, ok := rec.(acceptanceEntry)
	if !ok {
		return "", fmt.Errorf("acceptance: unexpected record type %T", rec)
	}
	return r.EntryDigest, nil
}

// ---------------------------------------------------------------------------
// Identity + content fields (ADR-074 §2.1: what the anchor entry CLAIMS)
// ---------------------------------------------------------------------------

// realizationIdentityAndClaim returns the evidence identity and the artifact
// digest the anchor entry claims for it. A zero identity is never valid and is
// handled as indeterminate by the caller (T335).
func realizationIdentityAndClaim(f witnessFamily, e *anchorEntry) (int64, string) {
	switch f.Name {
	case witnessFamilyLedger:
		return e.PublicationID, e.ManifestDigest
	case witnessFamilyKeyLifecycle:
		return e.EventSeq, e.EventDigest
	case witnessFamilyDestruction:
		return e.DestructionSeq, e.DestructionDigest
	case witnessFamilyVerification:
		return e.ReportSeq, e.ReportDigest
	case witnessFamilyAcceptance:
		return e.AcceptanceSeq, e.AcceptanceDigest
	}
	return 0, ""
}

// realizationClaimFieldsMatch compares the anchor entry's CONTENT fields — the
// fields the family copied from the ledger record at anchor time — with the
// ledger record's own values. It is the second half of the row judgement: a
// matching digest with contradicting content fields is still a false claim.
func realizationClaimFieldsMatch(f witnessFamily, e *anchorEntry, rec any) bool {
	switch f.Name {
	case witnessFamilyLedger:
		// The publication family's only per-entry content is its identity + the
		// manifest digest; both are compared by the caller (identity) and by the
		// digest comparison. Nothing further is copied into the anchor entry.
		r, ok := rec.(ledgerEntry)
		return ok && r.PublicationID == e.PublicationID && r.ManifestDigest == e.ManifestDigest
	case witnessFamilyKeyLifecycle:
		r, ok := rec.(keyLifecycleEntry)
		return ok && r.EventSeq == e.EventSeq && r.EventType == e.EventType && r.NotAfter == e.NotAfter
	case witnessFamilyDestruction:
		r, ok := rec.(*destructionGroup)
		// DestructionVerdict carries the group's terminal state at anchor time
		// (anchorDestructionEntry copies e.State); the group's effective state is
		// the ledger's own value for the same fact.
		return ok && r != nil && r.seq == e.DestructionSeq && r.state == e.DestructionVerdict
	case witnessFamilyVerification:
		r, ok := rec.(verificationLogEntry)
		return ok && r.ReportSeq == e.ReportSeq && r.Overall == e.Overall
	case witnessFamilyAcceptance:
		r, ok := rec.(acceptanceEntry)
		return ok && r.EntrySeq == e.AcceptanceSeq && r.RecordSeq == e.AcceptanceRecordSeq
	}
	return false
}

// ---------------------------------------------------------------------------
// The family main-ledger view (identity → record index + retention window)
// ---------------------------------------------------------------------------

// familyLedgerView is the read-only projection the judgement runs against.
//
// hasUnclassifiable / hasConflict are the two fail-closed axes of ADR-074 §3
// step ②: an unclassifiable line or a self-contradictory record makes the WHOLE
// family indeterminate — there is no skip-and-continue, because skipping is how
// a tampered prefix disappears (the R40-1/I8 discipline, inherited).
type familyLedgerView struct {
	index             map[int64]any
	minSeq            int64
	maxSeq            int64
	entries           int
	hasUnclassifiable bool
	hasConflict       bool
	reason            string
}

// loadFamilyLedger runs the family's EXISTING load (no new reader, no new
// writer) and folds it into one index + window. Nothing is cached: the same
// re-read discipline every family face already follows.
func loadFamilyLedger(f witnessFamily, s *HistoryExportScheduler) (*familyLedgerView, error) {
	v := &familyLedgerView{index: map[int64]any{}}
	switch f.Name {
	case witnessFamilyLedger:
		ls, err := loadLedgerState(s.cfg.Dir, s.trust)
		if err != nil {
			return nil, err
		}
		if len(ls.unusable) > 0 {
			v.hasUnclassifiable = true
			v.reason = fmt.Sprintf("the chain ledger holds %d unusable entry(ies); the family is not judged", len(ls.unusable))
		}
		if len(ls.conflicts) > 0 {
			v.hasConflict = true
			v.reason = fmt.Sprintf("the chain ledger holds %d publication_id(s) with contradictory digests; the family is not judged", len(ls.conflicts))
		}
		for id, e := range ls.usable {
			v.index[id] = e
		}
		v.minSeq, v.maxSeq, v.entries = indexSpan(v.index)
	case witnessFamilyKeyLifecycle:
		st, err := loadKeyLifecycleState(s.keyLifecycleConfig())
		if err != nil {
			return nil, err
		}
		if !st.verifiable {
			v.hasUnclassifiable = true
			v.reason = firstRealizationErr(st.errs, "the key lifecycle ledger cannot be trusted; the family is not judged")
		}
		for _, e := range st.entries {
			v.index[e.EventSeq] = e
		}
		v.minSeq, v.maxSeq, v.entries = st.window.MinSeq, st.window.MaxSeq, st.window.Entries
	case witnessFamilyDestruction:
		st, err := loadDestructionState(s.destructionConfig())
		if err != nil {
			return nil, err
		}
		for _, g := range st.groups {
			if !g.usable {
				// Quarantined (unauthorized / conflict / chain-broken): it never
				// counts as a record this face may compare against.
				v.hasConflict = true
				v.reason = firstRealizationErr(st.errs, "the destruction log holds a quarantined record; the family is not judged")
				continue
			}
			v.index[g.seq] = g
		}
		if !st.verifiable {
			v.hasUnclassifiable = true
			v.reason = firstRealizationErr(st.errs, "the destruction log cannot be trusted; the family is not judged")
		}
		v.minSeq, v.maxSeq, v.entries = st.window.MinSeq, st.window.MaxSeq, st.window.Entries
	case witnessFamilyVerification:
		st, err := loadVerificationState(s.verificationConfig())
		if err != nil {
			return nil, err
		}
		if !st.verifiable {
			v.hasUnclassifiable = true
			v.reason = firstRealizationErr(st.errs, "the verification log cannot be trusted; the family is not judged")
		}
		for _, e := range st.entries {
			v.index[e.ReportSeq] = e
		}
		v.minSeq, v.maxSeq, v.entries = st.window.MinSeq, st.window.MaxSeq, st.window.Entries
	case witnessFamilyAcceptance:
		st, err := loadAcceptanceState(acceptanceLogPath(s.cfg.Dir), s.keyAuthority)
		if err != nil {
			return nil, err
		}
		if !st.verifiable {
			v.hasUnclassifiable = true
			v.reason = firstRealizationErr(st.errs, "the acceptance ledger cannot be trusted; the family is not judged")
		}
		for _, e := range st.entries {
			v.index[e.EntrySeq] = e
		}
		v.minSeq, v.maxSeq, v.entries = st.window.MinEntry, st.window.MaxEntry, st.window.Entries
	default:
		return nil, fmt.Errorf("realization: family %q has no ledger reader", f.Name)
	}
	return v, nil
}

// indexSpan derives the retention window from an index whose keys are the
// evidence identities. An empty index yields the zero span, which makes every
// positive identity fall ABOVE the window — the correct reading of "the ledger
// holds nothing at all" (an extreme tail deletion, never a legal prefix drop).
func indexSpan(index map[int64]any) (int64, int64, int) {
	if len(index) == 0 {
		return 0, 0, 0
	}
	var min, max int64
	first := true
	for id := range index {
		if first || id < min {
			min = id
		}
		if first || id > max {
			max = id
		}
		first = false
	}
	return min, max, len(index)
}

func firstRealizationErr(errs []string, fallback string) string {
	if len(errs) > 0 && errs[0] != "" {
		return errs[0]
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Phase 43 compaction accounting (ADR-074 §3 step ③)
// ---------------------------------------------------------------------------

// seqInterval is one CLOSED destruction interval: from_seq ≤ id ≤ to_seq
// (destructionTarget, snapshot_destruction.go). Adjacent intervals never merge,
// so a gap is never covered by accident.
type seqInterval struct{ from, to int64 }

// realizationAccounting is the destruction face, READ ONCE per status document
// and then queried per family. Only COMPLETED, USABLE groups of the family's own
// kind contribute (ADR-074 §3.1): an `intended` or `aborted` record accounts for
// nothing, and a quarantined record accounts for nothing.
type realizationAccounting struct {
	byKind map[string][]seqInterval
	err    error
}

func (a *realizationAccounting) covers(kind string, id int64) bool {
	if a == nil || kind == "" {
		return false
	}
	for _, iv := range a.byKind[kind] {
		if id >= iv.from && id <= iv.to {
			return true
		}
	}
	return false
}

// loadRealizationAccounting reads the destruction ledger once and indexes every
// completed, usable group's closed target intervals by destruction kind. When
// the destruction face is disabled it returns an empty table (never an error) —
// that is exactly the T323 case: with no accounting, a below-window identity is
// `out_of_window` (unjudgeable), never `unrealized`.
//
// A destruction log that cannot be READ is a different matter and is reported as
// an error: without the accounting the two kinds of "fewer records" (legal
// prefix compaction vs deletion) are indistinguishable, so every family that
// depends on it is fail-closed to `indeterminate` rather than silently
// downgraded.
func loadRealizationAccounting(s *HistoryExportScheduler) *realizationAccounting {
	a := &realizationAccounting{byKind: map[string][]seqInterval{}}
	if s == nil {
		return a
	}
	c := s.destructionConfig()
	if !c.enabled() {
		return a
	}
	st, err := loadDestructionState(c)
	if err != nil {
		a.err = err
		return a
	}
	if !st.verifiable {
		a.err = fmt.Errorf("the destruction log cannot be trusted; the compaction accounting is unavailable")
		return a
	}
	for _, g := range st.groups {
		if !g.usable || g.state != destructionStateCompleted {
			continue
		}
		kind := g.entry.Kind
		if kind == "" {
			continue
		}
		for _, t := range g.entry.Targets {
			from, to := t.FromSeq, t.ToSeq
			if from == 0 && to == 0 {
				// A publication-face target: not a sequence range.
				continue
			}
			if from == 0 {
				from = to
			}
			if to == 0 {
				to = from
			}
			if from > to {
				from, to = to, from
			}
			if from <= 0 {
				continue
			}
			a.byKind[kind] = append(a.byKind[kind], seqInterval{from: from, to: to})
		}
	}
	return a
}

// ---------------------------------------------------------------------------
// The per-family judgement (ADR-074 §3)
// ---------------------------------------------------------------------------

// familyRealization is one family's derived result. The counters are the honest
// accounting of HOW the verdict was reached: `checked` counts the rows whose
// claim was actually compared against a recomputed/stored ledger digest, which
// is what makes `realized` non-vacuous (I5).
type familyRealization struct {
	name        string
	verdict     string
	reason      string
	checked     int
	realized    int
	compacted   int
	outOfWindow int
	unrealized  int
	unusable    int
	window      *anchorWindow
}

// realizeFamily judges ONE family. Fail-closed and family-isolated: any
// unreadable or self-contradictory input makes THIS family indeterminate and
// leaves every other family's value untouched (I8/T324).
func realizeFamily(f witnessFamily, s *HistoryExportScheduler, acc *realizationAccounting) familyRealization {
	res := familyRealization{name: f.Name}

	// ① Anchor side — loaded WITH the trust anchor, so entries that do not
	// verify are VISIBLE as unusable (unlike P46's signature-blind load). A
	// forged-but-unsigned claim must never be judged as if it were sound, and it
	// must never be silently skipped either (T329).
	ast, err := loadAnchorStatePath(f.AnchorPath(s.cfg.Dir), s.cfg.Dir, s.trust)
	if err != nil {
		res.verdict = realizationFamilyIndeterminate
		res.reason = "the family's anchor stream cannot be evaluated: " + err.Error()
		return res
	}
	w := ast.window
	res.window = &w
	res.unusable = len(ast.unusable)
	if len(ast.conflicts) > 0 {
		res.verdict = realizationFamilyIndeterminate
		res.reason = fmt.Sprintf("the anchor stream holds %d conflicting anchor_seq(s) %v; the family is not judged", len(ast.conflicts), ast.conflicts)
		return res
	}
	if len(ast.unusable) > 0 {
		res.verdict = realizationFamilyIndeterminate
		res.reason = fmt.Sprintf("the anchor stream holds %d anchor entry(ies) that do not verify; the family is not judged", len(ast.unusable))
		return res
	}

	// ② Delegation (ADR-074 §2.2). The sixth family's realization is judged by a
	// strictly stronger local recompute elsewhere; this face makes NO
	// realized/unrealized claim about it.
	if f.ArtifactDigest == nil {
		res.verdict = realizationFamilyDelegated
		return res
	}

	// ③ Ledger side — the family's existing load, fail-closed on either axis.
	lst, lerr := loadFamilyLedger(f, s)
	if lerr != nil {
		res.verdict = realizationFamilyIndeterminate
		res.reason = "the family's main ledger cannot be evaluated: " + lerr.Error()
		return res
	}
	if lst.hasUnclassifiable || lst.hasConflict {
		res.verdict = realizationFamilyIndeterminate
		res.reason = lst.reason
		return res
	}

	// ④ Accounting side — the shared, already-loaded destruction index.
	if acc != nil && acc.err != nil && f.CompactionKind != "" {
		res.verdict = realizationFamilyIndeterminate
		res.reason = acc.err.Error()
		return res
	}

	// ⑤ Per-entry judgement, ascending anchor_seq (a total order, so the verdict
	// does not depend on map iteration).
	for _, seq := range ast.seqs() {
		e := ast.latest[seq]
		id, claim := realizationIdentityAndClaim(f, &e)
		if id <= 0 {
			res.verdict = realizationFamilyIndeterminate
			res.reason = fmt.Sprintf("anchor_seq %d carries no evidence identity (zero); the family is not judged", seq)
			return res
		}
		switch {
		case id < lst.minSeq:
			// Below the ledger's retention window: the two reasons a record can
			// be missing there are INDISTINGUISHABLE without Phase 43 accounting.
			if acc.covers(f.CompactionKind, id) {
				res.compacted++
			} else {
				res.outOfWindow++
			}
		case id > lst.maxSeq:
			// Above the window: the ledger's tail was removed while the anchor
			// entry — which is written AFTER the ledger row (R40-6) — remains.
			// There is no legal producer of that state.
			res.unrealized++
		default:
			rec, ok := lst.index[id]
			if !ok {
				// Inside the window but absent: a legal prefix compaction cannot
				// explain a hole in the retained range.
				res.unrealized++
				break
			}
			dg, derr := f.ArtifactDigest(rec)
			if derr != nil {
				res.verdict = realizationFamilyIndeterminate
				res.reason = fmt.Sprintf("anchor_seq %d: the ledger record's artifact digest cannot be derived: %v", seq, derr)
				return res
			}
			// The ONE comparison this Phase exists for: the anchor entry's claim
			// versus the ledger's own (stored or recomputed) digest, plus the
			// copied content fields. `realized` is produced by equality, never by
			// the absence of a counter-example (I5).
			res.checked++
			if dg != claim || !realizationClaimFieldsMatch(f, &e, rec) {
				res.unrealized++
			} else {
				res.realized++
			}
		}
	}

	// ⑥ Family-level first-hit total order (ADR-073 §3): indeterminate >
	// unrealized > realized > nothing_assessed. An empty row set — or a row set
	// that was never actually checked — is NEVER `realized` (the empty set is not
	// "everything checks out").
	switch {
	case res.unrealized > 0:
		res.verdict = realizationFamilyUnrealized
	case res.realized > 0:
		res.verdict = realizationFamilyRealized
	default:
		res.verdict = realizationFamilyNothingAssessed
		if len(ast.latest) == 0 {
			res.reason = "the family's anchor window is empty: no anchor entry claimed anything to check"
		} else {
			res.reason = fmt.Sprintf("no anchor claim was actually checked (%d row(s), all compacted/out_of_window)", len(ast.latest))
		}
	}
	// A row that could not be judged keeps its reason even when the family as a
	// whole has a verdict (A5: an unjudgeable claim must never be presented as a
	// clean one — the counter alone would read as a statistic).
	if res.outOfWindow > 0 && res.verdict != realizationFamilyIndeterminate && res.verdict != realizationFamilyNothingAssessed {
		note := fmt.Sprintf("%d anchor claim(s) lie below the ledger's retention window and no completed %q destruction accounts for them: legal prefix compaction and deletion are indistinguishable here (no assertion, no waiver, silence)", res.outOfWindow, f.CompactionKind)
		if res.reason == "" {
			res.reason = note
		} else {
			res.reason = res.reason + "; " + note
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// Status face (ADR-074 §4 — read-only, zero side effects)
// ---------------------------------------------------------------------------

// anchorRealizationFamilyStatus is ONE family's row of the `anchor_realization`
// group. Every counter is omitempty so a row that counted nothing stays compact;
// `verdict` is never omitted, because "we cannot say" must always be visible.
type anchorRealizationFamilyStatus struct {
	Verdict     string        `json:"verdict"`
	Checked     int           `json:"checked,omitempty"`
	Realized    int           `json:"realized,omitempty"`
	Compacted   int           `json:"compacted,omitempty"`
	OutOfWindow int           `json:"out_of_window,omitempty"`
	Unrealized  int           `json:"unrealized,omitempty"`
	Unusable    int           `json:"unusable,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	Window      *anchorWindow `json:"window,omitempty"`
}

// anchorRealizationStatusSummary is the `anchor_realization` group. The six
// family rows are NAMED fields, not a slice: the table is closed at compile
// time, so a seventh family cannot be added to the registry without a
// deliberate decision about its realization ownership (ADR-073 §3 / I10).
type anchorRealizationStatusSummary struct {
	Ledger             anchorRealizationFamilyStatus `json:"ledger"`
	KeyLifecycle       anchorRealizationFamilyStatus `json:"key_lifecycle"`
	Destruction        anchorRealizationFamilyStatus `json:"destruction"`
	Verification       anchorRealizationFamilyStatus `json:"verification"`
	Acceptance         anchorRealizationFamilyStatus `json:"acceptance"`
	ProtectionDecision anchorRealizationFamilyStatus `json:"protection_decision"`

	// State is the GLOBAL scalar (ADR-073 §3): first-hit total order over the
	// NON-delegated families — indeterminate > unrealized > realized >
	// nothing_assessed. The delegated family participates in none of these steps.
	State string `json:"anchor_realization_state"`
	// AnchorRealized is a DERIVED convenience value — `State == "realized"` and
	// nothing else. It is deliberately not a second definition.
	AnchorRealized bool `json:"anchor_realized"`
	// Claims is the number of anchor claims actually compared in this read. It is
	// the non-vacuity witness: AnchorRealized ⇒ Claims > 0.
	Claims                int      `json:"anchor_realization_claims"`
	UnrealizedFamilies    []string `json:"anchor_realization_unrealized_families,omitempty"`
	IndeterminateFamilies []string `json:"anchor_realization_indeterminate_families,omitempty"`
}

// realizationRowStatus converts the internal result into its JSON row.
func realizationRowStatus(res familyRealization) anchorRealizationFamilyStatus {
	return anchorRealizationFamilyStatus{
		Verdict:     res.verdict,
		Checked:     res.checked,
		Realized:    res.realized,
		Compacted:   res.compacted,
		OutOfWindow: res.outOfWindow,
		Unrealized:  res.unrealized,
		Unusable:    res.unusable,
		Reason:      res.reason,
		Window:      res.window,
	}
}

// assignRealizationRow writes one family's row into the closed table. An unknown
// name is reported to the caller (never silently dropped): a family that exists
// in the registry but has no realization row is a closed-table violation.
func assignRealizationRow(out *anchorRealizationStatusSummary, name string, res familyRealization) bool {
	row := realizationRowStatus(res)
	switch name {
	case witnessFamilyLedger:
		out.Ledger = row
	case witnessFamilyKeyLifecycle:
		out.KeyLifecycle = row
	case witnessFamilyDestruction:
		out.Destruction = row
	case witnessFamilyVerification:
		out.Verification = row
	case witnessFamilyAcceptance:
		out.Acceptance = row
	case witnessFamilyProtectionDecision:
		out.ProtectionDecision = row
	default:
		return false
	}
	return true
}

// AnchorRealizationStatus derives the realization face for the whole registry.
//
// Strictly read-only (I4/T330): it stats nothing, writes nothing, audits nothing,
// dispatches nothing, compacts nothing and opens no connection. The only I/O is
// the anchor streams P46/P47 already read, the five family main ledgers and ONE
// destruction ledger read shared by every family.
func (s *HistoryExportScheduler) AnchorRealizationStatus() anchorRealizationStatusSummary {
	acc := loadRealizationAccounting(s)
	out := anchorRealizationStatusSummary{}
	verdicts := make(map[string]familyRealization, 6)
	unknown := []string{}
	for _, f := range witnessFamilyRegistry() {
		res := realizeFamily(f, s, acc)
		verdicts[f.Name] = res
		if !assignRealizationRow(&out, f.Name, res) {
			unknown = append(unknown, f.Name)
		}
	}

	claims := 0
	unrealized := []string{}
	indeterminate := []string{}
	anyRealized := false
	for _, f := range witnessFamilyRegistry() {
		res := verdicts[f.Name]
		if res.verdict == realizationFamilyDelegated {
			// ADR-073 §3: the delegated family takes part in NO step.
			continue
		}
		claims += res.checked
		switch res.verdict {
		case realizationFamilyIndeterminate:
			indeterminate = append(indeterminate, f.Name)
		case realizationFamilyUnrealized:
			unrealized = append(unrealized, f.Name)
		case realizationFamilyRealized:
			anyRealized = true
		}
	}
	// A registry family with no row is a closed-table violation: it must be loud,
	// never a silently unjudged family.
	indeterminate = append(indeterminate, unknown...)

	state := realizationStateNothingAssessed
	switch {
	case len(indeterminate) > 0:
		state = realizationStateIndeterminate
	case len(unrealized) > 0:
		state = realizationStateUnrealized
	case anyRealized:
		state = realizationStateRealized
	}
	out.State = state
	out.AnchorRealized = state == realizationStateRealized
	out.Claims = claims
	out.UnrealizedFamilies = unrealized
	out.IndeterminateFamilies = indeterminate
	return out
}
