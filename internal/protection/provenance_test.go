package protection

import (
	"context"
	"testing"
	"time"
)

// newProvenanceGate builds a Gate wired with a RecordingProvenanceSink so
// decision-time provenance can be observed end-to-end (R24-1).
func newProvenanceGate(sink *RecordingProvenanceSink) *Gate {
	ks := NewKillStore(newFakeKillPersistence(), time.Now)
	_ = ks.Bootstrap()
	return New(Config{
		KillStore: ks,
		Breaker:   NewBreakerSet(&fakeFailureReader{}, DefaultBreakerConfig(), time.Now),
		Sem:       NewSemaphoreSet(8),
		Buckets:   NewTokenBucketSet(TokenBucketConfig{Capacity: 1000, Refill: 1000}, time.Now),
		Audit:     &fakeAuditWriter{},
		Timeout:   NewTimeoutConfig(),
		Provenance: sink,
	})
}

// TestProvenance_CaptureAtDecisionTime proves the Gate emits a decision-time
// record on the admit path and that ProvenanceStore() exposes it.
func TestProvenance_CaptureAtDecisionTime(t *testing.T) {
	sink := NewRecordingProvenanceSink(4096)
	g := newProvenanceGate(sink)

	g.Check(context.Background(), "cap-x", "principal-y") // admit path

	store := g.ProvenanceStore()
	if store == nil {
		t.Fatal("ProvenanceStore() returned nil despite a configured sink")
	}
	recs := store.Recent(10)
	if len(recs) != 1 {
		t.Fatalf("want 1 provenance record, got %d", len(recs))
	}
	p := recs[0]
	if p.CapabilityID != "cap-x" || p.PrincipalHash == "" {
		t.Fatalf("capability/principal hash missing: %+v", p)
	}
	if p.Guard != "admit" || p.Decision != "admit" || p.Action != ActionAdmit {
		t.Fatalf("unexpected provenance record: %+v", p)
	}
	if p.TraceID != "" {
		t.Fatalf("context without a span must yield empty TraceID, got %q", p.TraceID)
	}
	if p.LatencyMicros < 0 {
		t.Fatalf("latency must be non-negative, got %d", p.LatencyMicros)
	}
}

// TestProvenance_NilNoOp proves a Gate built without a sink is byte-for-byte
// equivalent to pre-24.2: no emit, no panic, ProvenanceStore()==nil.
func TestProvenance_NilNoOp(t *testing.T) {
	ks := NewKillStore(newFakeKillPersistence(), time.Now)
	g := newTestGate(ks, &fakeFailureReader{}, &fakeAuditWriter{})
	if g.ProvenanceStore() != nil {
		t.Fatal("nil-provenance gate must report nil ProvenanceStore")
	}
	// Exercise the full decision path; must not panic or emit.
	g.Check(context.Background(), "c", "p")
	g.Check(context.Background(), "c", "p")
	if g.ProvenanceStore() != nil {
		t.Fatal("ProvenanceStore must stay nil after checks")
	}
}

// TestProvenanceSink_BoundedDeterministicDrop proves honest, deterministic
// loss under load (R24-7): once the buffer is full the oldest record is evicted,
// Dropped increments by exactly one, and Truncated is set permanently so a
// consumer can detect an incomplete log.
func TestProvenanceSink_BoundedDeterministicDrop(t *testing.T) {
	sink := NewRecordingProvenanceSink(4)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		sink.Emit(ctx, DecisionProvenance{CapabilityID: "c", At: time.Now()})
	}
	stats := sink.Stats()
	if stats.Capacity != 4 {
		t.Fatalf("capacity want 4 got %d", stats.Capacity)
	}
	if stats.Buffered != 4 {
		t.Fatalf("buffered want 4 got %d", stats.Buffered)
	}
	if stats.Dropped != 1 {
		t.Fatalf("dropped want 1 got %d", stats.Dropped)
	}
	if !stats.Truncated {
		t.Fatal("truncated must be true after eviction")
	}
	if len(sink.Recent(100)) != 4 {
		t.Fatalf("Recent must cap at capacity")
	}
}

// TestProvenanceStore_Filters proves the read-projection query methods work.
func TestProvenanceStore_Filters(t *testing.T) {
	sink := NewRecordingProvenanceSink(100)
	ctx := context.Background()
	sink.Emit(ctx, DecisionProvenance{CapabilityID: "a", TraceID: "t1"})
	sink.Emit(ctx, DecisionProvenance{CapabilityID: "b", TraceID: "t1"})
	sink.Emit(ctx, DecisionProvenance{CapabilityID: "a", TraceID: "t2"})

	if got := len(sink.ByCapability("a")); got != 2 {
		t.Fatalf("ByCapability(a) want 2 got %d", got)
	}
	if got := len(sink.ByTraceID("t1")); got != 2 {
		t.Fatalf("ByTraceID(t1) want 2 got %d", got)
	}
	if got := len(sink.ByTraceID("t2")); got != 1 {
		t.Fatalf("ByTraceID(t2) want 1 got %d", got)
	}
}
