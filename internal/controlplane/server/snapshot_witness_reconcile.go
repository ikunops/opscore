package server

// Phase 46 — Witness Reconciliation (ADR-067 scope, ADR-068 architecture).
//
// What P39/P41/P42/P43/P45 each paid five times: deleting a family's main
// ledger AND its anchor log silences every local check, and the only memory
// that the family ever existed survives OUT of domain, on the witness. Phase
// 46 turns that out-of-domain observability into an in-domain assertion: the
// caller submits the projection bytes the witness actually holds (the Phase 40
// anchorRequest wire shape, verbatim — feed it witness.jsonl lines), and the
// local side derives the identity, walks the absence matrix, and runs the
// three-segment window against the family's own anchor log.
//
// What this face deliberately does NOT do (ADR-067 A7-3, review BLOCKER-1):
// verify signatures. A projection carries a signature over a payload it does
// not carry, so it cannot be verified — P40's verification duty has always
// lived in the local load, and in the deleted scenario the local file is gone.
// Identity here is the DERIVED key_id/stream_id match (R40-2), and the trust
// level of the whole face is the caller responsibility of P40 §7.3.
//
// ZERO SIDE EFFECTS (ADR-068 I5 / T268): every function in this file only
// READS. No file is written, no audit entry is appended, no network is opened.

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Per-family verdicts (ADR-067 §3 / ADR-068 §3). The priority order when
// several conclusions apply at once is frozen: unverifiable > truncated >
// divergent > incomplete > intact.
const (
	witnessVerdictNotProvided  = "family_not_provided"
	witnessVerdictUnverifiable = "family_unverifiable"
	witnessVerdictDeleted      = "family_ledger_deleted"
	witnessVerdictTruncated    = "family_ledger_truncated"
	witnessVerdictDivergent    = "family_divergent"
	witnessVerdictIncomplete   = "family_incomplete"
	witnessVerdictIntact       = "family_intact"
)

// witnessSeqRange is one contiguous run of witness anchor_seqs ABOVE the local
// window's upper bound (one element of truncated_ranges, ADR-068 §4).
type witnessSeqRange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// witnessFamilyResult is the per-family row of the POST response (ADR-068 §4).
type witnessFamilyResult struct {
	Verdict string `json:"verdict"`
	// WitnessEntries is the number of projections the family submitted.
	WitnessEntries int `json:"witness_entries"`
	// IdentityOK reports that every submitted projection matched the locally
	// derived key_id/stream_id (R40-2). False on ANY identity mismatch (the
	// poison, I2) and on an empty submission (no identity was established).
	IdentityOK bool `json:"identity_ok"`
	// OutsideCount counts projections BELOW the local window's lower bound:
	// legal prefix compaction and deletion are indistinguishable there
	// (R40-1), so they take part in NO assertion (T270).
	OutsideCount int `json:"outside_count"`
	// DivergentSeqs lists in-window seqs whose witness digest disagrees with
	// the locally derived digest of the same seq (T263).
	DivergentSeqs []int64 `json:"divergent_seqs"`
	// TruncatedRanges lists the contiguous seq runs ABOVE the local window's
	// upper bound (T262).
	TruncatedRanges []witnessSeqRange `json:"truncated_ranges"`
	// MissingCount counts local anchor entries no projection covers: witness
	// retention is unknowable, so missing reads incomplete, never broken
	// (ADR-067 A8-3, T264).
	MissingCount int `json:"missing_count"`
	// Error carries the loud reason a family could not be concluded about.
	Error string `json:"error,omitempty"`
}

// witnessReconcileResult is the aggregate POST response.
type witnessReconcileResult struct {
	Families map[string]witnessFamilyResult `json:"families"`
}

// ReconcileWitnessFamilies runs the per-family reconciliation over the
// caller-grouped witness projections (ADR-068 §3). The R40-2 identity is
// derived ONCE — it is a property of the export directory + signing key, not
// of a family — and handed to every family check.
func (s *HistoryExportScheduler) ReconcileWitnessFamilies(submitted map[string][]anchorRequest) (witnessReconcileResult, error) {
	res := witnessReconcileResult{Families: map[string]witnessFamilyResult{}}
	if s == nil {
		return res, fmt.Errorf("witness reconcile: scheduled history export is not configured")
	}
	registry := witnessFamilyRegistry()
	identity, ierr := deriveAnchorIdentity(s.signer, s.trust, s.cfg.Dir)
	if ierr != nil {
		// Loud, per ADR-067 A8-5: without a derivable identity nothing about
		// any submitted family is assertable (and none of it is a shrug).
		for _, f := range registry {
			row := newWitnessFamilyResult(len(submitted[f.Name]))
			row.IdentityOK = false
			if len(submitted[f.Name]) > 0 {
				row.Verdict = witnessVerdictUnverifiable
				row.Error = ierr.Error()
			} else {
				row.Verdict = witnessVerdictNotProvided
			}
			res.Families[f.Name] = row
		}
		return res, nil
	}
	for _, f := range registry {
		res.Families[f.Name] = reconcileWitnessFamily(f, s.cfg.Dir, identity, submitted[f.Name])
	}
	return res, nil
}

// newWitnessFamilyResult seeds a row with the empty (never nil) lists the JSON
// face promises.
func newWitnessFamilyResult(entries int) witnessFamilyResult {
	return witnessFamilyResult{
		Verdict:         witnessVerdictIntact,
		WitnessEntries:  entries,
		IdentityOK:      true,
		DivergentSeqs:   []int64{},
		TruncatedRanges: []witnessSeqRange{},
	}
}

// reconcileWitnessFamily is the ADR-068 §3 algorithm for ONE family, in the
// frozen order: identity poison first (I2), then the absence matrix (I4),
// then the three-segment window (I3), then the verdict priority.
func reconcileWitnessFamily(f witnessFamily, dir string, identity *anchorIdentity, items []anchorRequest) witnessFamilyResult {
	res := newWitnessFamilyResult(len(items))
	if len(items) == 0 {
		// No projection for this family ⇒ no conclusion of any kind. Nothing
		// here may read as a deletion claim: absence of a submission is not
		// absence of a ledger.
		res.Verdict = witnessVerdictNotProvided
		res.IdentityOK = false // no identity was established
		return res
	}

	// ---- identity (poison semantics, I2 / review MAJOR-1): ONE mismatching
	// projection makes the WHOLE family unverifiable. Never skip-and-continue:
	// skipping is how a forged entry rides along an honest batch.
	for i := range items {
		if !identity.accepts(items[i].KeyID, items[i].StreamID) {
			res.Verdict = witnessVerdictUnverifiable
			res.IdentityOK = false
			res.Error = "a projection's key_id/stream_id does not match the locally derived identity"
			return res
		}
	}

	// ---- projection sanity: a malformed or duplicated entry is a refusal to
	// conclude, never an assertion (the P40 malformed discipline, carried to
	// the fifth stream's reconcile face).
	seen := map[int64]bool{}
	for i := range items {
		it := items[i]
		if it.AnchorSeq <= 0 || it.AnchorDigest == "" {
			res.Verdict = witnessVerdictUnverifiable
			res.Error = fmt.Sprintf("projection for anchor_seq %d is malformed (non-positive seq or empty anchor_digest)", it.AnchorSeq)
			return res
		}
		if seen[it.AnchorSeq] {
			res.Verdict = witnessVerdictUnverifiable
			res.Error = fmt.Sprintf("anchor_seq %d is submitted twice in one family; the witness-held byte sequence is ambiguous", it.AnchorSeq)
			return res
		}
		seen[it.AnchorSeq] = true
	}

	// ---- absence matrix (I4 / review MAJOR-2 / ADR-067 A5): the four
	// quadrants decide BEFORE any window comparison, so a missing file can
	// never masquerade as a truncated one (T266).
	ledgerAbsent := !witnessFilePresent(f.LedgerPath(dir))
	anchorAbsent := !witnessFilePresent(f.AnchorPath(dir))
	switch {
	case ledgerAbsent && anchorAbsent:
		// The core payoff (ADR-067 §3): the witness holds identity-matched
		// entries for a family whose local evidence is GONE — both files.
		// Deleting a family ledger and its anchor log silences P35~P45; it
		// cannot silence this verdict. Every submitted item is identity-matched
		// here (the poison ran first), so the entries ARE this deployment's.
		res.Verdict = witnessVerdictDeleted
		return res
	case ledgerAbsent || anchorAbsent:
		// Either half alone: deletion and never-anchored are locally
		// indistinguishable (A5) — incomplete, NEVER truncated, NEVER deleted.
		res.Verdict = witnessVerdictIncomplete
		return res
	}

	// ---- local window: the family's existing anchor load (ADR-068 §2/§3).
	// ONE read-only load feeds the window, the per-seq digests and the
	// missing-direction set.
	st, err := loadAnchorStatePath(f.AnchorPath(dir), dir, nil)
	if err != nil {
		res.Verdict = witnessVerdictUnverifiable
		res.Error = err.Error()
		return res
	}
	if st.window.Entries == 0 || !st.window.Continuous {
		// A legal anchor log is contiguous (P40 I4). An empty or gapped window
		// cannot bound an assertion, so NOTHING is assertable — fail-closed.
		res.Verdict = witnessVerdictUnverifiable
		res.Error = "the local anchor window is empty or discontinuous; refusing to assert"
		return res
	}
	if len(st.conflicts) > 0 {
		// The shared loader deletes a conflicted seq from latest BEFORE
		// computing the window (snapshot_anchor.go), so a same-seq
		// contradiction in the local log shifts the window boundary and would
		// misfile the witness's seq: at the window's max boundary the window
		// shrinks and the seq reads a FALSE family_ledger_truncated (ADR-068
		// §3's upper-bound uniqueness proof presumes a self-consistent log);
		// at the min boundary it silently falls into the outside branch and
		// reads a FALSE family_intact. The log the family's own I5 discipline
		// calls a violation bounds NO assertion — fail-closed.
		res.Verdict = witnessVerdictUnverifiable
		res.Error = fmt.Sprintf("the local anchor log records contradictory payloads for anchor_seq(s) %v; refusing to assert", st.conflicts)
		return res
	}

	// ---- three-segment window over the anchor_seq axis (I3 / review
	// BLOCKER-2: NO waiver layer).
	var divergent []int64
	var above []int64
	outside := 0
	hole := false
	digestError := ""
	covered := map[int64]bool{}
	for i := range items {
		seq := items[i].AnchorSeq
		covered[seq] = true
		switch {
		case seq < st.window.MinSeq:
			// Below the lower bound: legal prefix compaction and deletion are
			// indistinguishable (R40-1) — no assertion, no waiver, silence.
			outside++
		case seq <= st.window.MaxSeq:
			if e, inLocal := st.latest[seq]; inLocal {
				dg, derr := digestOfLocalAnchor(&e)
				if derr != nil {
					digestError = derr.Error()
					break
				}
				if dg != items[i].AnchorDigest {
					// Same seq, different statement: the witness remembers a
					// different entry than the local log records (T263).
					divergent = append(divergent, seq)
				}
			} else if dg, ok := f.LocalDigest(dir, seq); ok {
				// The entry set from the window load missed it, but the
				// family's own per-seq accessor (ADR-068 §2) finds it on a
				// fresh read: compare against THAT (a concurrent O_APPEND
				// append between the two reads is the family GET faces' own
				// accepted concurrency stance).
				if dg != items[i].AnchorDigest {
					divergent = append(divergent, seq)
				}
			} else {
				// Inside the window but in NO local read: a window hole. The
				// window bounds cannot be trusted ⇒ nothing is assertable.
				hole = true
			}
		default:
			// Above the upper bound: R40-6 makes the pending line durable
			// BEFORE dispatch, so local max >= witness max in every honest
			// state; and compaction only ever trims the PREFIX. A seq above
			// the local upper bound has exactly one origin — the tail was
			// deleted (P45 F2: no legal exit, no waiver layer). T262.
			above = append(above, seq)
		}
	}
	if digestError != "" {
		res.Verdict = witnessVerdictUnverifiable
		res.Error = digestError
		return res
	}
	res.OutsideCount = outside
	sort.Slice(divergent, func(i, j int) bool { return divergent[i] < divergent[j] })
	res.DivergentSeqs = divergent
	res.TruncatedRanges = witnessSeqRanges(above)
	if hole {
		res.Verdict = witnessVerdictUnverifiable
		res.Error = "a submitted seq falls inside the local window but is absent from the local anchor log (window hole); refusing to assert"
		return res
	}

	// ---- missing direction: local entries no projection covers. Witness
	// retention is unknowable ⇒ incomplete, never broken (T264).
	missing := 0
	for seq := range st.latest {
		if !covered[seq] {
			missing++
		}
	}
	res.MissingCount = missing

	// ---- verdict priority (ADR-068 §3): unverifiable > truncated >
	// divergent > incomplete > intact.
	switch {
	case len(above) > 0:
		res.Verdict = witnessVerdictTruncated
	case len(divergent) > 0:
		res.Verdict = witnessVerdictDivergent
	case missing > 0:
		res.Verdict = witnessVerdictIncomplete
	default:
		res.Verdict = witnessVerdictIntact
	}
	return res
}

// digestOfLocalAnchor derives the anchor_digest of a local anchor entry — the
// same derivation dispatchAnchorPath applied when the projection was sent.
func digestOfLocalAnchor(e *anchorEntry) (string, error) {
	return anchorDigestOf(e)
}

// witnessSeqRanges folds a set of seqs into contiguous [from,to] runs.
func witnessSeqRanges(seqs []int64) []witnessSeqRange {
	if len(seqs) == 0 {
		return []witnessSeqRange{}
	}
	sorted := append([]int64(nil), seqs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := []witnessSeqRange{{From: sorted[0], To: sorted[0]}}
	for _, v := range sorted[1:] {
		last := &out[len(out)-1]
		switch {
		case v == last.To:
			// duplicate — already deduped upstream, keep the fold total.
		case v == last.To+1:
			last.To = v
		default:
			out = append(out, witnessSeqRange{From: v, To: v})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The GET face (ADR-068 §4): the local five-family existence / window /
// not_enabled probe.
// ---------------------------------------------------------------------------

// witnessFamilyLocalView is one family's row of the GET probe. The vocabulary
// is DELETED-FREE by construction (I6 / review MAJOR-3 / T273): the face
// reports exactly the existence facts the POST absence matrix consumes, so
// (a) a file present here makes family_ledger_deleted unreachable on the POST
// face, and (b) a POST deleted verdict implies both presents false here. The
// two faces CANNOT disagree about a file state.
type witnessFamilyLocalView struct {
	LedgerPresent bool `json:"ledger_present"`
	AnchorPresent bool `json:"anchor_present"`
	// AnchorWindow is the local anchor log's window, when that file is present
	// and parseable (nil otherwise — never a fabricated window).
	AnchorWindow *anchorWindow `json:"anchor_window,omitempty"`
	// NotEnabled reports that the family's producer gate is OFF in the current
	// configuration: absence of files under this flag reads "never ran", not
	// "deleted" (T269 — the frozen load-bearing premise: disabling never
	// deletes files, ADR-067 A7-7).
	NotEnabled bool `json:"not_enabled,omitempty"`
	// Error carries the loud reason the anchor window could not be read.
	Error string `json:"error,omitempty"`
}

// witnessReconcileLocalView is the GET response.
type witnessReconcileLocalView struct {
	Dir      string                            `json:"dir"`
	Families map[string]witnessFamilyLocalView `json:"families"`
}

// WitnessReconcileLocalView builds the GET probe (read-only, never creates a
// file, never asserts a verdict).
func (s *HistoryExportScheduler) WitnessReconcileLocalView() witnessReconcileLocalView {
	view := witnessReconcileLocalView{Dir: s.cfg.Dir, Families: map[string]witnessFamilyLocalView{}}
	if s == nil {
		return view
	}
	for _, f := range witnessFamilyRegistry() {
		row := witnessFamilyLocalView{
			LedgerPresent: witnessFilePresent(f.LedgerPath(s.cfg.Dir)),
			AnchorPresent: witnessFilePresent(f.AnchorPath(s.cfg.Dir)),
			NotEnabled:    !witnessFamilyEnabled(s, f.Name),
		}
		if row.AnchorPresent {
			st, err := loadAnchorStatePath(f.AnchorPath(s.cfg.Dir), s.cfg.Dir, nil)
			if err != nil {
				row.Error = err.Error()
			} else if st.window.Entries > 0 {
				w := st.window
				row.AnchorWindow = &w
			}
		}
		view.Families[f.Name] = row
	}
	return view
}

// witnessReconcileRequest is the POST body (ADR-067 A1): projections grouped
// by family, each item ONE anchorRequest exactly as the witness holds it. The
// type is decoded (never re-serialized), so the witness-held bytes survive the
// round trip byte-for-byte.
type witnessReconcileRequest struct {
	Families map[string][]anchorRequest `json:"families"`
}

// decodeWitnessReconcileRequest parses and validates the POST body: the
// families object is required and every name must be one of the five
// registered families (the registry is closed, ADR-068 §2).
func decodeWitnessReconcileRequest(raw []byte) (map[string][]anchorRequest, error) {
	var payload witnessReconcileRequest
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("malformed request body")
	}
	if payload.Families == nil {
		return nil, fmt.Errorf("the families object is required")
	}
	for name := range payload.Families {
		if !witnessFamilyKnown(name) {
			return nil, fmt.Errorf("unknown witness-reconcile family %q", name)
		}
	}
	return payload.Families, nil
}
