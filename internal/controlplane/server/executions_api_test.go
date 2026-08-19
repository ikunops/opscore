package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/gorilla/websocket"
)

// TestExecutions_CreateAsync drives the new async create endpoint end-to-end:
// POST /api/executions returns 202 + a PLANNING id immediately, the run
// then transitions to RUNNING in the background, and the timeline projection
// reflects the created/started lifecycle. The blocking op is cancelled at
// the end so the background goroutine settles (no dangling RUNNING run).
func TestExecutions_CreateAsync(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/executions", token, map[string]any{
		"op":     "demo.block",
		"params": map[string]any{},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if body.ID == "" {
		t.Fatalf("create returned empty id: %s", rec.Body.String())
	}
	if body.Status != string(execution.StatusPlanning) {
		t.Fatalf("create status = %q, want PLANNING", body.Status)
	}

	// Cleanup: cancel the blocking run so the background goroutine settles.
	defer doJSON(t, h, "POST", "/api/executions/"+body.ID+"/cancel", token, nil)

	// Poll until the run reaches RUNNING (it blocks until cancelled).
	var got execution.Status
	for i := 0; i < 200; i++ {
		gr := doJSON(t, h, "GET", "/api/executions/"+body.ID, token, nil)
		var gb struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(gr.Body.Bytes(), &gb); err == nil && gb.Status != "" {
			got = execution.Status(gb.Status)
			if got == execution.StatusRunning {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got != execution.StatusRunning {
		t.Fatalf("run never reached RUNNING, last status=%q", got)
	}

	// Timeline must show created + started events (in that order).
	tr := doJSON(t, h, "GET", "/api/executions/"+body.ID+"/timeline", token, nil)
	if tr.Code != http.StatusOK {
		t.Fatalf("timeline status = %d body=%s", tr.Code, tr.Body.String())
	}
	var tb struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(tr.Body.Bytes(), &tb); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if tb.ID != body.ID {
		t.Fatalf("timeline id = %q, want %q", tb.ID, body.ID)
	}
	if len(tb.Events) < 2 || tb.Events[0].Type != "created" || tb.Events[1].Type != "started" {
		t.Fatalf("timeline events = %+v, want [created, started, ...]", tb.Events)
	}
}

// TestExecutions_CreateValidation covers the rejection paths of the create
// endpoint: missing op (400), unknown op (404), and a non-admin caller
// lacking execution.create (403).
func TestExecutions_CreateValidation(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	adminToken := login(t, h, "admin", "adminpw")

	// No op in body -> 400.
	rec := doJSON(t, h, "POST", "/api/executions", adminToken, map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty op: want 400, got %d", rec.Code)
	}

	// Unknown op -> 404.
	rec = doJSON(t, h, "POST", "/api/executions", adminToken, map[string]any{
		"op": "no.such.op",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown op: want 404, got %d", rec.Code)
	}

	// Non-admin (bob, no roles) -> 403 (missing execution.create grant).
	if _, err := srv.auth.Register("bob", "bobpw"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	bobToken := login(t, h, "bob", "bobpw")
	rec = doJSON(t, h, "POST", "/api/executions", bobToken, map[string]any{
		"op": "demo.block",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob create: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestExecutions_TimelineProjection seeds a completed execution with two steps
// and asserts the timeline is projected in lifecycle order with the correct
// per-event statuses.
func TestExecutions_TimelineProjection(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	now := time.Now()
	started := now.Add(100 * time.Millisecond)
	finished := started.Add(200 * time.Millisecond)
	_ = srv.runtime.Store().Create(execution.ExecutionRecord{
		ID:         "ex-tl",
		Operation:  "demo.block",
		Status:     execution.StatusSuccess,
		UserName:   "admin",
		CreatedAt:  now,
		StartedAt:  &started,
		FinishedAt: &finished,
		DurationMs: 200,
		Steps: []execution.ExecutionStepRecord{
			{Name: "stepA", Status: execution.StepSuccess, DurationMs: 100, Output: "ok-a"},
			{Name: "stepB", Status: execution.StepSuccess, DurationMs: 100, Output: "ok-b"},
		},
	})

	tr := doJSON(t, h, "GET", "/api/executions/ex-tl/timeline", token, nil)
	if tr.Code != http.StatusOK {
		t.Fatalf("timeline status = %d body=%s", tr.Code, tr.Body.String())
	}
	var tb struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Events []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"events"`
	}
	if err := json.Unmarshal(tr.Body.Bytes(), &tb); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if tb.Status != string(execution.StatusSuccess) {
		t.Fatalf("timeline status = %q, want SUCCESS", tb.Status)
	}
	want := []struct{ typ, st string }{
		{"created", "PLANNING"},
		{"started", "RUNNING"},
		{"step:stepA", "SUCCESS"},
		{"step:stepB", "SUCCESS"},
		{"finished", "SUCCESS"},
	}
	if len(tb.Events) != len(want) {
		t.Fatalf("timeline events = %+v, want %d events", tb.Events, len(want))
	}
	for i, w := range want {
		if tb.Events[i].Type != w.typ || tb.Events[i].Status != w.st {
			t.Fatalf("event[%d] = %+v, want {type:%q status:%q}", i, tb.Events[i], w.typ, w.st)
		}
	}
}

// TestExecutions_StreamWS verifies the WebSocket stream: a client connected
// with a valid, execution.read-granted token receives lifecycle events when
// an execution is created. The subscription is established on dial (handshake
// completion), so the event published by the subsequent POST is delivered.
func TestExecutions_StreamWS(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	ts := httptest.NewServer(h)
	defer ts.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/executions/stream"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Trigger an execution (blocks until cancelled).
	createRec := doJSON(t, h, "POST", "/api/executions", token, map[string]any{
		"op": "demo.block",
	})
	var cr struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &cr)
	defer doJSON(t, h, "POST", "/api/executions/"+cr.ID+"/cancel", token, nil)

	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ev map[string]any
	if err := c.ReadJSON(&ev); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if ev["Type"] == nil {
		t.Fatalf("ws event missing Type: %+v", ev)
	}
	// The streamed event carries the execution id + lifecycle status.
	if ev["ID"] == nil || ev["Status"] == nil {
		t.Fatalf("ws event missing ID/Status: %+v", ev)
	}
}
