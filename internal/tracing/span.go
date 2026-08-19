package tracing

import (
	"context"
	"time"
)

// StartSpan begins a new span. If ctx already carries a span, the new span is a
// child: it inherits the parent's TraceID and sets ParentSpanID (R20-6). If ctx
// carries no span, a brand-new trace is minted. Never panics on a missing-span
// context (P-1).
func StartSpan(ctx context.Context, operation string) (context.Context, *Span) {
	parent := FromContext(ctx)
	s := &Span{
		SpanID:    newSpanID(),
		Operation: operation,
		Start:     time.Now(),
		Refs:      map[string]string{},
	}
	if parent != nil {
		s.TraceID = parent.TraceID
		s.ParentSpanID = parent.SpanID
	} else {
		s.TraceID = NewTraceID()
	}
	return context.WithValue(ctx, spanKey, s), s
}

// NewSpan builds a span with a freshly-minted SpanID. The caller supplies an
// independent trace identity (R20-6); parentSpanID is empty at a trace root.
// Start/Ended remain zero so the caller sets timing; Refs are advisory (R20-6).
func NewSpan(traceID, parentSpanID, operation string, refs map[string]string) Span {
	return Span{
		TraceID:      traceID,
		SpanID:       newSpanID(),
		ParentSpanID: parentSpanID,
		Operation:    operation,
		Refs:         refs,
	}
}

// End closes the span. Non-blocking (R20-7) and idempotent: calling it twice or
// on a nil span is a no-op, never a panic.
func (s *Span) End() {
	if s == nil || !s.Ended.IsZero() {
		return
	}
	s.Ended = time.Now()
}

// FromContext returns the span carried by ctx, or nil if none. Never panics on a
// missing or nil context (P-1).
func FromContext(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(spanKey).(*Span)
	return s
}

// TraceFromContext returns the trace id from ctx, or "" if none.
func TraceFromContext(ctx context.Context) string {
	s := FromContext(ctx)
	if s == nil {
		return ""
	}
	return s.TraceID
}

// WithRef attaches an advisory ref (R20-6, R20-10). Refs are never identity and
// never alter the span's TraceID/SpanID. Nil-safe: on a nil span it returns nil.
func (s *Span) WithRef(kind, id string) *Span {
	if s == nil {
		return nil
	}
	if s.Refs == nil {
		s.Refs = map[string]string{}
	}
	s.Refs[kind] = id
	return s
}
