// Package audit adapts the Kernel's AuditSink contract to the Storage layer
// (Phase 1.6). The Kernel's Emit() signature is unchanged, so this is a
// drop-in replacement for the Phase 0 LogSink — audit remains impossible to
// bypass (it fires from inside the Executor after every run).
package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

// StorageAuditSink persists audit events via the Storage layer.
type StorageAuditSink struct {
	stor   storage.Storage
	logger *slog.Logger
}

// NewStorageAuditSink builds a sink that appends to stor.Audit().
func NewStorageAuditSink(stor storage.Storage, logger *slog.Logger) *StorageAuditSink {
	return &StorageAuditSink{stor: stor, logger: logger}
}

// Emit adapts a core.AuditEvent into a storage.AuditEvent and persists it.
// A persist failure is logged but never breaks the caller's execution — audit
// is best-effort at the sink; the Executor must not fail because storage hiccups.
func (s *StorageAuditSink) Emit(event core.AuditEvent) {
	ts := event.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	result := "success"
	if !event.Result.Success {
		result = "failure"
	}
	detail := ""
	if event.Result.Error != nil {
		detail = event.Result.Error.Error()
	}
	if _, err := s.stor.Audit().Append(storage.AuditEvent{
		Timestamp:             ts,
		Actor:                 event.User.Name,
		Operation:             event.OperationName,
		Action:                "execute",
		Target:                hostTarget(event),
		Result:                result,
		Detail:                detail,
		CapabilityHash:        event.CapabilityHash,
		ExecutionID:           event.ExecutionID,
		SnapshotSchemaVersion: storage.CapabilitySnapshotSchemaVersion,
	}); err != nil && s.logger != nil {
		s.logger.Error("audit persist failed", "operation", event.OperationName, "err", err)
	}
}

// hostTarget renders a short, human-readable target combining the remote host
// (when executed over SSH) and the operation input (e.g. service name).
// Example: "192.168.94.20 :: nginx" or just "nginx" for local runs.
func hostTarget(event core.AuditEvent) string {
	tgt := targetOf(event.Input)
	if event.Target != "" {
		if tgt == "" {
			return event.Target
		}
		return event.Target + " :: " + tgt
	}
	return tgt
}

// targetOf renders the operation input into a short, human-readable target.
// Prefers a "name" key (the common convention); otherwise JSON-encodes the map.
func targetOf(input map[string]any) string {
	if input == nil {
		return ""
	}
	if v, ok := input["name"]; ok {
		return fmt.Sprintf("%v", v)
	}
	b, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(b)
}
