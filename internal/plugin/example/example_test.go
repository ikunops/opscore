package example

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/compat"
	"github.com/YuDong999/opscore/internal/plugin/runtime"
	"github.com/YuDong999/opscore/internal/storage"
)

// captureSink records audit events emitted by the Executor.
type captureSink struct{ events []core.AuditEvent }

func (s *captureSink) Emit(e core.AuditEvent) { s.events = append(s.events, e) }

// TestHostInfoPlugin_EndToEnd walks the entire Runtime Contract chain for a
// real (handler-backed) plugin and asserts it reaches Execution + Audit.
//
// This is the Phase 4.2 deliverable (GPT Round 18): prove the frozen Contract
// is natural to build against, end to end, with no Contract changes.
func TestHostInfoPlugin_EndToEnd(t *testing.T) {
	store := storage.NewMemoryStorage()
	reg := core.NewRegistry()

	// syncFunc projects the plugin's operations into durable Storage (the
	// Permission Sync: Manifest -> OperationMetadata). The Dispatcher plans
	// from the in-memory core.Registry; Storage is only its durable projection.
	mgr := runtime.NewManager(reg, store, func(name string, ops []core.Operation) error {
		for _, op := range ops {
			if _, err := store.Operations().Save(storage.Operation{
				Name:         op.Name,
				ResourceType: op.Permission.ResourceType,
				ActionType:   op.Permission.Action,
				Risk:         op.Risk.String(),
				Source:       op.Source,
				Enabled:      false,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	mgr.SetKernel(compat.KernelInfo{Version: "0.1.0", SupportedAPIs: []string{"opscore.plugin/v1"}})
	mgr.SetGate(compat.DefaultGate{})

	ctx := context.Background()
	enabled, errs := mgr.Bootstrap(ctx, NewLoader(), []string{"os.linux"},
		runtime.BootstrapPolicy{AutoEnableNewPlugin: true})
	if len(errs) > 0 {
		t.Fatalf("bootstrap errors: %v", errs)
	}
	if len(enabled) != 1 || enabled[0] != "hostinfo@1.0.0" {
		t.Fatalf("expected [hostinfo@1.0.0] enabled, got %v", enabled)
	}

	d, ok := mgr.Get("hostinfo@1.0.0")
	if !ok || d.State != runtime.StateEnabled {
		t.Fatalf("plugin not enabled: ok=%v state=%v", ok, d.State)
	}

	op, ok := reg.Get("plugin.hostinfo.collect")
	if !ok {
		t.Fatal("operation not registered in core Registry")
	}
	if op.Handler == nil {
		t.Fatal("registered operation has nil Handler")
	}

	// Drive the operation through the Dispatcher -> Executor (SSOT) and assert
	// it executes and emits an audit event.
	sink := &captureSink{}
	exec := core.NewExecutor(sink)
	disp := core.NewDispatcher(reg, exec)
	runCtx := core.NewContext().
		WithUser(core.UserContext{ID: "tester", Name: "Tester"}).
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))).
		Build()

	res := disp.Execute(runCtx, "plugin.hostinfo.collect", nil)
	if !res.Success {
		t.Fatalf("execution failed: %v", res.Error)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty execution output")
	}
	if len(sink.events) == 0 {
		t.Fatal("expected at least one audit event from the Executor")
	}
	if sink.events[0].OperationName != "plugin.hostinfo.collect" {
		t.Fatalf("audit OperationName = %q", sink.events[0].OperationName)
	}
}
