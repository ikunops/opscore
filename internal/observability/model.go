// Package observability is the Phase 8.1 Pilot Capability (ADR-015): a
// peripheral READ MODEL over the frozen Runtime Core, Plugin Ecosystem, Cluster
// and Governance systems. It is the first of the four Platform Operations
// capabilities to be implemented, chosen as the Reference Capability because
// it is the purest (Read Model, no verdict, no coordination, no execution).
//
// Design invariant (ADR-015 MUST-1..5):
//   - READ ONLY: it consumes events already emitted by frozen subsystems; it
//     never opens an execution path, never calls back into Runtime Core.
//   - FROZEN CONTRACT: it adds no Runtime Contract type, changes no interface.
//   - COMPOSED, NOT INTRUSIVE: it provides adapter sinks that the host wires
//     into existing buses/sink interfaces. It injects NO instrumentation code
//     into Execution / Manager / Loader / Provider / Manifest / isolation.
//   - EXISTING IDs ONLY: every Observation carries IDs that already exist in
//     the frozen systems (TraceID / ExecutionID / RequestID / PluginID / AuditID).
//     It introduces no new identity system.
//   - NO RUNTIME DEPENDENCY: it must never import internal/plugin/runtime (the
//     execution engine) nor internal/plugin/isolation (process execution). The
//     AST guard in observability_test.go enforces this mechanically.
//
// Architecture (ADR-015 §3):
//
//	Execution → Event → Collector → Metrics → Dashboard API
//
// The Collector is the single ingestion point; adapter sinks translate the
// four existing event types (execution.ExecutionEvent, sandbox.Decision,
// manifest.SignatureResult, core.AuditEvent) into the unified Observation
// read-model record.
package observability

import "time"

// Source identifies which frozen subsystem produced the underlying event an
// Observation was derived from. Observability only OBSERVES; it never emits.
type Source string

const (
	// SourceExecution is execution.ExecutionEvent (lifecycle: created/started/
	// step/finished/cancelled) published on the existing execution.EventBus.
	SourceExecution Source = "execution"
	// SourceSandbox is sandbox.Decision (Phase 6.1 envelope verdict).
	SourceSandbox Source = "sandbox"
	// SourceSignature is manifest.SignatureResult (Phase 5 trust verdict).
	SourceSignature Source = "signature"
	// SourceAudit is core.AuditEvent (compliance correlation: ties an execution
	// to its user/host/risk/duration for the audit read view).
	SourceAudit Source = "audit"
)

// Kind classifies the observation within the unified model
// (Metrics / Logs / Traces / Audit Correlation — ADR-015 §1).
type Kind string

const (
	KindMetric           Kind = "metric"
	KindLog              Kind = "log"
	KindTrace            Kind = "trace"
	KindAuditCorrelation Kind = "audit-correlation"
)

// SchemaVersion is the read-model schema version of an Observation. It is a
// OBSERVABILITY-LOCAL schema marker (Round 44 SHOULD-1) — explicitly NOT a
// Runtime Contract. Future Dashboard / Exporter / Storage backends may depend
// on these fields, so the version is frozen here from day one and stamped on
// every record so downstream consumers can branch on shape without guessing.
const SchemaVersion = "observability/v1"

// Observation is the unified, read-only projection of ONE event from a frozen
// subsystem. It carries ONLY existing IDs (ADR-015 MUST-4) — it introduces no
// new identity system. ObsID is an opaque, observability-local handle used only
// to key the in-memory store; it is derived, never a source of truth.
type Observation struct {
	// SchemaVersion stamps the read-model shape this record conforms to. It is
	// set by the Collector at ingest and is an observability-local contract
	// only — it never travels into the frozen Runtime/ecosystem systems.
	SchemaVersion string

	// ObsID is an opaque, observability-local handle for dedup/ordering inside
	// this read model. It is NOT a new identity system — it is a random handle
	// stamped at ingest, used only to key the store.
	ObsID string

	Timestamp time.Time
	Source    Source
	Kind      Kind

	// Existing IDs only (ADR-015 MUST-4). Every field below is copied verbatim
	// from a frozen subsystem's event; observability owns none of them.
	TraceID     string
	ExecutionID string
	RequestID   string
	PluginID    string // derived from Source ("plugin:<name>") / SignerID; never invented
	AuditID     string

	// Read-only projection of the source payload.
	Operation  string
	Status     string // lifecycle status / verdict string
	Code       string // machine code: DecisionCode / SignatureResult.Code / execution.Status
	Verdict    string // allow | deny | verified | unverified | success | failed
	Reason     string
	Risk       string
	DurationMs int64
}
