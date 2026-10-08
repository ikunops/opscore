package server

// Phase 48 — Runtime Decision Attestation (ADR-071 scope, ADR-072 architecture).
//
// P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45
// what-accepted / P46 whether-witnessed / P47 whether-delivered — every one of
// them is about an EVIDENCE ARTIFACT and its carrier. None of them asked whether
// the DECISION that produced those artifacts was itself truthfully recorded.
//
// The gap this file closes (ADR-071 §2, all line-checked there):
//
//   - the Gate emits exactly one provenance record per Check (gate.go:106-220),
//     and the ONLY configured sink is a bounded in-memory ring
//     (cmd/opscore/main.go:285, provenance.go:94-112) — no persistence at all,
//     so a restart empties the decision log;
//   - the durable export face covers alert-TRANSITION edges, a DIFFERENT dataset
//     (mgmt_obs.go:529-532);
//   - the audit face records rejections only, and carries no digest, no chain
//     and no signature (ADR-071 §2 fact 6).
//
// So the decision itself sits on NO evidence chain. This Phase puts it on one,
// as the SIXTH witness family `protection_decision`, built from two halves whose
// detection powers are declared separately (ADR-071 §3):
//
//	(a) the P46/P47 reuse half — registering into witnessFamilyRegistry and
//	    anchorDeliveryStreams buys the absence matrix (both files gone ⇒
//	    family_ledger_deleted), the anchor-side window, and the delivery face;
//	(b) the NEW local-recompute half — the decision log is a HASH CHAIN, the
//	    anchor entry carries the chain head digest, and the read face REWALKS the
//	    chain and compares heads. P46 is blind to the main ledger's CONTENT
//	    (its LedgerPath is an existence probe only: snapshot_witness_reconcile.go
//	    :175/:384, and :244-282 compares the local ANCHOR digest), so a rewritten
//	    or truncated decision log reads `family_intact` there. The local
//	    recompute is the non-vacuous source of `decision_attested` (ADR-072 §3.3).
//
// Non-negotiables carried in from the frozen faces:
//
//   - `internal/protection` and `snapshot_anchor.go` are ZERO DIFF (A2/A1). The
//     Gate's emitDecision call sites, the seven-guard order and the decision
//     vocabulary are untouched: the new sink only IMPLEMENTS
//     protection.ProvenanceSink and is injected at the assembly root.
//   - R24-4 verbatim (I2): Emit NEVER blocks, NEVER backpressures and NEVER
//     changes a decision. It enqueues (bounded, microseconds, no I/O) and
//     evicts the oldest record on overflow, counting the loss.
//   - I5: in the ProvenanceStore role the new sink DELEGATES to the existing
//     ring verbatim, so gate.ProvenanceStore() returns the same ring and the
//     P24.2 read faces (/decisions, /decisions/export) stay byte-identical.
//   - I6: two kinds of "fewer records" are strictly separated. LEGAL PREFIX
//     COMPACTION (compactLogPrefixGroupsObserved + decisionGroupOf) drops whole
//     oldest groups, leaves the chain head UNCHANGED and is NOT counted as loss;
//     TAIL DELETION / TAMPERING moves the head off the anchored head and is
//     reported as truncated / divergent. The discriminator is exactly "is the
//     head still on the anchored head".
//   - I9: fail-closed. An unclassifiable decision-log line or a conflicting
//     anchor seq means the family is NOT swept at all (byte-zero change) with a
//     loud diagnostic; the other five families are untouched.
//   - I11: no retro-attestation. A decision taken before the family was enabled
//     is NOT assertable, and is NEVER reconstructed from audit rows.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Frozen constants (ADR-072 A4). The decision log's bounds are NOT configurable:
// a per-deployment knob would make "the chain head is anchored" mean a different
// amount of retained evidence in different places, and the criterion would stop
// being one criterion. Bounds are whole-GROUP counts (one `seq` is one group).
// ---------------------------------------------------------------------------
const (
	// decisionLogFile is the SIXTH family's MAIN ledger — the hash chain of
	// decision records. Same directory as every other family ledger (R41-3).
	decisionLogFile = "protection-decision.jsonl"
	// decisionAnchorFile is the sixth family's OWN anchor stream. Its own file,
	// its own anchor_seq space, for the reason every earlier family got one: the
	// shared anchor classifier keys on `anchor_seq` alone, so one file holding two
	// families would let a compaction drop a group across them (ADR-055 §9).
	decisionAnchorFile = "decision-anchor.jsonl"
	// decisionLossFile is the honest-loss counter (ADR-071 A5). It is NOT an
	// evidence file: it exists so a loss can never be laundered into "nothing
	// happened" by a restart (ProvenanceStats is process-local, provenance.go
	// :129-138, and resets to zero).
	decisionLossFile = "protection-decision-loss.json"
)

// The two retention caps are VARIABLES, not constants, for one reason: the tests
// that pin the OVERFLOW and the PREFIX-COMPACTION disciplines must push more
// records than the cap, and every drained record costs an fsync'd append
// (appendLogLine: open, write, Sync, close). With the production caps that cost a
// minute of disk latency per test on this host (measured: T303 60s, T304 53s,
// T315 37s). The cap's SIZE is incidental to those disciplines, so a test shrinks
// it for its own duration and restores it; production never writes them.
var (
	// decisionQueueCap bounds the in-flight queue. Larger than the log's
	// retention on purpose: the queue is a microsecond-scale hand-off buffer, the
	// log is the retention boundary.
	decisionQueueCap = 2048
	// decisionLogCapacity bounds the decision log by whole `seq` groups, exactly
	// like every other main ledger.
	decisionLogCapacity = 1024
)

// anchorKindProtectionDecision is the sixth anchor family's kind. It is defined
// HERE and not in snapshot_anchor.go, because that file is zero-diff (ADR-072
// §3.2): the anchorKind* block (snapshot_anchor.go:65-78) is frozen.
const anchorKindProtectionDecision = "protection_decision"

// decision_log_state values (ADR-072 §4). `no_anchor` is "the family has no
// anchor entry yet" (before the first tick); `not_enabled` is "the producer gate
// is off", never a claim about history (ADR-071 A7-⑧).
const (
	decisionLogStateAttested      = "attested"
	decisionLogStateDivergent     = "divergent"
	decisionLogStateTruncated     = "truncated"
	decisionLogStateNoAnchor      = "no_anchor"
	decisionLogStatePendingAnchor = "pending_anchor"
	decisionLogStateNotEnabled    = "not_enabled"
)

// witnessFamilyProtectionDecision is the sixth registered family name. It lives
// here so the registry, the enable switch and the partition table all point at
// one constant (ADR-072 §2).
const witnessFamilyProtectionDecision = "protection_decision"

// ---------------------------------------------------------------------------
// The record and its chain (ADR-072 §3.1)
// ---------------------------------------------------------------------------

// decisionRecord is one line of the decision log.
//
// The chain fields are the ONLY fields outside the digest: `digest` is the
// SHA-256 of the canonical payload (everything else, `seq` included — binding
// the seq means a record cannot be silently renumbered), and `prev_digest` is
// the previous record's digest. The payload fields are taken VERBATIM from
// protection.DecisionProvenance (provenance.go:20-50); no field is added and
// none is invented, so the R24-7 secret boundary (hash + advisory refs only)
// carries over unchanged (T313).
type decisionRecord struct {
	Seq        int64  `json:"seq"`
	PrevDigest string `json:"prev_digest"`
	Digest     string `json:"digest"`

	At            string `json:"at"`
	TraceID       string `json:"trace_id"`
	CapabilityID  string `json:"capability_id"`
	PrincipalHash string `json:"principal_hash"`
	Guard         string `json:"guard"`
	Decision      string `json:"decision"`
	Action        string `json:"action"`
	Threshold     string `json:"threshold"`
	Observed      string `json:"observed"`
	Detail        string `json:"detail"`
	LatencyMicros int64  `json:"latency_micros"`
}

// decisionSigned is the exact structure decisionRecordDigest serializes: the
// payload region, in a fixed field order.
type decisionSigned struct {
	Seq           int64  `json:"seq"`
	At            string `json:"at"`
	TraceID       string `json:"trace_id"`
	CapabilityID  string `json:"capability_id"`
	PrincipalHash string `json:"principal_hash"`
	Guard         string `json:"guard"`
	Decision      string `json:"decision"`
	Action        string `json:"action"`
	Threshold     string `json:"threshold"`
	Observed      string `json:"observed"`
	Detail        string `json:"detail"`
	LatencyMicros int64  `json:"latency_micros"`
}

func decisionSignedFields(r *decisionRecord) decisionSigned {
	return decisionSigned{
		Seq:           r.Seq,
		At:            r.At,
		TraceID:       r.TraceID,
		CapabilityID:  r.CapabilityID,
		PrincipalHash: r.PrincipalHash,
		Guard:         r.Guard,
		Decision:      r.Decision,
		Action:        r.Action,
		Threshold:     r.Threshold,
		Observed:      r.Observed,
		Detail:        r.Detail,
		LatencyMicros: r.LatencyMicros,
	}
}

// decisionRecordDigest is the chain value: sha256(canonical payload).
func decisionRecordDigest(r *decisionRecord) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil decision record")
	}
	payload, err := json.Marshal(decisionSignedFields(r))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// decisionGroupOf is the classifier this log injects into the shared append-only
// primitive: one `seq` is ONE group. A line that is not JSON, or whose seq is not
// positive, is UNCLASSIFIABLE — and an unclassifiable line fails every operation
// that would rewrite the file (appendonly_log.go:140, R40-4), which is what
// makes the fail-closed path in the drain real rather than decorative.
func decisionGroupOf(raw []byte) (int64, bool) {
	var probe struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, false
	}
	if probe.Seq <= 0 {
		return 0, false
	}
	return probe.Seq, true
}

func decisionLogPath(dir string) string    { return filepath.Join(dir, decisionLogFile) }
func decisionAnchorPath(dir string) string { return filepath.Join(dir, decisionAnchorFile) }
func decisionLossPath(dir string) string   { return filepath.Join(dir, decisionLossFile) }

// walkDecisionChain reads the decision log and REWALKS the chain (ADR-072
// §3.3). It returns the head (highest surviving seq and its digest) and whether
// the chain is internally sound.
//
// Soundness is checked on two levels, so neither a field edit nor a
// recomputed-digest edit survives:
//
//  1. every record's stored `digest` must equal the digest RECOMPUTED from its
//     payload — this catches an in-place field edit even if the attacker leaves
//     the digest alone;
//  2. every record after the first must point at its predecessor
//     (`prev_digest == prev.digest`) and continue the sequence — this catches a
//     recomputed digest, a deleted middle record and a reordered file.
//
// The FIRST surviving record's `prev_digest` is deliberately NOT checked against
// anything: after a legal prefix compaction it points at a record that is gone,
// and refusing that would make compaction indistinguishable from tampering in
// the wrong direction. That is the pivot of I6 — a shorter PREFIX is legal, a
// shorter HEAD is not.
//
// A missing file is an empty chain, not corruption (absence is not tampering;
// deleting the log entirely is P46's `family_ledger_deleted`, not this face's
// verdict). An unclassifiable or unparseable line is fail-closed: ok=false.
func walkDecisionChain(path string) (headSeq int64, headDigest string, ok bool) {
	lines, allClassified, err := readLogLines(path, decisionGroupOf)
	if err != nil {
		return 0, "", false
	}
	if !allClassified {
		return 0, "", false
	}
	var prev *decisionRecord
	for i := range lines {
		var r decisionRecord
		if jerr := json.Unmarshal(lines[i].raw, &r); jerr != nil {
			// Unreachable: the classifier already parsed the line.
			return 0, "", false
		}
		want, derr := decisionRecordDigest(&r)
		if derr != nil || want != r.Digest {
			return 0, "", false
		}
		if prev != nil {
			if r.PrevDigest != prev.Digest {
				return 0, "", false
			}
			if r.Seq != prev.Seq+1 {
				return 0, "", false
			}
		}
		cp := r
		prev = &cp
	}
	if prev == nil {
		return 0, "", true
	}
	return prev.Seq, prev.Digest, true
}

// ---------------------------------------------------------------------------
// The honest-loss counter (ADR-071 A5)
// ---------------------------------------------------------------------------

type decisionLossState struct {
	Dropped int64 `json:"dropped"`
}

// readDecisionLoss returns the persisted loss count. A missing or unreadable
// counter reads 0 — the counter is a completeness signal, and refusing to answer
// would be worse than reporting the last durable value.
func readDecisionLoss(dir string) (int64, error) {
	data, err := os.ReadFile(decisionLossPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // never written: zero losses is the honest genesis
		}
		return 0, err
	}
	var st decisionLossState
	if jerr := json.Unmarshal(data, &st); jerr != nil {
		// Final review F3: a corrupted loss counter must NOT read as zero —
		// zeroing it would launder real drops and re-light decision_attested.
		// Fail-closed: the caller loses the attested claim until the operator
		// intervenes (the file is deliberately unchained/unanchored — the one
		// artifact without integrity discipline — so fail-closed is the only
		// defense it has).
		return 0, fmt.Errorf("decision loss counter is unreadable: %w", jerr)
	}
	if st.Dropped < 0 {
		return 0, fmt.Errorf("decision loss counter is negative (%d)", st.Dropped)
	}
	return st.Dropped, nil
}

func writeDecisionLoss(dir string, n int64) error {
	data, err := json.Marshal(decisionLossState{Dropped: n})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := decisionLossPath(dir)
	tmp := path + ".tmp"
	f, oerr := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if oerr != nil {
		return oerr
	}
	if _, werr := f.Write(data); werr != nil {
		f.Close()
		os.Remove(tmp)
		return werr
	}
	if serr := f.Sync(); serr != nil {
		f.Close()
		os.Remove(tmp)
		return serr
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		os.Remove(tmp)
		return rerr
	}
	return nil
}

// ---------------------------------------------------------------------------
// The non-blocking sink (ADR-072 §3.4; R24-4 verbatim)
// ---------------------------------------------------------------------------

// DecisionAttestSink is the Phase 48 provenance sink. It is a DELEGATING
// decorator: in the ProvenanceSink role it records into the existing ring AND
// enqueues for the durable chain; in the ProvenanceStore role it forwards
// verbatim to the ring (I5), so `gate.ProvenanceStore()` keeps returning the
// same ring and the P24.2 read faces are byte-identical (T298).
//
// Emit is non-blocking BY CONSTRUCTION (I2/R24-4): one delegated ring write (the
// pre-existing behaviour, unchanged) plus one bounded-mutex append. No I/O, no
// waiting, no backpressure — a full queue evicts the oldest record and counts
// the loss instead of delaying a decision. The persistence of that count happens
// on the scheduler tick, never here.
type DecisionAttestSink struct {
	ring *protection.RecordingProvenanceSink
	dir  string

	mu        sync.Mutex
	queue     []protection.DecisionProvenance
	dropped   int64
	persisted int64
	// lossErr is sticky (final review F3): set when the persisted loss counter
	// is unreadable/corrupt. Non-nil ⇒ the attested claim is withheld (the
	// counter must not read as zero — that would launder real drops).
	lossErr string
}

// NewDecisionAttestSink builds the sink over an existing ring and the export
// directory the sixth family's files live in. The persisted loss count is loaded
// HERE, so a restarted process continues the count instead of laundering the
// previous process's losses into zero (T304).
func NewDecisionAttestSink(ring *protection.RecordingProvenanceSink, dir string) *DecisionAttestSink {
	// F3: a corrupt/unreadable loss counter must not read as zero. The sink
	// carries the sticky error; the first fillDecisionAttest surfaces it and
	// withholds the attested claim until an operator resolves it.
	dropped, lossErr := readDecisionLoss(dir)
	sinkErr := ""
	if lossErr != nil {
		sinkErr = lossErr.Error()
	}
	return &DecisionAttestSink{
		ring:      ring,
		dir:       dir,
		queue:     make([]protection.DecisionProvenance, 0, decisionQueueCap),
		dropped:   dropped,
		persisted: dropped,
		lossErr:   sinkErr,
	}
}

// Emit records one decision (protection.ProvenanceSink, provenance.go:55-57).
//
// R24-4, verbatim: the ring write is preserved EXACTLY as it was before this
// Phase, the enqueue is O(1) under a bounded mutex, and a full queue evicts the
// oldest record while incrementing the loss count. Nothing here can delay or
// alter the Gate's decision.
func (s *DecisionAttestSink) Emit(ctx context.Context, p protection.DecisionProvenance) {
	if s == nil {
		return
	}
	// ① the pre-existing ring behaviour, untouched.
	s.ring.Emit(ctx, p)
	// ② the durable hand-off, microseconds, no I/O.
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) >= decisionQueueCap {
		s.queue = s.queue[1:]
		s.dropped++
	}
	s.queue = append(s.queue, p)
}

// ---- ProvenanceStore role: verbatim delegation (I5) ----

// Recent delegates to the ring (R24-5: the projection is the ring's, not ours).
func (s *DecisionAttestSink) Recent(n int) []protection.DecisionProvenance {
	return s.ring.Recent(n)
}

// ByTraceID delegates to the ring.
func (s *DecisionAttestSink) ByTraceID(id string) []protection.DecisionProvenance {
	return s.ring.ByTraceID(id)
}

// ByCapability delegates to the ring.
func (s *DecisionAttestSink) ByCapability(cap string) []protection.DecisionProvenance {
	return s.ring.ByCapability(cap)
}

// Stats delegates to the ring. The RING's dropped/truncated are the ring's own
// bounded-loss signals and are reported unchanged; this sink's own losses are a
// DIFFERENT dataset (never persisted vs. evicted from a bounded buffer) and are
// surfaced as decision_queue_dropped on the sixth family's row instead.
func (s *DecisionAttestSink) Stats() protection.ProvenanceStats {
	return s.ring.Stats()
}

// ---- internals shared with the scheduler tick ----

// Dropped returns the honest loss count: persisted value plus this process's own
// evictions.
func (s *DecisionAttestSink) Dropped() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// queueEmpty reports whether there is nothing to drain. It lets the drain peek
// WITHOUT consuming, so a fail-closed refusal never destroys a decision that
// could still be recorded once the log is repaired (I9).
func (s *DecisionAttestSink) queueEmpty() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) == 0
}

// takeQueue removes and returns everything queued. The drained buffer is
// re-created rather than reused so a caller holding the slice cannot observe a
// concurrent append.
func (s *DecisionAttestSink) takeQueue() []protection.DecisionProvenance {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	out := s.queue
	s.queue = make([]protection.DecisionProvenance, 0, decisionQueueCap)
	return out
}

// addDropped counts records that were taken but never persisted. It is the same
// honesty rule as the queue eviction: a decision that reached no durable byte is
// a LOSS, and `decision_attested` must be false for as long as it is counted.
func (s *DecisionAttestSink) addDropped(n int64) {
	if s == nil || n <= 0 {
		return
	}
	s.mu.Lock()
	s.dropped += n
	s.mu.Unlock()
}

// persistLoss writes the loss count through to disk, but only when it changed —
// so an idle tick writes nothing. The persisted count is CROSS-PROCESS (T304).
func (s *DecisionAttestSink) persistLoss() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	n := s.dropped
	changed := n != s.persisted
	s.mu.Unlock()
	if !changed {
		return nil
	}
	if err := writeDecisionLoss(s.dir, n); err != nil {
		return err
	}
	s.mu.Lock()
	s.persisted = n
	s.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Scheduler wiring
// ---------------------------------------------------------------------------

// decisionAttestConfig is the sixth family's bundle. Like every other family's
// config it is rebuilt on use and caches nothing.
type decisionAttestConfig struct {
	on   bool
	sink *DecisionAttestSink
	dir  string
}

// enabled is the family's producer gate (ADR-072 §2(b)). The gate is the
// SINK's presence: without a sink the Gate emits nothing durable, so the family
// has no producer and the GET probe must say `not_enabled` rather than implying
// a history that was never written (D7 / T308).
func (c decisionAttestConfig) enabled() bool { return c.on && c.sink != nil }

func (s *HistoryExportScheduler) decisionAttestConfig() decisionAttestConfig {
	if s == nil {
		return decisionAttestConfig{}
	}
	return decisionAttestConfig{
		on:   s.cfg.DecisionAttest != nil,
		sink: s.cfg.DecisionAttest,
		dir:  s.cfg.Dir,
	}
}

// warnDecisionAttest records one diagnostic. Like the P47 sweep it writes
// NOTHING to the pre-existing error faces: a decision-recording failure is not a
// P40/P43/P45 judgement, and folding it into one would poison that verdict.
func (s *HistoryExportScheduler) warnDecisionAttest(msg string, err error) {
	if s == nil || s.logger == nil {
		return
	}
	if err != nil {
		s.logger.Warn("decision attestation: "+msg, "err", err)
		return
	}
	s.logger.Warn("decision attestation: " + msg)
}

// decisionDispatchMu serializes the sixth anchor stream's dispatchers (I10).
// The stream has TWO: the tick drain (below) and the P47 sweep
// (sweepAnchorDelivery). Without a shared mutex two dispatchers could derive the
// same anchor_seq and fork the stream — the same argument P47 I8 makes for the
// three streams it added.
//
// Lock order: decisionDispatchMu is an L1-class lock (it may compact the sixth
// anchor log, whose observer is nil, so it never reaches L2/L3/L4). Nothing
// takes it while holding s.mu.
var decisionDispatchMu sync.Mutex

// drainDecisionAttest is the sixth family's tick tail (ADR-072 §3.4). It is the
// ONLY producer of decision records and of the family's anchor entries.
//
// Order per tick: read the chain head → append the queued records as chained
// lines → write ONE anchor entry carrying the new head → bound the log by whole
// prefix groups. Anchoring is per TICK, never per record (ADR-071 A8-②): the
// decision source is high-frequency (one record per Check), so per-record
// signing/anchoring is not viable, and the assertable granularity is honestly
// "this decision falls inside an anchored window and the chain after it is
// unchanged".
//
// The P47 sweep runs BEFORE this (frozen tick order), so an entry created here
// is never re-dispatched in the same tick.
func (s *HistoryExportScheduler) drainDecisionAttest(ctx context.Context) {
	c := s.decisionAttestConfig()
	if !c.enabled() {
		return
	}
	path := decisionLogPath(s.cfg.Dir)
	if !c.sink.queueEmpty() {
		headSeq, headDigest, chainOK := walkDecisionChain(path)
		if !chainOK {
			// I9 fail-closed: an unreadable line means we cannot know which seqs
			// are taken, so NOTHING is appended and the file is left byte-identical.
			// The queued records are deliberately NOT consumed — they are retried
			// once the log is repaired, so a refusal loses nothing.
			s.warnDecisionAttest("the decision log holds unclassifiable or broken line(s); the family is not swept this tick (byte-zero change)", nil)
		} else {
			recs := c.sink.takeQueue()
			written := 0
			var werr error
			for _, p := range recs {
				seq := headSeq + 1
				r := decisionRecord{
					Seq:           seq,
					PrevDigest:    headDigest,
					At:            p.At.UTC().Format(time.RFC3339Nano),
					TraceID:       p.TraceID,
					CapabilityID:  p.CapabilityID,
					PrincipalHash: p.PrincipalHash,
					Guard:         p.Guard,
					Decision:      p.Decision,
					Action:        p.Action,
					Threshold:     p.Threshold,
					Observed:      p.Observed,
					Detail:        p.Detail,
					LatencyMicros: p.LatencyMicros,
				}
				dg, derr := decisionRecordDigest(&r)
				if derr != nil {
					werr = derr
					break
				}
				r.Digest = dg
				raw, merr := json.Marshal(&r)
				if merr != nil {
					werr = merr
					break
				}
				if aerr := appendLogLine(path, raw); aerr != nil {
					werr = aerr
					break
				}
				headSeq, headDigest = seq, dg
				written++
			}
			if werr != nil {
				// A record that reached no durable byte is a LOSS, not silence.
				c.sink.addDropped(int64(len(recs) - written))
				s.warnDecisionAttest("the decision log could not be extended; the unwritten records are counted as loss", werr)
			}
			if written > 0 {
				// The anchor entry carries the head at anchor time (§3.2). The
				// dispatch mutex is the sixth stream's I10 lock.
				decisionDispatchMu.Lock()
				aerr := s.anchorDecisionChain(headSeq, headDigest)
				decisionDispatchMu.Unlock()
				if aerr != nil {
					// I6: the anchor is an EXIT. A failed dispatch never rewrites
					// the recorded fact; P47's sweep retries it.
					s.warnDecisionAttest("the decision chain head could not be anchored", aerr)
				}
			}
			// I6 bounded retention: whole oldest GROUPS only, chain head unchanged.
			// The observer is deliberately nil (see the note below the drain).
			if cerr := compactLogPrefixGroupsObserved(path, decisionLogCapacity, decisionGroupOf, nil); cerr != nil {
				s.warnDecisionAttest("the decision log's prefix compaction was refused", cerr)
			}
		}
	}
	if perr := c.sink.persistLoss(); perr != nil {
		s.warnDecisionAttest("the honest-loss counter could not be persisted", perr)
	}
}

// NOTE (I6, ADR-072 §2(c)/§3.4): the decision log's prefix compaction installs a
// NIL observer, and that is a declared decision, not an omission. The five main
// ledgers account a dropped prefix by writing a destruction record; the decision
// log must not, for two reasons that both come from the frozen faces:
//
//   - a destruction record would have to be a new destruction KIND, and the five
//     family implementations (snapshot_destruction.go among them) are zero-diff
//     in this Phase (ADR-072 §1), so the kind could not be classified;
//   - more importantly, the accounting would be WRONG. A prefix compaction here
//     drops the oldest records of a hash chain whose HEAD is unchanged and which
//     has already been covered by an anchor entry: it is bounded RETENTION, not a
//     loss of evidence. Writing a destruction for it would conflate the two kinds
//     of "fewer records" that I6 exists to keep apart — and would let a legal
//     compaction read as a disappearance.
//
// The family's hook therefore exists as this call site's nil observer, and the
// discriminator lives where it belongs: in the chain head (T315).

// anchorDecisionChain writes ONE anchor entry for the current chain head and
// dispatches it. It is the §3.2 carrier decision, forced by the frozen
// snapshot_anchor.go: anchorEntry/anchorSigned have no general-purpose payload
// digest slot, only per-family ones, so the sixth family REUSES two existing
// slots:
//
//	Kind           = "protection_decision"   (a new constant, defined here)
//	PublicationID  = the chain head seq      (non-omitempty; historically 0 for
//	                                          non-publication families, so this
//	                                          overload changes no existing byte)
//	ManifestDigest = the chain head digest   (non-omitempty; historically "" for
//	                                          non-publication families — see
//	                                          anchorDestructionEntry/
//	                                          anchorAcceptanceEntry/
//	                                          anchorKeyLifecycleEvent, none of
//	                                          which set it)
//
// ManifestDigest is inside the signature coverage (anchorSignedFields copies it),
// so the head digest is covered by the Phase 37 signature and travels to the
// witness. The SEMANTIC OVERLOAD is declared in ADR-071 A8-⑨: for the
// publication family this field is a manifest digest; for the sixth family it is
// a decision-chain head digest. The field NAME is not changed — changing it
// would break the frozen file and every existing family's signature payload.
//
// identityID() (snapshot_anchor.go:208-217, frozen) has no case for the new kind
// and therefore falls back to PublicationID = the head seq (> 0), so
// st.byID[identityID()] is keyed meaningfully and no 0-fallback occurs (T302).
func (s *HistoryExportScheduler) anchorDecisionChain(headSeq int64, headDigest string) error {
	if !s.anchorEnabled() || s.signer == nil {
		return nil
	}
	path := decisionAnchorPath(s.cfg.Dir)
	seq, err := nextAnchorSeqPath(path)
	if err != nil {
		return err
	}
	ae := anchorEntry{
		AnchorSeq:      seq,
		Kind:           anchorKindProtectionDecision,
		PublicationID:  headSeq,
		ManifestDigest: headDigest,
		RecordedAt:     s.clock().UTC().Format(time.RFC3339Nano),
		State:          anchorStatePending,
	}
	if serr := s.signer.signAnchorEntry(&ae, s.cfg.Dir, s.clock()); serr != nil {
		return serr
	}
	// The sixth anchor stream is the accountability system's own bookkeeping, so
	// its compaction is unobserved (nil) — the same structural termination rule
	// as the destruction/acceptance anchor streams (ADR-070 §2, R43-8).
	if aerr := appendAnchorEntryPathObserved(path, s.cfg.AnchorCapacity, ae, nil); aerr != nil {
		return aerr
	}
	return s.dispatchAnchorPath(context.Background(), path, ae)
}

// ---------------------------------------------------------------------------
// Read face (ADR-072 §4 — strictly read-only, zero side effects)
// ---------------------------------------------------------------------------

// decisionLogStateAt derives the family's local-recompute verdict (ADR-072
// §3.3). `st` is the sixth anchor stream's already-loaded state, so the read face
// walks the anchor log once.
//
// This is the NEW mechanism (ADR-071 §2 M1): P46 compares the local ANCHOR
// entry's digest against the witness projection and never looks at the main
// ledger's content, so a rewritten or truncated decision log reads
// `family_intact` there. The three comparisons below are what makes
// `decision_attested` non-vacuous (T299/T300).
func (s *HistoryExportScheduler) decisionLogStateAt(st *anchorState) string {
	c := s.decisionAttestConfig()
	if !c.enabled() {
		return decisionLogStateNotEnabled
	}
	if st == nil || len(st.conflicts) > 0 {
		// The anchor stream cannot be evaluated (unreadable) or contradicts
		// itself. Nothing about the chain is assertable — fail-closed.
		return decisionLogStateDivergent
	}
	if st.window.Entries == 0 {
		return decisionLogStateNoAnchor
	}
	// F1 (final review MAJOR-1): the comparison is against the newest ANCHORED
	// entry — an entry that exists but was never confirmed anchors nothing.
	// The previous version compared against the newest entry regardless of its
	// delivery state, so a record whose anchor dispatch FAILED (transient
	// ENOSPC/permissions) and was never followed by new decisions read as
	// `attested` forever — a false out-of-domain claim exactly in the
	// forensic-critical silent-tail scenario (probe-reproduced).
	anchoredPubID, anchoredDigest := int64(0), ""
	haveAnchored := false
	for _, ae := range st.latest {
		if ae.State != anchorStateAnchored {
			continue
		}
		if !haveAnchored || ae.PublicationID > anchoredPubID {
			anchoredPubID, anchoredDigest = ae.PublicationID, ae.ManifestDigest
			haveAnchored = true
		}
	}
	if !haveAnchored {
		return decisionLogStateNoAnchor
	}
	headSeq, headDigest, chainOK := walkDecisionChain(decisionLogPath(s.cfg.Dir))
	if !chainOK {
		// A broken link inside the chain is content tampering.
		return decisionLogStateDivergent
	}
	if headSeq < anchoredPubID {
		// The head is a strict PREFIX of the anchored head: the tail was removed.
		// A legal prefix compaction never lands here — it drops the oldest groups
		// and leaves the head untouched (I6).
		return decisionLogStateTruncated
	}
	if headSeq == anchoredPubID && headDigest != anchoredDigest {
		// Same length, different content: the log was rewritten.
		return decisionLogStateDivergent
	}
	if headSeq > anchoredPubID {
		// Durable records BEYOND the witnessed point: their anchor dispatch has
		// not been confirmed (transient failure — the sweep retries; or the
		// dispatch permanently failed). Honest state: not attested, loudly
		// named. The strict-equality rule (ADR-071 §3 ①) wins over the ADR-072
		// relaxation — the relaxation's assumption (a newer anchor entry covers
		// the gap) is exactly what failed here.
		return decisionLogStatePendingAnchor
	}
	// The chain is sound and its head is exactly the anchored head.
	return decisionLogStateAttested
}

// fillDecisionAttest adds the three source-specific fields to the sixth family's
// row (ADR-072 §4). Every other family's row omits them, so the four pre-existing
// rows stay byte-identical.
//
// `decision_attested` is a CONJUNCTION of four conditions and none may be
// dropped (ADR-071 §3, review M2): the local recompute (state == attested), the
// delivery face (the newest anchor entry is `anchored`), the honest loss counter
// (no never-persisted decision in this window) and the absence of a structural
// error. Without the first conjunct the criterion would degenerate into "an
// anchor entry exists", and an anchor entry is written on every tick that
// records anything — i.e. it would be near-tautological.
func (s *HistoryExportScheduler) fillDecisionAttest(row *anchorDeliveryStreamStatus, st *anchorState) {
	c := s.decisionAttestConfig()
	dropped := int64(0)
	if c.sink != nil {
		dropped = c.sink.Dropped()
	}
	lossReadErr := ""
	if c.sink != nil {
		// F3: the sticky constructor-captured loss error — a corrupt counter
		// must not read as zero (fail-open laundering). The persisted count is
		// already INSIDE sink.dropped (the constructor loaded it), so no second
		// read happens here: adding it again would double-count the losses.
		if c.sink.lossErr != "" {
			lossReadErr = c.sink.lossErr
		}
	}
	row.DecisionQueueDropped = &dropped

	anchoredHead := false
	if st != nil && len(st.latest) > 0 {
		if e, ok := st.latest[st.window.MaxSeq]; ok && e.State == anchorStateAnchored {
			anchoredHead = true
		}
	}
	state := s.decisionLogStateAt(st)
	row.DecisionLogState = state

	attested := state == decisionLogStateAttested &&
		anchoredHead &&
		dropped == 0 &&
		row.Error == "" &&
		lossReadErr == "" &&
		len(row.Conflicts) == 0
	if lossReadErr != "" {
		row.Error = strings.TrimSpace(row.Error + " " + lossReadErr)
	}
	row.DecisionAttested = &attested
}
