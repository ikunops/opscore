package core

import (
	"log/slog"
	"time"
)

// AuditEvent records a single operation execution for compliance.
type AuditEvent struct {
	TraceID       string
	Timestamp     time.Time
	OperationName string
	User          UserContext
	Host          HostContext
	Target        string // remote host address when executed over SSH; "" if local
	// CapabilityHash is the integrity hash of the capability snapshot that
	// drove this execution (Phase 2.8 / ADR-009). It is a weak reference: the
	// full snapshot is frozen in a CapabilitySnapshotStore keyed by this hash,
	// so each audit event is traceable to the exact capabilities in effect
	// without bloating the (hot, high-frequency) audit row.
	CapabilityHash string
	// ExecutionID correlates this audit event with the ExecutionRecord
	// that drove it (Phase 2.1 / Round 3). Empty for synchronous /
	// ad-hoc runs that go through Dispatcher.Execute directly. It is set
	// by the Executor from ctx.ExecutionID(), so the kernel never
	// reaches into the ExecutionService to stamp it.
	ExecutionID string
	Permission  Permission
	Risk        RiskLevel
	Input       map[string]any
	Result      ExecutionResult
	Duration    time.Duration
}

// AuditSink is the audit output interface.
// Implementations: LogSink (Phase 0), DBSink (Phase 1), KafkaSink (future).
type AuditSink interface {
	Emit(event AuditEvent)
}

// LogSink writes audit events to a slog.Logger.
// This is the Phase 0 default — zero dependencies, zero config.
type LogSink struct {
	logger *slog.Logger
}

func NewLogSink(logger *slog.Logger) *LogSink {
	return &LogSink{logger: logger}
}

func (s *LogSink) Emit(event AuditEvent) {
	s.logger.Info("audit",
		"trace_id", event.TraceID,
		"operation", event.OperationName,
		"user", event.User.Name,
		"host", event.Host.Hostname,
		"resource", event.Permission.ResourceType,
		"action", event.Permission.Action,
		"risk", event.Risk.String(),
		"success", event.Result.Success,
		"capability_hash", event.CapabilityHash,
		"duration_ms", event.Duration.Milliseconds(),
	)
	if !event.Result.Success && event.Result.Error != nil {
		s.logger.Error("audit_failure",
			"trace_id", event.TraceID,
			"operation", event.OperationName,
			"error", event.Result.Error.Error(),
		)
	}
}

// NoopSink discards all audit events. Useful for tests.
type NoopSink struct{}

func NewNoopSink() *NoopSink        { return &NoopSink{} }
func (s *NoopSink) Emit(AuditEvent) {}
