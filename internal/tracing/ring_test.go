package tracing

import (
	"fmt"
	"testing"
)

// TestRingEvictionIsCounted (P-5 / M-5) proves bounded eviction increments
// DroppedCount and flips Complete to false (R20-8 honesty).
func TestRingEvictionIsCounted(t *testing.T) {
	r := NewTraceRing(3)
	for i := 0; i < 3; i++ {
		r.Add(Span{TraceID: "t", SpanID: fmt.Sprintf("s%d", i), Operation: "op"})
	}
	if r.DroppedCount() != 0 {
		t.Fatalf("no eviction yet, dropped=%d", r.DroppedCount())
	}
	if !r.Complete() {
		t.Fatal("ring complete before eviction")
	}
	r.Add(Span{TraceID: "t", SpanID: "s3", Operation: "op"})
	if r.DroppedCount() != 1 {
		t.Fatalf("expected 1 dropped, got %d", r.DroppedCount())
	}
	if r.Complete() {
		t.Fatal("ring must be incomplete after eviction (R20-8)")
	}
}

// TestTraceRingTruncatedFlag (P-7 / M-7) proves an evicted ring reports
// truncated=true on a successful query (R20-10 honesty).
func TestTraceRingTruncatedFlag(t *testing.T) {
	r := NewTraceRing(2)
	r.Add(Span{TraceID: "t1", SpanID: "a", Operation: "op"})
	r.Add(Span{TraceID: "t1", SpanID: "b", Operation: "op"})
	r.Add(Span{TraceID: "t1", SpanID: "c", Operation: "op"}) // overflow -> eviction
	spans, truncated := r.QueryByTrace("t1")
	if !truncated {
		t.Fatal("evicted ring must report truncated=true (R20-10)")
	}
	if len(spans) == 0 {
		t.Fatal("expected surviving spans for t1")
	}
}

// TestRingQueryEmptyTraceID proves an empty trace id yields (nil, false) and
// never reports truncation.
func TestRingQueryEmptyTraceID(t *testing.T) {
	r := NewTraceRing(2)
	r.Add(Span{TraceID: "t", SpanID: "a", Operation: "op"})
	if _, truncated := r.QueryByTrace(""); truncated {
		t.Fatal("empty trace id query must not report truncated")
	}
}

// TestRingCapacityDefault proves a non-positive capacity falls back to the
// default bound.
func TestRingCapacityDefault(t *testing.T) {
	r := NewTraceRing(0)
	if r.Capacity() != DefaultTraceRingCapacity {
		t.Fatalf("zero capacity must fall back to default %d, got %d", DefaultTraceRingCapacity, r.Capacity())
	}
}
