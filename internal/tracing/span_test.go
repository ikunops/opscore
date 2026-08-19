package tracing

import (
	"context"
	"testing"
	"time"
)

// TestTraceIsNilSafe (P-1 / M-1) proves every tracing helper is panic-free on a
// missing-span context and on a nil span.
func TestTraceIsNilSafe(t *testing.T) {
	if FromContext(context.Background()) != nil {
		t.Fatal("FromContext on bare context must return nil")
	}
	if TraceFromContext(context.Background()) != "" {
		t.Fatal("TraceFromContext on bare context must return empty")
	}
	ctx, span := StartSpan(context.Background(), "op")
	if span == nil {
		t.Fatal("StartSpan must return a span")
	}
	if TraceFromContext(ctx) != span.TraceID {
		t.Fatal("TraceFromContext must reflect the started span")
	}
	// nil span operations must not panic.
	var nilSpan *Span
	nilSpan.End()
	nilSpan.WithRef("k", "v")
	// nil context is tolerated.
	if FromContext(nil) != nil {
		t.Fatal("FromContext(nil) must return nil")
	}
}

// TestStartSpanChildInheritsTrace proves a child span inherits the parent's
// TraceID and sets ParentSpanID while keeping its own SpanID (R20-6).
func TestStartSpanChildInheritsTrace(t *testing.T) {
	ctx, root := StartSpan(context.Background(), "root")
	_, child := StartSpan(ctx, "child")
	if child.TraceID != root.TraceID {
		t.Fatal("child must inherit parent trace id (R20-6)")
	}
	if child.ParentSpanID != root.SpanID {
		t.Fatal("child must set parent span id")
	}
	if child.SpanID == root.SpanID {
		t.Fatal("child must have its own span id")
	}
}

// TestSpanEndIsNonBlocking (R20-7 / M-6) proves End() returns without blocking.
func TestSpanEndIsNonBlocking(t *testing.T) {
	_, span := StartSpan(context.Background(), "op")
	done := make(chan struct{})
	go func() {
		span.End()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("End() blocked (R20-7)")
	}
	if span.Ended.IsZero() {
		t.Fatal("End() must set end time")
	}
}

// TestSpanEndIdempotent (R20-7) proves a second End() is a no-op.
func TestSpanEndIdempotent(t *testing.T) {
	_, span := StartSpan(context.Background(), "op")
	span.End()
	first := span.Ended
	time.Sleep(time.Millisecond)
	span.End()
	if !span.Ended.Equal(first) {
		t.Fatal("End() must be idempotent (R20-7)")
	}
}

// TestWithRefAdvisory (M-3) proves a ref is attached yet never becomes identity.
func TestWithRefAdvisory(t *testing.T) {
	_, span := StartSpan(context.Background(), "op")
	span.WithRef("execution", "exec-9")
	if id, ok := span.Ref("execution"); !ok || id != "exec-9" {
		t.Fatal("ref not attached")
	}
	if span.TraceID == "exec-9" {
		t.Fatal("a ref must not become identity (R20-6/R20-10)")
	}
}
