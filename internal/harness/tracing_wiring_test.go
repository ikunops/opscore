package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/management"
)

// TestTraceRingWiringReachesManagement (ADR-045 §2.5 / step 11) proves the
// trace ring the harness constructs is the SAME one the management traces read
// surface serves: a span ingested via the bridge is returned by the endpoint.
func TestTraceRingWiringReachesManagement(t *testing.T) {
	cfg := mgmtConfig(t, "s3cret")
	cfg.ManagementAddr = ":19083"
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	// Ingest a span through the harness-owned bridge.
	h.cap.bridge.Publish(execution.ExecutionEvent{
		Type: execution.EventExecutionStarted, ID: "exe-wired",
		Status: execution.StatusRunning, Timestamp: time.Now(),
	})

	ts := httptest.NewServer(h.mgmt.Handler)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+management.RoutePrefix+"traces?execution_id=exe-wired", nil)
	req.Header.Set(management.HeaderToken, "s3cret")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("traces: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("traces status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var body struct {
		TraceID string `json:"trace_id"`
		Spans   []struct {
			Refs map[string]string `json:"refs"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TraceID == "" {
		t.Fatal("trace_id empty in response")
	}
	if len(body.Spans) == 0 {
		t.Fatal("expected at least one span for exe-wired")
	}
}

// TestTraceRingWiringReachesSinks (ADR-045 §2.5) proves the harness constructs
// the TracingBridge and wires it to the shared ring: bridge.Publish lands a span
// in the ring the capabilities hold.
func TestTraceRingWiringReachesSinks(t *testing.T) {
	h, err := Build(context.Background(), mgmtConfig(t, "s3cret"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	if h.cap.bridge == nil {
		t.Fatal("harness did not construct the TracingBridge")
	}
	if h.cap.traceRing == nil {
		t.Fatal("harness did not construct the trace ring")
	}
	h.cap.bridge.Publish(execution.ExecutionEvent{
		Type: execution.EventExecutionFinished, ID: "exe-sink",
		Status: execution.StatusSuccess, Timestamp: time.Now(),
	})
	spans, _ := h.cap.traceRing.QueryByRef("execution", "exe-sink")
	if len(spans) == 0 {
		t.Fatal("bridge.Publish did not land a span in the harness ring")
	}
}
