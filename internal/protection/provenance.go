package protection

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DecisionProvenance is a single, decision-time record of one Gate decision
// (R24-1 Decision-Time Provenance: generated AT decision time, never
// reconstructed after the fact). It is the unit of the Phase 24.2 decision-log
// projection.
//
// Secret boundary (R24-7): provenance carries ONLY the principal HASH (never
// cleartext) plus ADVISORY refs — the guard name, the decision, the action, and
// the threshold/observed values that informed the decision. It MUST NEVER carry
// a token, cookie, API key, or any other secret. This makes the decision-log
// safe to ship to an observability backend.
type DecisionProvenance struct {
	// TraceID is the trace identity carried on the request context (advisory
	// ref only, per R20-6). Empty when the request entered without a span.
	TraceID string `json:"trace_id"`
	// CapabilityID is the capability/operation the Gate evaluated.
	CapabilityID string `json:"capability_id"`
	// PrincipalHash is the salted SHA-256 of the principal. Never cleartext.
	PrincipalHash string `json:"principal_hash"`
	// Guard is the decision stage: "kill", "principal_kill", "breaker",
	// "concurrency", "quota", "rate", or "admit".
	Guard string `json:"guard"`
	// Decision is "admit" or "reject".
	Decision string `json:"decision"`
	// Action is the protection.* vocabulary constant for the decision.
	Action string `json:"action"`
	// Threshold is the advisory guard threshold that informed the decision
	// (e.g. breaker failure threshold, concurrency cap, bucket capacity, or
	// quota ceiling). Empty when the guard has no numeric threshold.
	Threshold string `json:"threshold"`
	// Observed is the advisory value the guard measured (e.g. effective failure
	// count, "exhausted", or the observed usage vs ceiling).
	Observed string `json:"observed"`
	// Detail is a free-text advisory note (e.g. breaker state string, or
	// "quota evidence missing/incomplete"). Never carries a secret.
	Detail string `json:"detail"`
	// LatencyMicros is the wall-clock decision latency (now - gate entry),
	// recorded as a pure observer (R24-1: evidence, not a re-evaluation).
	LatencyMicros int64 `json:"latency_micros"`
	// At is the decision timestamp.
	At time.Time `json:"at"`
}

// ProvenanceSink receives decision provenance from the Gate. Emit MUST be
// non-blocking and MUST NOT alter the Gate decision (R24-4 Observation
// Non-Interference): a slow or full sink never delays or changes an admission.
type ProvenanceSink interface {
	Emit(ctx context.Context, p DecisionProvenance)
}

// ProvenanceStore is the read projection of the decision-log (R24-5 Projection
// Only: it is derived from Gate decisions, never a Source of Truth). The
// RecordingProvenanceSink implements both ProvenanceSink and ProvenanceStore.
type ProvenanceStore interface {
	// Recent returns up to n most-recent provenance records (oldest first).
	Recent(n int) []DecisionProvenance
	// ByTraceID returns all records sharing a trace id (correlation across
	// guards within one request trace).
	ByTraceID(id string) []DecisionProvenance
	// ByCapability returns all records for a capability id.
	ByCapability(cap string) []DecisionProvenance
	// Stats exposes completeness/truncation signals (R24-7 provenance loss
	// honesty): capacity, current buffered count, total dropped, and whether
	// truncation has ever occurred.
	Stats() ProvenanceStats
}

// ProvenanceStats surfaces the sink's completeness state (R24-7).
type ProvenanceStats struct {
	Capacity  int   `json:"capacity"`
	Buffered  int   `json:"buffered"`
	Dropped   int64 `json:"dropped"`
	Truncated bool  `json:"truncated"`
}

// RecordingProvenanceSink is a bounded, in-memory decision-log sink that
// implements both ProvenanceSink and ProvenanceStore. Emit is non-blocking
// (no I/O, a bounded mutex held for microseconds) and therefore never delays
// the Gate decision path (R24-4).
//
// Bounded loss is HONEST (R24-7): when the buffer is full, the OLDEST record is
// evicted deterministically, Dropped is incremented by exactly one, and
// Truncated is set true permanently — so a consumer can detect that the
// decision-log is incomplete rather than silently believing a partial log is
// whole.
type RecordingProvenanceSink struct {
	capN      int
	mu        sync.Mutex
	buf       []DecisionProvenance
	dropped   atomic.Int64
	truncated atomic.Bool
}

// NewRecordingProvenanceSink builds a bounded sink. capacity<=0 defaults to
// 4096 records.
func NewRecordingProvenanceSink(capacity int) *RecordingProvenanceSink {
	if capacity <= 0 {
		capacity = 4096
	}
	return &RecordingProvenanceSink{
		capN: capacity,
		buf:  make([]DecisionProvenance, 0, capacity),
	}
}

// Emit records one decision. Non-blocking; on a full buffer the oldest record
// is evicted and Dropped/Truncated updated (R24-7 deterministic, honest loss).
func (s *RecordingProvenanceSink) Emit(ctx context.Context, p DecisionProvenance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) >= s.capN {
		// Deterministic eviction of the oldest record.
		s.buf = s.buf[1:]
		s.dropped.Add(1)
		s.truncated.Store(true)
	}
	s.buf = append(s.buf, p)
}

// Stats returns the sink completeness/truncation signals (R24-7).
func (s *RecordingProvenanceSink) Stats() ProvenanceStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ProvenanceStats{
		Capacity:  s.capN,
		Buffered:  len(s.buf),
		Dropped:   s.dropped.Load(),
		Truncated: s.truncated.Load(),
	}
}

// Recent returns up to n most-recent records (oldest first).
func (s *RecordingProvenanceSink) Recent(n int) []DecisionProvenance {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.buf) {
		n = len(s.buf)
	}
	out := make([]DecisionProvenance, n)
	copy(out, s.buf[len(s.buf)-n:])
	return out
}

// ByTraceID returns all records sharing a trace id.
func (s *RecordingProvenanceSink) ByTraceID(id string) []DecisionProvenance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DecisionProvenance, 0, len(s.buf))
	for _, p := range s.buf {
		if p.TraceID == id {
			out = append(out, p)
		}
	}
	return out
}

// ByCapability returns all records for a capability id.
func (s *RecordingProvenanceSink) ByCapability(cap string) []DecisionProvenance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DecisionProvenance, 0, len(s.buf))
	for _, p := range s.buf {
		if p.CapabilityID == cap {
			out = append(out, p)
		}
	}
	return out
}
