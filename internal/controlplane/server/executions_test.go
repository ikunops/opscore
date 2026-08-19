package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/storage"
	"log/slog"
)

// blockingStep blocks until its context is cancelled, so a run stays RUNNING
// long enough for the test to observe and cancel it over HTTP.
type blockingStep struct{}

func (blockingStep) Describe() string { return "block" }
func (blockingStep) Execute(ctx core.Context) core.StepResult {
	<-ctx.Done()
	return core.StepResult{StepName: "block", Success: false, Error: ctx.Err()}
}

type blockHandler struct{}

func (blockHandler) Plan(_ core.Context, _ map[string]any) (*core.ExecutionPlan, error) {
	return &core.ExecutionPlan{
		OperationName: "demo.block",
		Steps:         []core.ExecutionStep{blockingStep{}},
	}, nil
}

// newTestServerWithRuntime wires a full Core + Runtime + Server with a blocking
// operation ("demo.block") so execution listing/cancellation can be exercised
// over HTTP (Phase 2.1.4).
func newTestServerWithRuntime(t *testing.T) (*Server, storage.Storage) {
	t.Helper()
	stor := storage.NewMemoryStorage()

	registry := core.NewRegistry()
	registry.Register(core.Operation{
		Name:    "demo.block",
		Handler: blockHandler{},
	})
	executor := core.NewExecutor(core.NewLogSink(slog.Default()))
	dispatcher := core.NewDispatcher(registry, executor)
	store := execution.NewMemoryStore()
	runtime := core.NewRuntime(executor, store)

	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		Runtime:        runtime,
		AccessSecret:   "access-secret",
		RefreshSecret:  "refresh-secret",
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, stor
}

func TestExecutions_Unauthenticated(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()

	cases := []struct {
		method, path string
	}{
		{"GET", "/api/executions"},
		{"GET", "/api/executions/abc"},
		{"POST", "/api/executions/abc/cancel"},
	}
	for _, c := range cases {
		rec := doJSON(t, h, c.method, c.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: want 401, got %d", c.method, c.path, rec.Code)
		}
	}
}

// TestExecutions_CancelFlow drives a real (blocking) run over HTTP, lists it
// while RUNNING, cancels it, and asserts the run settles into CANCELLED.
func TestExecutions_CancelFlow(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	// Start the blocking run in a goroutine.
	runDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		runDone <- doJSON(t, h, "POST", "/api/operations/demo.block/run", token, map[string]any{})
	}()

	// Poll until a RUNNING execution appears, grab its id.
	var execID string
	for i := 0; i < 200; i++ {
		rec := doJSON(t, h, "GET", "/api/executions", token, nil)
		var body struct {
			Executions []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"executions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil && len(body.Executions) > 0 {
			if body.Executions[0].Status == string(execution.StatusRunning) {
				execID = body.Executions[0].ID
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if execID == "" {
		t.Fatal("no RUNNING execution appeared")
	}

	// Cancel it.
	rec := doJSON(t, h, "POST", "/api/executions/"+execID+"/cancel", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d body=%s", rec.Code, rec.Body.String())
	}
	var cresp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cresp); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if cresp.Status != string(execution.StatusCancelRequested) {
		t.Fatalf("cancel status = %q, want CANCEL_REQUESTED", cresp.Status)
	}

	// The run must settle into CANCELLED with the same execution id.
	runRec := <-runDone
	var runBody struct {
		Cancelled   bool   `json:"cancelled"`
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(runRec.Body.Bytes(), &runBody); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if !runBody.Cancelled {
		t.Fatalf("run not cancelled: body=%s", runRec.Body.String())
	}
	if runBody.ExecutionID != execID {
		t.Fatalf("execution_id = %q, want %q", runBody.ExecutionID, execID)
	}
}

// TestExecutions_RBACOwnership verifies admin sees all, a non-admin sees only
// their own, and cross-user access is forbidden.
func TestExecutions_RBACOwnership(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()

	// Create a non-admin user "bob" and log in.
	if _, err := srv.auth.Register("bob", "bobpw"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	bobToken := login(t, h, "bob", "bobpw")
	adminToken := login(t, h, "admin", "adminpw")

	// Seed two executions owned by different users.
	now := time.Now()
	_ = srv.runtime.Store().Create(execution.ExecutionRecord{
		ID: "ex-alice", Operation: "demo.block", Status: execution.StatusRunning,
		UserName: "alice", CreatedAt: now,
	})
	_ = srv.runtime.Store().Create(execution.ExecutionRecord{
		ID: "ex-bob", Operation: "demo.block", Status: execution.StatusRunning,
		UserName: "bob", CreatedAt: now,
	})

	// Bob listing sees only his own.
	rec := doJSON(t, h, "GET", "/api/executions", bobToken, nil)
	var list struct {
		Executions []struct {
			ID string `json:"id"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Executions) != 1 || list.Executions[0].ID != "ex-bob" {
		t.Fatalf("bob list = %+v, want only ex-bob", list.Executions)
	}

	// Bob cannot view alice's execution (403), but admin can (200).
	rec = doJSON(t, h, "GET", "/api/executions/ex-alice", bobToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob get alice: want 403, got %d", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/executions/ex-alice", adminToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin get alice: want 200, got %d", rec.Code)
	}
}

// TestExecutions_CancelNotRunning verifies cancellation is rejected once the
// execution is no longer RUNNING (e.g. already finished).
func TestExecutions_CancelNotRunning(t *testing.T) {
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	_ = srv.runtime.Store().Create(execution.ExecutionRecord{
		ID: "ex-done", Operation: "demo.block", Status: execution.StatusSuccess,
		UserName: "admin", CreatedAt: time.Now(),
	})
	rec := doJSON(t, h, "POST", "/api/executions/ex-done/cancel", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel finished: want 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}
