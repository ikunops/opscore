package server

// Phase 47 — Anchor Delivery Reliability (ADR-069 scope, ADR-070 architecture).
//
// P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45
// what-accepted / P46 whether-witnessed. NONE of them asked the one question
// this Phase asks: is an anchor DELIVERY OBLIGATION settled?
//
// Five families anchor into five independent streams (P40/P41/P42/P43/P45). The
// crash-recovery face only ever covered TWO of them (chain-anchor via
// anchorHousekeeping, acceptance-anchor via dispatchAcceptancePending); the other
// three (key_lifecycle, verification, destruction) had inline dispatch only, so
// a crash between "the pending line is durable" and "the state advance is
// durable" left an entry stranded `pending` forever — invisible AND
// uncompactible (compactAnchorPrefixPathObserved refuses to evict an
// unconfirmed group). This file closes that gap:
//
//   - one sweeper per stream, statically partitioned (I1);
//   - `unanchored` is terminal and never revived (I2);
//   - an unreadable line OR a conflicting seq makes the WHOLE stream not-swept,
//     byte-for-byte, with the failure loud (I3);
//   - the read face is derived on every call and has ZERO side effects (I4/A5);
//   - the state advance is byte-identical to the frozen dispatchAnchorPath —
//     the ONLY difference is the family observer (I5), forced by the frozen
//     face (snapshot_anchor.go is zero-diff);
//   - `converged` is about the state machine being AT REST, never about
//     delivery success: a dispatch result (including the legal terminal
//     `unanchored`) NEVER participates (I6/M6).
//
// Everything here is READ-DERIVED. There is no new scheduler field, hence no
// sticky-error lifecycle to reason about (ADR-070 §4). Failures go to
// s.logger only; the pre-existing error faces (AnchorError, destruction.error)
// are deliberately NOT written by the sweep (A6/I7).

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// The three static sweeper owners (ADR-070 §2, A2). The partition is frozen: a
// stream is swept by exactly one of them, and no stream is covered twice (I1).
const (
	anchorSweeperHousekeeping      = "anchor_housekeeping"
	anchorSweeperAcceptancePending = "acceptance_pending"
	anchorSweeperDeliverySweep     = "delivery_sweep"
)

// anchorDeliveryStream is one anchored family's delivery coordinates. It is a
// static, frozen row (ADR-070 §2): Family names match the P46 witness family
// registry, AnchorPath is the stream's own anchor log, SweptBy names the ONE
// sweeper that owns recovery for it, DispatchMu is the per-stream dispatch
// mutex (I8), and Observer is the compaction-accounting hook the stream's own
// compactions install.
//
// Observer is not a new design: it is the SAME hook the stream's producer
// already uses, so a sweep-triggered compaction is indistinguishable from a
// producer-triggered one (I5). The destruction stream carries nil because its
// own anchor log's compaction is deliberately unobserved (its ADR-061 I5
// self-termination rule — observing it would record a destruction whose
// dispatch appends back into the same file).
type anchorDeliveryStream struct {
	Family     string
	AnchorPath func(dir string) string
	SweptBy    string
	DispatchMu *sync.Mutex
	Observer   func(*HistoryExportScheduler) compactionObserver
}

// keyLifecycleDispatchMu and verificationDispatchMu are the two NEW per-stream
// dispatch mutexes (I8). Before P47 neither stream had any anchor-dispatch
// mutex: the sweep is a SECOND dispatcher, so "exactly one dispatcher per
// stream at any instant" only becomes an invariant once the producer call sites
// are wrapped too (history_export_scheduler.go AppendKeyLifecycleEvent,
// snapshot_verification.go AttestVerification).
//
// Lock order (I8): L1 (these two) → L2 (destructionDispatchMu) → L3
// (destructionWriteMu) → L4 (s.mu). The one allowed nesting is L1 → L2: a
// dispatch under L1 may compact the stream's anchor log, and that compaction's
// destruction observer synchronously reaches completeDestruction →
// dispatchDestructionPending (L2). No path ever takes L2 → L1, so the partial
// order is acyclic. Taking them in the reverse order is a deadlock and is
// forbidden.
var (
	keyLifecycleDispatchMu sync.Mutex
	verificationDispatchMu sync.Mutex
)

// anchorDeliveryStreams is the static five-family table (ADR-070 §2). It is the
// single source of truth for both the partition test (T277) and the read face's
// `swept_by` column.
func anchorDeliveryStreams() []anchorDeliveryStream {
	return []anchorDeliveryStream{
		{
			Family:     witnessFamilyLedger,
			AnchorPath: anchorLogPath,
			SweptBy:    anchorSweeperHousekeeping,
		},
		{
			Family:     witnessFamilyKeyLifecycle,
			AnchorPath: keyLifecycleAnchorPath,
			SweptBy:    anchorSweeperDeliverySweep,
			DispatchMu: &keyLifecycleDispatchMu,
			Observer: func(s *HistoryExportScheduler) compactionObserver {
				return s.destructionObserver(destructionKindAnchorCompaction)
			},
		},
		{
			Family:     witnessFamilyVerification,
			AnchorPath: verificationAnchorPath,
			SweptBy:    anchorSweeperDeliverySweep,
			DispatchMu: &verificationDispatchMu,
			Observer: func(s *HistoryExportScheduler) compactionObserver {
				return s.destructionObserver(destructionKindAnchorCompaction)
			},
		},
		{
			Family:     witnessFamilyDestruction,
			AnchorPath: destructionAnchorPath,
			SweptBy:    anchorSweeperDeliverySweep,
			DispatchMu: &destructionDispatchMu,
			// The destruction anchor stream's own compaction is deliberately
			// unobserved (ADR-061 I5 self-termination rule).
			Observer: func(*HistoryExportScheduler) compactionObserver { return nil },
		},
		{
			Family:     witnessFamilyAcceptance,
			AnchorPath: acceptanceAnchorPath,
			SweptBy:    anchorSweeperAcceptancePending,
		},
	}
}

// deliverySweepStreams returns the streams P47 owns, in the FROZEN order
// lifecycle → verification → destruction (ADR-070 §2/§3).
func deliverySweepStreams() []anchorDeliveryStream {
	var out []anchorDeliveryStream
	for _, st := range anchorDeliveryStreams() {
		if st.SweptBy == anchorSweeperDeliverySweep {
			out = append(out, st)
		}
	}
	return out
}

// warnAnchorDelivery records one sweep diagnostic. The sweep writes NOTHING to
// the existing error faces (A6/I7): a delivery failure is not a P40/P43
// judgement, and folding it in would poison those verdicts. With no logger
// configured the diagnostic is deliberately dropped rather than misrouted.
func (s *HistoryExportScheduler) warnAnchorDelivery(family, msg string, err error) {
	if s == nil || s.logger == nil {
		return
	}
	if err != nil {
		s.logger.Warn("anchor delivery sweep: "+msg, "family", family, "err", err)
		return
	}
	s.logger.Warn("anchor delivery sweep: "+msg, "family", family)
}

// dispatchAnchorPathObserved is the Phase 47 form of dispatchAnchorPath
// (snapshot_anchor.go:948-1007). It is a VERBATIM copy of that function's
// semantics with exactly ONE degree of freedom added: the compaction observer.
//
// The copy is forced, not chosen (ADR-070 §3): the producer paths for
// key_lifecycle and verification MUST account for a compaction the dispatch may
// trigger (otherwise a sweep-triggered prefix compaction would drop a group
// without accounting — an I5 violation), but snapshot_anchor.go is zero-diff and
// therefore cannot grow an observer parameter. T291 pins the two implementations
// together byte-for-byte so they cannot drift.
func (s *HistoryExportScheduler) dispatchAnchorPathObserved(ctx context.Context, path string, e anchorEntry, observer compactionObserver) error {
	dg, derr := anchorDigestOf(&e)
	if derr != nil {
		return derr
	}
	req := anchorRequest{
		V:                  1,
		KeyID:              e.KeyID,
		StreamID:           e.StreamID,
		PublicationID:      e.PublicationID,
		AnchorSeq:          e.AnchorSeq,
		AnchorDigest:       dg,
		ManifestDigest:     e.ManifestDigest,
		PrevPublicationID:  e.PrevPublicationID,
		PrevManifestDigest: e.PrevManifestDigest,
		RecordedAt:         e.RecordedAt,
		Sig:                e.Sig,
	}
	// TEST-ONLY hook (R40-6), same as the frozen path: it fires at the moment of
	// dispatch, so a test can assert the pending line is already durable.
	if s.beforeAnchorDispatch != nil {
		s.beforeAnchorDispatch(s.cfg.Dir, e.AnchorSeq)
	}
	status, body, terr := s.anchorTransport.deliver(ctx, req)
	res := classifyAnchorResponse(status, body, terr)

	if res.Conflict {
		// A4/A5: record NOTHING. The entry stays `pending` and the problem is
		// surfaced — a conflict is never resolved by overwriting.
		return fmt.Errorf("anchor: witness reported a conflict for publication %d (seq %d)", e.PublicationID, e.AnchorSeq)
	}

	next := e
	next.Attempts = e.Attempts + 1
	switch res.State {
	case anchorStateAnchored:
		next.State = anchorStateAnchored
		next.AckID = res.AckID
		next.AnchoredAt = s.clock().UTC().Format(time.RFC3339Nano)
	case anchorStateUnanchored:
		next.State = anchorStateUnanchored
		next.LastError = res.Detail
	default: // pending — retryable
		next.State = anchorStatePending
		next.LastError = res.Detail
		if next.Attempts >= s.anchorMaxAttempts() {
			next.State = anchorStateUnanchored
			next.LastError = fmt.Sprintf("attempts exhausted (%d): %s", next.Attempts, res.Detail)
		}
	}
	// The payload is unchanged, so the original signature still covers it: a
	// state advance needs no re-signing (ADR-053 §1.3). The ONLY difference from
	// the frozen function is that the compaction this append may trigger is
	// observed by the family's hook.
	if aerr := appendAnchorEntryPathObserved(path, s.cfg.AnchorCapacity, next, observer); aerr != nil {
		return aerr
	}
	if next.State != anchorStateAnchored {
		return fmt.Errorf("anchor: seq %d is %s (attempt %d): %s", next.AnchorSeq, next.State, next.Attempts, next.LastError)
	}
	return nil
}

// sweepAnchorDelivery is the Phase 47 recovery sweep (ADR-070 §3). It runs at
// the scheduler tick tail, BETWEEN dispatchAcceptancePending and
// drainDestructionAnchorQueue (I10) — i.e. BEFORE the tick's in-tick producers,
// so a destruction seq created later in the same tick is never re-dispatched
// here (T295).
//
// It carries NO scheduler state: every failure goes to s.logger, and the read
// face re-derives delivery status from the anchor logs on each call. Hence there
// is no "sticky error poisons converged" problem (M6).
func (s *HistoryExportScheduler) sweepAnchorDelivery(ctx context.Context) {
	if !s.anchorEnabled() {
		return
	}
	for _, st := range deliverySweepStreams() {
		// I8/L1: one dispatcher per stream at any instant. The lock is held
		// across load AND dispatch, so a concurrent producer cannot allocate a
		// seq or append against a state this sweep already decided to advance.
		st.DispatchMu.Lock()
		path := st.AnchorPath(s.cfg.Dir)
		state, err := loadAnchorStatePath(path, s.cfg.Dir, s.trust)
		if err != nil {
			// I3①: an unclassifiable line fails the whole load. The stream is
			// left byte-identical; the failure is loud but only in the log.
			s.warnAnchorDelivery(st.Family, "anchor log holds unclassifiable line(s); the stream is not swept", err)
			st.DispatchMu.Unlock()
			continue
		}
		if len(state.conflicts) > 0 {
			// I3②: loadAnchorStatePath does NOT return an error for a conflict —
			// it silently drops the seq from `latest`. Without this explicit
			// check the dropped seq would be presented as converged (M1/M5).
			s.warnAnchorDelivery(st.Family,
				fmt.Sprintf("anchor log holds %d conflicting anchor_seq(s) %v; the stream is not swept", len(state.conflicts), state.conflicts), nil)
			st.DispatchMu.Unlock()
			continue
		}
		var observer compactionObserver
		if st.Observer != nil {
			observer = st.Observer(s)
		}
		for _, seq := range state.seqs() {
			e := state.latest[seq]
			if e.State != anchorStatePending {
				// I2: `unanchored` is terminal and never revived; `anchored` is
				// settled. Only a pending obligation is swept.
				continue
			}
			if e.Attempts >= s.anchorMaxAttempts() {
				// The contradictory state (pending ∧ attempts >= max) is
				// terminalised exactly as anchorHousekeeping does — never left
				// pending (T287).
				final := e
				final.State = anchorStateUnanchored
				final.LastError = fmt.Sprintf("attempts exhausted (%d)", e.Attempts)
				if aerr := appendAnchorEntryPathObserved(path, s.cfg.AnchorCapacity, final, observer); aerr != nil {
					s.warnAnchorDelivery(st.Family, fmt.Sprintf("terminalising exhausted seq %d failed", e.AnchorSeq), aerr)
				}
				continue
			}
			// M6: the dispatch RESULT never feeds any read/judgement field. A
			// non-anchored terminal state (including the legal `unanchored`)
			// makes dispatchAnchorPathObserved return an error; it is logged and
			// otherwise ignored.
			if derr := s.dispatchAnchorPathObserved(ctx, path, e, observer); derr != nil {
				s.warnAnchorDelivery(st.Family, fmt.Sprintf("seq %d did not confirm", e.AnchorSeq), derr)
			}
		}
		st.DispatchMu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Read face (A5: strictly read-only, zero side effects)
// ---------------------------------------------------------------------------

// anchorDeliveryStreamStatus is one family's row on the delivery face (A1).
// Every field is DERIVED at read time; none is cached scheduler state.
type anchorDeliveryStreamStatus struct {
	Family           string  `json:"family"`
	Present          bool    `json:"present"`
	SweptBy          string  `json:"swept_by"`
	Anchored         int     `json:"anchored"`
	Unanchored       int     `json:"unanchored"`
	Pending          int     `json:"pending"`
	PendingRetryable int     `json:"pending_retryable"`
	PendingExhausted int     `json:"pending_exhausted"`
	Conflicts        []int64 `json:"conflicts,omitempty"`
	// OldestPendingRecordedAt is the min RecordedAt over pending entries. It is
	// a LOWER BOUND on backlog age, not the moment an entry entered pending:
	// RecordedAt is the entry's FIRST recording time and survives state advances
	// (next := e). A timestamp that cannot be parsed is omitted, never guessed.
	OldestPendingRecordedAt string `json:"oldest_pending_recorded_at,omitempty"`
	// LastUnanchoredReason makes "why was delivery abandoned" visible without
	// introducing a sticky error field: it is read from the highest-seq
	// `unanchored` entry's own LastError.
	LastUnanchoredReason string `json:"last_unanchored_reason,omitempty"`
	// Converged is pending == 0 ∧ no conflicts ∧ no STRUCTURAL error. A dispatch
	// failure — including the legal terminal `unanchored` — does NOT participate
	// (I6/M6).
	Converged bool `json:"converged"`
	// Error carries STRUCTURAL state ONLY: a load failure or conflicting seqs.
	Error string `json:"error,omitempty"`
}

// anchorDeliveryStatusSummary is the `anchor_delivery` group (A1).
type anchorDeliveryStatusSummary struct {
	Converged bool                         `json:"converged"`
	Families  []anchorDeliveryStreamStatus `json:"families"`
}

// AnchorDeliveryStatus derives the delivery face. It is strictly read-only: it
// stats five files and runs the existing anchor load, and it never dispatches,
// writes, audits or opens a network connection (A5/T290).
//
// It is called from Status() while holding s.mu, in the same position as the
// other family summaries.
func (s *HistoryExportScheduler) AnchorDeliveryStatus() anchorDeliveryStatusSummary {
	out := anchorDeliveryStatusSummary{Families: []anchorDeliveryStreamStatus{}, Converged: true}
	for _, st := range anchorDeliveryStreams() {
		row := anchorDeliveryStreamStatus{Family: st.Family, SweptBy: st.SweptBy}
		path := st.AnchorPath(s.cfg.Dir)
		row.Present = witnessFilePresent(path)
		state, err := loadAnchorStatePath(path, s.cfg.Dir, s.trust)
		if err != nil {
			// Structural: the stream cannot be evaluated, so it is not converged.
			row.Error = err.Error()
			out.Families = append(out.Families, row)
			out.Converged = false
			continue
		}
		row.Conflicts = state.conflicts
		var oldestT time.Time
		for _, seq := range state.seqs() {
			e := state.latest[seq]
			switch e.State {
			case anchorStateAnchored:
				row.Anchored++
			case anchorStateUnanchored:
				row.Unanchored++
				// seqs() is ascending, so the final assignment is the highest seq.
				row.LastUnanchoredReason = e.LastError
			case anchorStatePending:
				row.Pending++
				if e.Attempts >= s.anchorMaxAttempts() {
					// A self-contradictory pending state (dispatchAnchorPath
					// terminalises at the cap) is called out SEPARATELY, never
					// folded into pending (A8-②).
					row.PendingExhausted++
				} else {
					row.PendingRetryable++
				}
				if t, perr := time.Parse(time.RFC3339Nano, e.RecordedAt); perr == nil {
					if row.OldestPendingRecordedAt == "" || t.Before(oldestT) {
						oldestT = t
						row.OldestPendingRecordedAt = e.RecordedAt
					}
				}
			}
		}
		if len(row.Conflicts) > 0 {
			// The load succeeded but the stream is self-contradictory: it is not
			// swept (I3②) and must never read as converged.
			row.Error = fmt.Sprintf("anchor log holds %d conflicting anchor_seq(s) %v; the stream is not swept", len(row.Conflicts), row.Conflicts)
		}
		row.Converged = row.Pending == 0 && len(row.Conflicts) == 0 && row.Error == ""
		if !row.Converged {
			out.Converged = false
		}
		out.Families = append(out.Families, row)
	}
	return out
}
