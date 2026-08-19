package observability

import (
	"sync"

	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/tracing"
)

// TracingBridge is the Phase 20 causal-tracing adapter (ADR-045 §2.3). It is a
// peripheral READ-MODEL adapter: it consumes existing execution.ExecutionEvent
// publications and (a) populates the vestigial Observation.TraceID field and
// (b) records a causal Span into an independent tracing.TraceRing. It holds NO
// execution state and never calls back into Runtime Core (R20-3/R20-4). The
// TraceRing is owned by the harness; the bridge only writes to it.
type TracingBridge struct {
	c           *Collector
	ring        *tracing.TraceRing
	mu          sync.Mutex
	traceByExec map[string]string // ingest-time correlation: groups one execution's events into one trace
}

// NewTracingBridge builds a bridge over collector c and ring. A nil ring is a
// wiring error: the read surface then reports 503 (R20-10 evidence unavailable).
func NewTracingBridge(c *Collector, ring *tracing.TraceRing) *TracingBridge {
	return &TracingBridge{c: c, ring: ring, traceByExec: make(map[string]string)}
}

// Publish implements execution.EventBus. It records a trace-aware observation and
// a causal span. The trace identity is independent of the ExecutionID (R20-6):
// execution_id -> trace_id is an advisory, Refs-based grouping, never a
// derivation (R20-10).
func (b *TracingBridge) Publish(ev execution.ExecutionEvent) {
	traceID := b.traceFor(ev.ID)
	b.c.record(Observation{
		Timestamp:   ev.Timestamp,
		Source:      SourceExecution,
		Kind:        KindTrace,
		TraceID:     traceID,
		ExecutionID: ev.ID,
		RequestID:   ev.ID,
		Status:      string(ev.Status),
		Code:        string(ev.Type),
		Operation:   string(ev.Type),
	})
	span := tracing.NewSpan(traceID, "", string(ev.Type), map[string]string{"execution": ev.ID})
	span.Start = ev.Timestamp
	span.Ended = ev.Timestamp
	b.ring.Add(span)
}

// traceFor returns the trace id for an execution, minting a fresh independent id
// on first sight so all events of one execution share one trace. The map is an
// ingest-time optimization only; the read surface resolves execution_id -> trace
// via Refs, never via this map (R20-10).
func (b *TracingBridge) traceFor(execID string) string {
	if execID == "" {
		return tracing.NewTraceID()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.traceByExec[execID]; ok {
		return id
	}
	id := tracing.NewTraceID()
	b.traceByExec[execID] = id
	return id
}
