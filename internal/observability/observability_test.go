package observability

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
	"github.com/YuDong999/opscore/internal/tracing"
)

// TestObservabilityDoesNotImportRuntime enforces ADR-015 MUST-5: Observability
// is a peripheral READ MODEL and must never import the Runtime execution engine
// (internal/plugin/runtime) nor process-isolation internals
// (internal/plugin/isolation) — doing so would let it re-implement execution
// behavior and break the freeze. This is the same AST-guard discipline used
// across the Plugin Ecosystem (catalog, registry, compat, oci, dist). The guard
// inspects the package SOURCE files only; this test file's own build imports
// (go/parser etc.) are exempt.
func TestObservabilityDoesNotImportRuntime(t *testing.T) {
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"github.com/YuDong999/opscore/internal/plugin/runtime":   true,
		"github.com/YuDong999/opscore/internal/plugin/isolation": true,
	}
	for _, f := range matches {
		if filepath.Base(f) == "observability_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range file.Imports {
			path := imp.Path.Value
			if len(path) >= 2 {
				path = path[1 : len(path)-1]
			}
			if forbidden[path] {
				t.Errorf("%s must not import forbidden package %s (ADR-015 MUST-5)", f, path)
			}
		}
	}
}

// TestCollectorIngestsAllFourSources proves the Pilot capability consumes every
// existing event type through its adapter sinks and derives metrics + correlation
// without touching the Runtime Contract.
func TestCollectorIngestsAllFourSources(t *testing.T) {
	c := NewCollector()

	// 1) execution lifecycle event (via ExecutionBus adapter)
	NewExecutionBus(c).Publish(execution.ExecutionEvent{
		Type:      execution.EventExecutionStarted,
		ID:        "exe-1",
		Status:    execution.StatusRunning,
		Timestamp: time.Now(),
	})

	// 2) sandbox envelope decision (Phase 6.1)
	SandboxSink(c)(sandbox.Decision{
		Operation: "disk.free", Source: "plugin:foo",
		Allowed: true, Code: sandbox.CodeAllowed, Reason: "within envelope",
	})

	// 3) signature verification result (Phase 5)
	SignatureSink(c)(manifest.SignatureResult{
		Verified: true, SignerID: "k1", Code: "OK", PolicyDecision: "trusted key",
	})

	// 4) audit event (compliance correlation)
	NewAuditSinkAdapter(c).Emit(core.AuditEvent{
		TraceID: "tr-1", ExecutionID: "exe-1", OperationName: "disk.free",
		Result: core.ExecutionResult{Success: true}, Duration: 12 * time.Millisecond,
	})

	if got := c.Count(); got != 4 {
		t.Fatalf("expected 4 observations, got %d", got)
	}

	// Round 44 SHOULD-1: every record is stamped with the read-model schema
	// version (observability-local, never a Runtime Contract).
	for _, o := range c.Query(Query{}) {
		if o.SchemaVersion != SchemaVersion {
			t.Errorf("observation %s has SchemaVersion %q, want %q", o.ObsID, o.SchemaVersion, SchemaVersion)
		}
	}

	// derived counters
	if got := c.Counter("observations_total", map[string]string{"source": "execution"}); got != 1 {
		t.Errorf("execution observations_total = %d, want 1", got)
	}
	if got := c.Counter("verdict_total", map[string]string{"source": "sandbox", "verdict": "allow"}); got != 1 {
		t.Errorf("sandbox allow verdict_total = %d, want 1", got)
	}
	if got := c.Counter("verdict_total", map[string]string{"source": "signature", "verdict": "verified"}); got != 1 {
		t.Errorf("signature verified verdict_total = %d, want 1", got)
	}
	if got := c.Counter("execution_status_total", map[string]string{"status": "RUNNING"}); got != 1 {
		t.Errorf("execution_status_total RUNNING = %d, want 1", got)
	}

	// audit correlation: the same ExecutionID links the execution trace + audit
	linked := c.Query(Query{ExecutionID: "exe-1"})
	if len(linked) != 2 {
		t.Fatalf("expected 2 observations linked by exe-1, got %d", len(linked))
	}

	// read-only by construction: Query returns copies, never mutates the store
	before := c.Count()
	_ = c.Query(Query{})
	if c.Count() != before {
		t.Fatalf("Query mutated the store: before=%d after=%d", before, c.Count())
	}
}

// TestObservationTraceIDPopulated (P-2 adapter-side, ADR-045 §2.3) proves the
// tracing adapter populates the vestigial Observation.TraceID and that it is
// independent of the ExecutionID (R20-6).
func TestObservationTraceIDPopulated(t *testing.T) {
	ring := tracing.NewTraceRing(tracing.DefaultTraceRingCapacity)
	c := NewCollector()
	bridge := NewTracingBridge(c, ring)
	bridge.Publish(execution.ExecutionEvent{
		Type:      execution.EventExecutionStarted,
		ID:        "exe-trace-1",
		Status:    execution.StatusRunning,
		Timestamp: time.Now(),
	})
	found := false
	for _, o := range c.Observations() {
		if o.ExecutionID == "exe-trace-1" {
			found = true
			if o.TraceID == "" {
				t.Fatal("Observation.TraceID must be populated by the tracing adapter")
			}
			if o.TraceID == "exe-trace-1" {
				t.Fatal("Observation.TraceID must not equal ExecutionID (R20-6)")
			}
		}
	}
	if !found {
		t.Fatal("expected an observation for exe-trace-1")
	}
}

// TestObservationTraceIDAdvisory (R20-10) proves two executions get independent
// trace ids and that no TraceID is ever synthesized from an ExecutionID.
func TestObservationTraceIDAdvisory(t *testing.T) {
	ring := tracing.NewTraceRing(tracing.DefaultTraceRingCapacity)
	c := NewCollector()
	bridge := NewTracingBridge(c, ring)
	bridge.Publish(execution.ExecutionEvent{Type: execution.EventExecutionStarted, ID: "exe-a", Status: execution.StatusRunning, Timestamp: time.Now()})
	bridge.Publish(execution.ExecutionEvent{Type: execution.EventExecutionFinished, ID: "exe-a", Status: execution.StatusSuccess, Timestamp: time.Now()})
	bridge.Publish(execution.ExecutionEvent{Type: execution.EventExecutionStarted, ID: "exe-b", Status: execution.StatusRunning, Timestamp: time.Now()})

	ids := map[string]string{}
	for _, o := range c.Observations() {
		if o.ExecutionID == "exe-a" {
			ids["a"] = o.TraceID
		}
		if o.ExecutionID == "exe-b" {
			ids["b"] = o.TraceID
		}
	}
	if ids["a"] == "" || ids["b"] == "" {
		t.Fatal("both executions must have a trace id")
	}
	if ids["a"] == ids["b"] {
		t.Fatal("independent executions must have distinct trace ids (R20-6)")
	}
	if ids["a"] == "exe-a" || ids["b"] == "exe-b" {
		t.Fatal("trace id must not be derived from execution id (R20-10)")
	}
}
