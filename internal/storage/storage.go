package storage

// OperationStore persists Operation metadata.
type OperationStore interface {
	Save(op Operation) (Operation, error)
	GetByName(name string) (Operation, error)
	GetByID(id int64) (Operation, error)
	List() ([]Operation, error)
	ListEnabled() ([]Operation, error)
	SetEnabled(name string, enabled bool) error
	Delete(name string) error
}

// UserStore persists authenticated principals.
type UserStore interface {
	Save(u User) (User, error)
	GetByUsername(username string) (User, error)
	GetByID(id int64) (User, error)
	List() ([]User, error)
	AddRole(userID, roleID int64) error
	RemoveRole(userID, roleID int64) error
	Roles(userID int64) ([]Role, error)
}

// RoleStore persists roles and their operation grants.
type RoleStore interface {
	Save(r Role) (Role, error)
	GetByName(name string) (Role, error)
	GetByID(id int64) (Role, error)
	List() ([]Role, error)
	AddOperation(roleID, opID int64) error
	RemoveOperation(roleID, opID int64) error
	Operations(roleID int64) ([]Operation, error)
}

// AuditStore is the append-only audit log.
type AuditStore interface {
	Append(e AuditEvent) (AuditEvent, error)
	List(limit int) ([]AuditEvent, error)
	ListByOperation(op string, limit int) ([]AuditEvent, error)
	// ListByCorrelation returns audit events sharing a correlationID,
	// most-recent first. It is a QUERY VIEW ONLY — it provides no consistency
	// guarantee against a concurrent mutation (Phase 17.3 replay guard treats
	// its result as a point-in-time hint, not a proof). Additive (ADR-038 §3.1):
	// the existing methods are untouched and no schema migration is required
	// (the correlation_id column already exists in v4).
	ListByCorrelation(correlationID string, limit int) ([]AuditEvent, error)
	// Query is the Phase 18 evidence read (ADR-040 §3.1). It is ADDITIVE: the
	// four methods above and all their callers are untouched.
	//
	// Two properties distinguish it from List:
	//
	//  1. the predicate is applied IN THE STORE, so Limit bounds the rows
	//     RETURNED, not the rows examined — a match in the oldest row is still
	//     found under a small limit (the F-3 defect this method exists to fix);
	//  2. the result carries its own window (AuditPage.Limit/Truncated), so an
	//     empty page is interpretable rather than merely empty.
	//
	// (AuditPage{Events: []}, nil) means "searched, found nothing".
	// A non-nil error means "could not search". The two are NEVER collapsed —
	// that collapse is the false-clean Phase 18 exists to forbid (R18-1).
	Query(q AuditQuery) (AuditPage, error)
}

// ConfigStore is a simple key/value settings store.
type ConfigStore interface {
	Get(key string) (string, bool, error)
	Set(key, value string) error
	List() ([]Config, error)
}

// TaskStore tracks Operation executions and their steps (Task Engine).
type TaskStore interface {
	Save(t Task) (Task, error)
	GetByID(id int64) (Task, error)
	AppendStep(s TaskStep) (TaskStep, error)
	UpdateStep(id int64, status, output string, durationMs int64) error
	Steps(taskID int64) ([]TaskStep, error)
}

// CapabilitySnapshotStore persists observed capability snapshots keyed by their
// content hash (ADR-009 / Phase 2.8). The ExecutionRecord/AuditEvent carry only
// the hash (weak reference, see core ExecutionRecord.CapabilityHash); this store
// holds the full payload so an audit viewer can resolve a hash to the exact
// capability set WITHOUT a strong FK back into the execution tables. Content-
// addressed: Put is idempotent on hash.
type CapabilitySnapshotStore interface {
	// Put stores the payload; if the hash already exists it is a no-op and the
	// existing id is returned. schemaVersion records the payload's envelope
	// version (MUST fix, GPT review) so older snapshots can be migrated.
	// Returns the assigned snapshot id.
	Put(hash string, payload []byte, schemaVersion int) (int64, error)
	// GetByHash returns the payload for a hash. ErrNotFound if absent.
	GetByHash(hash string) ([]byte, error)
	// GetByID returns the payload for a snapshot id. ErrNotFound if absent.
	GetByID(id int64) ([]byte, error)
	// IDForHash returns the snapshot id for a hash, or ErrNotFound if absent.
	IDForHash(hash string) (int64, error)
}

// PluginStore persists plugin lifecycle state (Phase 3.0 / MUST-3) so loaded
// plugins are not lost across a process restart. The Plugin Manager in
// Phase 3.1 is the only writer; readers are the bootstrap loader and Audit.
type PluginStore interface {
	// Upsert inserts or replaces a plugin row by ID (stable name@version).
	Upsert(p Plugin) error
	// Get returns the plugin by ID. ErrNotFound if absent.
	Get(id string) (Plugin, error)
	// List returns all persisted plugins.
	List() ([]Plugin, error)
	// SetEnabled flips the enabled flag (grant/revoke the plugin's ops).
	SetEnabled(id string, enabled bool) error
	// SetStatus moves the plugin to a lifecycle state.
	SetStatus(id string, status string) error
}

// Storage is the root persistence interface (Repository pattern).
// Implementations: MemoryStorage (default), SQLiteStorage (Phase 1.2).
type Storage interface {
	Operations() OperationStore
	Users() UserStore
	Roles() RoleStore
	Audit() AuditStore
	Config() ConfigStore
	Tasks() TaskStore
	Snapshots() CapabilitySnapshotStore
	Plugins() PluginStore
	Close() error
}
