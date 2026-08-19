package observability

import (
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
)

// This file provides the adapter sinks that let the host wire Observability
// into the EXISTING event buses / sink interfaces WITHOUT any change to those
// frozen subsystems (ADR-015 MUST-3: data all from existing systems, no new
// execution protocol). Observability adapts; it does not modify.

// ExecutionBus adapts the Collector to execution.EventBus so it can be
// subscribed to the existing multi-subscriber bus (server.ExecutionEventBus)
// or used directly. It forwards every published ExecutionEvent to the collector
// and owns no bus state of its own.
type ExecutionBus struct{ c *Collector }

// NewExecutionBus builds an adapter over c.
func NewExecutionBus(c *Collector) *ExecutionBus { return &ExecutionBus{c: c} }

// Publish implements execution.EventBus.
func (b *ExecutionBus) Publish(ev execution.ExecutionEvent) { b.c.ObserveExecution(ev) }

// SandboxSink returns a sandbox.AuditSink that forwards every Decision to the
// collector. Wire it via envelope.WithAudit(observability.SandboxSink(col)).
func SandboxSink(c *Collector) sandbox.AuditSink {
	return func(d sandbox.Decision) { c.ObserveSandbox(d) }
}

// SignatureSink returns a manifest.AuditSink that forwards every
// SignatureResult to the collector. Wire it via
// manifest.NewSignatureVerifier(policy, observability.SignatureSink(col)).
func SignatureSink(c *Collector) manifest.AuditSink {
	return func(r manifest.SignatureResult) { c.ObserveSignature(r) }
}

// AuditSinkAdapter adapts the Collector to core.AuditSink so it can be wired
// as a standard audit sink alongside the existing LogSink / DBSink.
type AuditSinkAdapter struct{ c *Collector }

// NewAuditSinkAdapter builds an adapter over c.
func NewAuditSinkAdapter(c *Collector) *AuditSinkAdapter { return &AuditSinkAdapter{c: c} }

// Emit implements core.AuditSink.
func (a *AuditSinkAdapter) Emit(ev core.AuditEvent) { a.c.ObserveAudit(ev) }
