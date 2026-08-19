// Package storage defines the persistence abstraction (Repository pattern) for
// OpsCore's Control Plane (Phase 1).
//
// Design notes (8-bit / ADR-004):
//   - Storage is an interface, never *sql.DB. Default implementation is
//     MemoryStorage; SQLiteStorage (Phase 1.2) and a future Postgres backend
//     implement the same interfaces without touching callers.
//   - The Operation Registry (in core) is the runtime source of truth for
//     capabilities. This layer is its durable projection: Code Owns Capability,
//     Database Owns Assignment. We persist metadata + authorization, never logic.
package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// Operation is the persisted projection of a registered Operation's metadata.
type Operation struct {
	ID           int64
	Name         string // globally unique, e.g. "system.service.restart"
	ResourceType string // e.g. "service"
	ActionType   string // e.g. "restart"
	Risk         string // "low" | "medium" | "high"
	Source       string // "builtin" | "plugin:<name>"
	Enabled      bool
}

// Plugin lifecycle states (Phase 3.0 / MUST-3). Mirrors the state machine
// GPT Round 6 froze: Discovered -> Loaded -> Registered -> Enabled ->
// Disabled -> Unloaded. Disabled is the only legal predecessor of Unloaded.
const (
	PluginDiscovered = "discovered"
	PluginLoaded     = "loaded"
	PluginRegistered = "registered"
	PluginEnabled    = "enabled"
	PluginDisabled   = "disabled"
	PluginUnloaded   = "unloaded"
	// PluginRejected is a STORAGE-ONLY status: a plugin the Compatibility Gate
	// refused before Load (Phase 3.5). It never enters the runtime lifecycle
	// state machine (Phase 3.4.1), but is persisted so operators can SEE why a
	// plugin is absent, and is paired with a PluginLoadRejected audit event.
	PluginRejected = "rejected"
)

// Plugin is the durable projection of a loaded plugin's lifecycle state.
// Persisted so plugin state survives a process restart (MUST-3).
type Plugin struct {
	// ID is the STABLE plugin identity, e.g. "mysql@1.0.0" (Phase 3.4.1:
	// Plugin Identity Migration). Unlike Name (a display name that may later
	// be renamed), the ID pins name@version so Enable/Disable/Unload and
	// Audit always reference the exact loaded artifact. It is the UNIQUE key
	// of the plugin_registry row.
	ID string
	// Name is the display name, e.g. "mysql". May repeat across versions.
	Name     string
	Version  string    // semantic version declared in the manifest
	Status   string    // one of the Plugin* constants above
	Enabled  bool      // whether the plugin's operations are currently granted
	LoadedAt time.Time // when it was last loaded
}

// User is an authenticated principal.
type User struct {
	ID           int64
	Username     string
	PasswordHash string // bcrypt hash (set in Phase 1.4)
	CreatedAt    time.Time
}

// Role groups authorization grants.
type Role struct {
	ID          int64
	Name        string
	Description string
}

// AuditEvent is one row in the append-only audit log.
type AuditEvent struct {
	ID        int64
	Timestamp time.Time
	Actor     string // username, or "system"
	Operation string // operation name
	Action    string // "execute" | "plan" | "policy.<verb>" (Phase 17)
	Target    string // operation-specific target, e.g. service name
	// Result is the outcome class. The original pair is "success" | "failure";
	// Phase 17 Management adds exactly ONE value, whose meaning is FROZEN by
	// ADR-036 §3.3.2.1:
	//
	//	"intent"  — written BEFORE the mutation is attempted. It records what
	//	            was asked for, never what happened. An intent row with no
	//	            following outcome row means the process died mid-mutation;
	//	            that gap IS the signal, so an intent row must never be
	//	            rewritten or deleted after the fact.
	//
	// A compare-and-swap refusal is deliberately NOT a fourth value: the frozen
	// table records it as "failure", with the conflict as the failure REASON in
	// Detail and the actually-stored revision in Revision. Minting a "conflict"
	// class would widen the audit vocabulary beyond what R76/R77 signed, and
	// would silently break every existing reader that treats "not success" as
	// failure.
	//
	// The log is append-only: one attempt emits intent THEN outcome as two
	// separate rows. Collapsing them into a single "final" row would erase the
	// crash window the pair exists to expose.
	Result string // "success" | "failure" | "intent"
	Detail string
	// CapabilityHash is the weak reference (ADR-009 / Phase 2.8) to the
	// content-addressed CapabilitySnapshotStore. It lets an audit viewer
	// resolve the exact capability set in effect at execution time without
	// bloating the (hot, high-frequency) audit row. Empty for events emitted
	// before snapshotting existed, or for runs without an observed snapshot.
	CapabilityHash string
	// ExecutionID correlates the audit row with the ExecutionRecord that
	// drove it (Phase 2.1 / Round 3). Empty for synchronous / ad-hoc
	// runs. It travels WITH the audit row so a replay viewer needs no
	// join on the (separately stored) execution table.
	ExecutionID string
	// SnapshotSchemaVersion is the schema version of the SnapshotEnvelope that
	// CapabilityHash points at (SHOULD polish, GPT review). It travels WITH the
	// audit row so a replay/audit viewer never has to resolve the snapshot just
	// to learn "which schema version was in effect" — it can decide migration
	// strategy directly from the audit event. Mirrors CapabilitySnapshotSchemaVersion
	// at emit time. 0 when the referenced snapshot predates versioning.
	SnapshotSchemaVersion int
	// Revision is the policy revision this row is about (Phase 17 / ADR-036
	// §3.5, OQ-17.1-B). Which revision depends on Result, and the difference
	// is the whole point:
	//   intent  -> the EXPECTED revision the caller supplied via If-Match;
	//   success -> the COMMITTED revision the CAS produced;
	//   failure -> the best obtainable revision; for a CAS refusal that is the
	//              ACTUAL stored revision, which is exactly what the caller
	//              needs in order to refetch and retry.
	// Reading intent+outcome together therefore reconstructs the full CAS
	// decision from the log alone, with no access to the live store. 0 for
	// every non-Phase-17 event.
	Revision int
	// CorrelationID ties every row emitted while serving ONE request together
	// (Phase 17 / ADR-036 §3.5). Without it the intent row and its outcome row
	// are only heuristically linked — by adjacency and timestamp — which stops
	// being true the moment two operators mutate concurrently. Empty for
	// events not emitted by a correlated request.
	CorrelationID string
}

// Config is a simple key/value settings row.
type Config struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Task is one Operation execution tracked by the Task Engine (Phase 1.6).
type Task struct {
	ID        int64
	Operation string
	Status    string // "running" | "success" | "failure"
	CreatedAt time.Time
}

// TaskStep is one step within a Task.
type TaskStep struct {
	ID         int64
	TaskID     int64
	StepName   string
	Command    string
	Status     string // "pending" | "running" | "success" | "failure"
	Output     string
	DurationMs int64
}

// CapabilitySnapshot is one persisted capability observation: the serialized
// core/snapshot.CapabilitySnapshot payload, content-addressed by Hash.
type CapabilitySnapshot struct {
	ID            int64
	Hash          string
	SchemaVersion int
	Payload       []byte
	CreatedAt     time.Time
}

// CapabilitySnapshotSchemaVersion is the current schema version of the persisted
// capability snapshot payload (MUST fix, GPT review). Bump it when the shape of
// core/snapshot.CapabilitySnapshot changes so older payloads can be detected and
// migrated; the store holds JSON (never an opaque BLOB), wrapped in an envelope
// carrying this version.
const CapabilitySnapshotSchemaVersion = 1

// SnapshotEnvelope wraps a persisted capability snapshot payload with an
// explicit schema version. The store holds this envelope as JSON (MUST fix, GPT
// review): an audit viewer reads SchemaVersion first and can migrate older
// payloads instead of guessing by hash alone.
type SnapshotEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

// PersistCapabilitySnapshot stores a snapshot content-addressed by its hash and
// returns the assigned snapshot id. It is the CONTROL-PLANE integration point:
// call it from the boundary that performs discovery/enrichment (never from core)
// so the kernel stays free of storage. Idempotent on hash — re-persisting the
// same capability set reuses the existing row. The payload is wrapped in a
// versioned JSON envelope, never stored as a bare opaque BLOB.
func PersistCapabilitySnapshot(snap *snapshot.CapabilitySnapshot, store CapabilitySnapshotStore) (int64, error) {
	if snap == nil {
		return 0, fmt.Errorf("persist capability snapshot: nil snapshot")
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("persist capability snapshot: %w", err)
	}
	env := SnapshotEnvelope{SchemaVersion: CapabilitySnapshotSchemaVersion, Payload: raw}
	payload, err := json.Marshal(env)
	if err != nil {
		return 0, fmt.Errorf("persist capability snapshot: %w", err)
	}
	return store.Put(snap.Hash(), payload, CapabilitySnapshotSchemaVersion)
}
