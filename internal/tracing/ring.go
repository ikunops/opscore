package tracing

import "sync"

// DefaultTraceRingCapacity is the bounded capacity of a TraceRing (R20-8).
const DefaultTraceRingCapacity = 5000

// TraceRing is a bounded, honest span store (R20-8). It is deliberately separate
// from observability.Collector — merging them would touch the existing Collector
// contract (R20-3 frozen-package boundary). Eviction is counted and surfaced as
// a truncated flag, never silently dropped (R20-10).
type TraceRing struct {
	mu       sync.Mutex
	capacity int
	buf      []Span
	dropped  int64
}

// NewTraceRing constructs a bounded ring. A non-positive capacity falls back to
// DefaultTraceRingCapacity.
func NewTraceRing(capacity int) *TraceRing {
	if capacity <= 0 {
		capacity = DefaultTraceRingCapacity
	}
	return &TraceRing{
		capacity: capacity,
		buf:      make([]Span, 0, capacity),
	}
}

// Capacity returns the ring capacity.
func (r *TraceRing) Capacity() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capacity
}

// DroppedCount returns the number of spans evicted under bounded capacity.
func (r *TraceRing) DroppedCount() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Complete reports whether the ring has never evicted a span. false iff
// DroppedCount > 0 (R20-8 honesty).
func (r *TraceRing) Complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped == 0
}

// Add appends a span. When the ring is full the oldest span is evicted and
// DroppedCount is incremented. Non-blocking (R20-7).
func (r *TraceRing) Add(s Span) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) >= r.capacity {
		r.buf = r.buf[1:]
		r.dropped++
	}
	r.buf = append(r.buf, s)
}

// QueryByTrace returns all spans for a trace and a truncated flag. truncated is
// true iff the ring has evicted any span (R20-10 honesty: eviction is never a
// clean absence). A query with an empty traceID returns (nil, false).
func (r *TraceRing) QueryByTrace(traceID string) ([]Span, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if traceID == "" {
		return nil, false
	}
	out := make([]Span, 0, len(r.buf))
	for _, s := range r.buf {
		if s.TraceID == traceID {
			out = append(out, s)
		}
	}
	return out, r.dropped > 0
}

// QueryByRef returns all spans carrying an advisory ref of the given kind/id,
// and the truncated flag (R20-10 honesty). It is the read-side half of advisory
// resolution: the management surface uses it to map execution_id -> trace_id via
// Refs WITHOUT deriving any identity (R20-10). A query with an empty kind or id
// returns (nil, false).
func (r *TraceRing) QueryByRef(kind, id string) ([]Span, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "" || id == "" {
		return nil, false
	}
	out := make([]Span, 0, len(r.buf))
	for _, s := range r.buf {
		if s.Refs != nil && s.Refs[kind] == id {
			out = append(out, s)
		}
	}
	return out, r.dropped > 0
}
