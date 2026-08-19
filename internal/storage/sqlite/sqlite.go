// Package sqlite implements storage.Storage on top of a SQLite database
// (pure-Go driver modernc.org/sqlite, no cgo — keeps the single binary).
//
// It is one concrete implementation of the Repository interfaces defined in
// the parent storage package; swapping MemoryStorage for SQLiteStorage (or a
// future Postgres) requires no caller changes.
package sqlite

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// Schema is the idempotent DDL run on open (CREATE TABLE IF NOT EXISTS).
const Schema = `
CREATE TABLE IF NOT EXISTS operations (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,
  resource_type TEXT NOT NULL,
  action_type   TEXT NOT NULL,
  risk          TEXT NOT NULL,
  source        TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS roles (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS user_roles (
  user_id INTEGER NOT NULL,
  role_id INTEGER NOT NULL,
  PRIMARY KEY (user_id, role_id),
  FOREIGN KEY (user_id) REFERENCES users(id),
  FOREIGN KEY (role_id) REFERENCES roles(id)
);
CREATE TABLE IF NOT EXISTS role_operations (
  role_id      INTEGER NOT NULL,
  operation_id INTEGER NOT NULL,
  PRIMARY KEY (role_id, operation_id),
  FOREIGN KEY (role_id) REFERENCES roles(id),
  FOREIGN KEY (operation_id) REFERENCES operations(id)
);
CREATE TABLE IF NOT EXISTS audit_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp TEXT NOT NULL,
  actor     TEXT NOT NULL,
  operation TEXT NOT NULL,
  action    TEXT NOT NULL,
  target    TEXT NOT NULL DEFAULT '',
  result    TEXT NOT NULL,
  detail    TEXT NOT NULL DEFAULT '',
  capability_hash TEXT NOT NULL DEFAULT '',
  execution_id TEXT NOT NULL DEFAULT '',
  snapshot_schema_version INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS config (
  key       TEXT PRIMARY KEY,
  value     TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  operation  TEXT NOT NULL,
  status     TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_steps (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id    INTEGER NOT NULL,
  step_name  TEXT NOT NULL,
  command    TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL,
  output     TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (task_id) REFERENCES tasks(id)
);
CREATE TABLE IF NOT EXISTS capability_snapshots (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  hash          TEXT NOT NULL UNIQUE,
  payload       BLOB NOT NULL,
  schema_version INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL
);
-- Phase 2.1.2/2.1.3: durable execution lifecycle records. Steps are stored
-- inline as a JSON array (the model keeps Steps []ExecutionStepRecord), so a
-- single table backs both the Recorder writes and the Execution API reads.
-- Reuses the same *sql.DB as the rest of storage (single binary, single file).
CREATE TABLE IF NOT EXISTS execution_records (
  id              TEXT PRIMARY KEY,
  operation       TEXT NOT NULL,
  permission      TEXT NOT NULL DEFAULT '',
  risk            TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'CREATED',
  user_id         TEXT NOT NULL DEFAULT '',
  user_name       TEXT NOT NULL DEFAULT '',
  target          TEXT NOT NULL DEFAULT '',
  trace_id        TEXT NOT NULL DEFAULT '',
  capability_hash TEXT NOT NULL DEFAULT '',
  version         INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  finished_at     TEXT,
  duration_ms     INTEGER NOT NULL DEFAULT 0,
  error           TEXT NOT NULL DEFAULT '',
  steps           TEXT NOT NULL DEFAULT '[]'
);
`

// SQLiteStorage is the SQLite-backed storage.Storage implementation.
type SQLiteStorage struct {
	db *sql.DB

	op     *opStore
	user   *userStore
	role   *roleStore
	audit  *auditStore
	cfg    *configStore
	task   *taskStore
	snap   *snapshotStore
	plugin *pluginStore
}

// NewSQLiteStorage opens (or creates) the database at path and brings the
// schema up to the latest migration version (idempotent — safe on every
// open). Use path=":memory:" for an ephemeral database.
func NewSQLiteStorage(path string) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := Ensure(db, Migrations); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLiteStorage{db: db}
	s.op = &opStore{db: db}
	s.user = &userStore{db: db}
	s.role = &roleStore{db: db}
	s.audit = &auditStore{db: db}
	s.cfg = &configStore{db: db}
	s.task = &taskStore{db: db}
	s.snap = &snapshotStore{db: db}
	s.plugin = &pluginStore{db: db}
	return s, nil
}

func (s *SQLiteStorage) Operations() storage.OperationStore         { return s.op }
func (s *SQLiteStorage) Users() storage.UserStore                   { return s.user }
func (s *SQLiteStorage) Roles() storage.RoleStore                   { return s.role }
func (s *SQLiteStorage) Audit() storage.AuditStore                  { return s.audit }
func (s *SQLiteStorage) Config() storage.ConfigStore                { return s.cfg }
func (s *SQLiteStorage) Tasks() storage.TaskStore                   { return s.task }
func (s *SQLiteStorage) Snapshots() storage.CapabilitySnapshotStore { return s.snap }
func (s *SQLiteStorage) Plugins() storage.PluginStore               { return s.plugin }
func (s *SQLiteStorage) Close() error                               { return s.db.Close() }

// DB exposes the underlying *sql.DB so other store backends that live in this
// package (e.g. the ExecutionStore) can share the same connection / file as the
// rest of storage — keeping the single-binary, single-file deployment story.
func (s *SQLiteStorage) DB() *sql.DB { return s.db }

func nowRFC3339() string { return time.Now().Format(time.RFC3339) }
