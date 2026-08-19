package audit

import (
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

func TestStorageAuditSink_Emit(t *testing.T) {
	stor := storage.NewMemoryStorage()
	sink := NewStorageAuditSink(stor, nil)

	sink.Emit(core.AuditEvent{
		TraceID:       "trace-1",
		Timestamp:     time.Now(),
		OperationName: "system.service.restart",
		User:          core.UserContext{Name: "admin"},
		Permission:    core.Permission{ResourceType: "system.service", Action: "restart"},
		Risk:          core.RiskMedium,
		Input:         map[string]any{"name": "nginx"},
		Result:        core.ExecutionResult{Success: true, Duration: 42 * time.Millisecond},
	})

	events, err := stor.Audit().List(10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	e := events[0]
	if e.Actor != "admin" || e.Operation != "system.service.restart" {
		t.Fatalf("unexpected event: %+v", e)
	}
	if e.Result != "success" || e.Target != "nginx" {
		t.Fatalf("unexpected result/target: result=%q target=%q", e.Result, e.Target)
	}

	// failure path
	sink.Emit(core.AuditEvent{
		OperationName: "system.service.restart",
		User:          core.UserContext{Name: "admin"},
		Result:        core.ExecutionResult{Success: false, Error: errExample{}},
	})
	events, _ = stor.Audit().List(10)
	if len(events) != 2 {
		t.Fatalf("expected 2 events after failure, got %d", len(events))
	}
	if events[0].Result != "failure" {
		t.Fatalf("expected last event failure, got %q", events[0].Result)
	}
}

type errExample struct{}

func (errExample) Error() string { return "boom" }
