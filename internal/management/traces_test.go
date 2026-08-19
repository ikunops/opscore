package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/tracing"
)

// newTraceKit builds the management surface with a wired trace ring so the
// Phase 20 traces read surface can be exercised end to end.
func newTraceKit(t *testing.T, ringCap int) *kit {
	t.Helper()
	repo, err := governancepolicy.NewFileRepository(t.TempDir())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	audit := &fakeAudit{}
	a, err := NewTokenAuthenticator(testToken, "op-1")
	if err != nil {
		t.Fatalf("authn: %v", err)
	}
	collector := observability.NewCollector()
	ring := tracing.NewTraceRing(ringCap)
	var seq int
	srv, err := New(Config{
		Repo:             repo,
		Audit:            audit,
		Authenticator:    a,
		Collector:        collector,
		TraceRing:        ring,
		NewCorrelationID: func() string { seq++; return fmt.Sprintf("corr-%d", seq) },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &kit{t: t, repo: repo, audit: audit, ts: ts, srv: srv, collector: collector}
}

// addSpan appends a causal span to the ring with an "execution" ref.
func addSpan(ring *tracing.TraceRing, traceID, spanID, execID, operation string) {
	ring.Add(tracing.Span{
		TraceID:   traceID,
		SpanID:    spanID,
		Operation: operation,
		Start:     time.Now(),
		Ended:     time.Now(),
		Refs:      map[string]string{"execution": execID},
	})
}

type traceResponseBody struct {
	TraceID   string         `json:"trace_id"`
	Spans     []tracing.Span `json:"spans"`
	Truncated bool           `json:"truncated"`
}

// TestTracesByTraceID proves a direct trace_id lookup returns 200 with the trace.
func TestTracesByTraceID(t *testing.T) {
	k := newTraceKit(t, tracing.DefaultTraceRingCapacity)
	addSpan(k.srv.traceRing, "t1", "s1", "exe-1", "exec.started")
	resp, raw := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?trace_id=t1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, raw)
	}
	var body traceResponseBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TraceID != "t1" {
		t.Fatalf("trace_id = %q, want t1", body.TraceID)
	}
	if len(body.Spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(body.Spans))
	}
}

// TestTracesByExecutionID proves advisory execution_id -> trace_id resolution.
func TestTracesByExecutionID(t *testing.T) {
	k := newTraceKit(t, tracing.DefaultTraceRingCapacity)
	addSpan(k.srv.traceRing, "t1", "s1", "exe-1", "exec.started")
	resp, raw := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?execution_id=exe-1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, raw)
	}
	var body traceResponseBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TraceID != "t1" {
		t.Fatalf("advisory trace_id = %q, want t1", body.TraceID)
	}
}

// TestTracesAdvisoryNotDerived (P-11 / R20-10) proves an execution_id with NO
// matching Refs returns 404 — never a 200 with a synthesized TraceID.
func TestTracesAdvisoryNotDerived(t *testing.T) {
	k := newTraceKit(t, tracing.DefaultTraceRingCapacity)
	// A known execution with real refs exists, but we query a DIFFERENT unknown id.
	addSpan(k.srv.traceRing, "t1", "s1", "exe-real", "exec.started")
	resp, raw := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?execution_id=exe-unknown"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 (advisory, not synthesized), got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "trace_id") {
		t.Fatalf("unknown execution_id must not yield a synthesized trace_id: %s", raw)
	}
}

// TestTracesTruncatedNeverDropped (P-12 / R20-10) proves a 200 after ring
// eviction always carries truncated:true.
func TestTracesTruncatedNeverDropped(t *testing.T) {
	k := newTraceKit(t, 2) // capacity 2 -> 3rd add evicts
	addSpan(k.srv.traceRing, "t1", "s1", "exe-1", "exec.started")
	addSpan(k.srv.traceRing, "t1", "s2", "exe-1", "exec.step")
	addSpan(k.srv.traceRing, "t-other", "so", "exe-x", "exec.started") // overflow -> eviction
	resp, raw := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?trace_id=t1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, raw)
	}
	var body traceResponseBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Truncated {
		t.Fatalf("evicted ring must report truncated:true in 200 (R20-10): %s", raw)
	}
}

// TestTracesBadRequest proves missing / duplicate query params -> 400.
func TestTracesBadRequest(t *testing.T) {
	k := newTraceKit(t, tracing.DefaultTraceRingCapacity)
	resp, _ := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 (no params), got %d", resp.StatusCode)
	}
	resp2, _ := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?execution_id=exe-1&trace_id=t1"})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 (both params), got %d", resp2.StatusCode)
	}
}

// TestTracesNotFound proves a trace_id with no spans -> 404.
func TestTracesNotFound(t *testing.T) {
	k := newTraceKit(t, tracing.DefaultTraceRingCapacity)
	resp, _ := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?trace_id=ghost"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestTracesEvidenceUnavailable proves a nil ring -> 503 (R20-10).
func TestTracesEvidenceUnavailable(t *testing.T) {
	k := newKit(t) // no TraceRing wired -> srv.traceRing is nil
	resp, _ := k.do(call{method: http.MethodGet, path: RoutePrefix + "traces?trace_id=t1"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (ring unavailable), got %d", resp.StatusCode)
	}
}
