package tracing

import "time"

// Span is the unit of causal tracing. It is plain data: no methods touch I/O,
// no methods take a context, no methods block (R20-7). A span is a passenger,
// not a driver (R20-4).
type Span struct {
	TraceID      string            `json:"trace_id"`       // opaque, random, independent identity (R20-6)
	SpanID       string            `json:"span_id"`        // unique within TraceID, independent identity (R20-6)
	ParentSpanID string            `json:"parent_span_id"` // empty at trace root
	Start        time.Time         `json:"start"`          // span start
	Ended        time.Time         `json:"end"`            // zero until End() called
	Operation    string            `json:"operation"`      // human-readable, NOT a kind/type (no SpanKind)
	Refs         map[string]string `json:"refs"`           // advisory only (R20-6, R20-10)
}

// Duration is a method, not a field, so End() can set Ended exactly once.
func (s Span) Duration() time.Duration {
	if s.Ended.IsZero() {
		return 0
	}
	return s.Ended.Sub(s.Start)
}

// Ref returns an advisory ref by kind. Refs are never identity (R20-6): a
// missing ref is a valid, non-error state (P-3).
func (s Span) Ref(kind string) (string, bool) {
	if s.Refs == nil {
		return "", false
	}
	id, ok := s.Refs[kind]
	return id, ok
}

// spanKey is the private context key for carrying a *Span (R20-5: context only,
// never global state).
type spanKeyType struct{}

var spanKey = spanKeyType{}
