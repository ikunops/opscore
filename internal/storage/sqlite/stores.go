package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// OperationStore
// ---------------------------------------------------------------------------

type opStore struct{ db *sql.DB }

func (s *opStore) Save(op storage.Operation) (storage.Operation, error) {
	_, err := s.db.Exec(
		`INSERT INTO operations(name, resource_type, action_type, risk, source, enabled)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(name) DO UPDATE SET
		   resource_type=excluded.resource_type, action_type=excluded.action_type,
		   risk=excluded.risk, source=excluded.source, enabled=excluded.enabled`,
		op.Name, op.ResourceType, op.ActionType, op.Risk, op.Source, boolToInt(op.Enabled))
	if err != nil {
		return op, err
	}
	return s.GetByName(op.Name)
}

func (s *opStore) GetByName(name string) (storage.Operation, error) {
	row := s.db.QueryRow(
		`SELECT id, name, resource_type, action_type, risk, source, enabled FROM operations WHERE name=?`, name)
	return scanOp(row)
}

func (s *opStore) GetByID(id int64) (storage.Operation, error) {
	row := s.db.QueryRow(
		`SELECT id, name, resource_type, action_type, risk, source, enabled FROM operations WHERE id=?`, id)
	return scanOp(row)
}

func (s *opStore) List() ([]storage.Operation, error) {
	rows, err := s.db.Query(
		`SELECT id, name, resource_type, action_type, risk, source, enabled FROM operations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOps(rows)
}

func (s *opStore) ListEnabled() ([]storage.Operation, error) {
	rows, err := s.db.Query(
		`SELECT id, name, resource_type, action_type, risk, source, enabled FROM operations WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOps(rows)
}

func (s *opStore) SetEnabled(name string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE operations SET enabled=? WHERE name=?`, boolToInt(enabled), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *opStore) Delete(name string) error {
	res, err := s.db.Exec(`DELETE FROM operations WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func scanOp(row *sql.Row) (storage.Operation, error) {
	var op storage.Operation
	var enabled int
	if err := row.Scan(&op.ID, &op.Name, &op.ResourceType, &op.ActionType, &op.Risk, &op.Source, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return op, storage.ErrNotFound
		}
		return op, err
	}
	op.Enabled = enabled != 0
	return op, nil
}

func collectOps(rows *sql.Rows) ([]storage.Operation, error) {
	var out []storage.Operation
	for rows.Next() {
		var op storage.Operation
		var enabled int
		if err := rows.Scan(&op.ID, &op.Name, &op.ResourceType, &op.ActionType, &op.Risk, &op.Source, &enabled); err != nil {
			return nil, err
		}
		op.Enabled = enabled != 0
		out = append(out, op)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// UserStore
// ---------------------------------------------------------------------------

type userStore struct{ db *sql.DB }

func (s *userStore) Save(u storage.User) (storage.User, error) {
	if u.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO users(username, password_hash, created_at) VALUES(?,?,?)`,
			u.Username, u.PasswordHash, nowRFC3339())
		if err != nil {
			return u, err
		}
		u.ID, _ = res.LastInsertId()
		return u, nil
	}
	_, err := s.db.Exec(
		`UPDATE users SET username=?, password_hash=? WHERE id=?`,
		u.Username, u.PasswordHash, u.ID)
	return u, err
}

func (s *userStore) GetByUsername(username string) (storage.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, created_at FROM users WHERE username=?`, username)
	return scanUser(row)
}

func (s *userStore) GetByID(id int64) (storage.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, created_at FROM users WHERE id=?`, id)
	return scanUser(row)
}

func (s *userStore) List() ([]storage.User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.User
	for rows.Next() {
		u, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *userStore) AddRole(userID, roleID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO user_roles(user_id, role_id) VALUES(?,?)`, userID, roleID)
	return err
}

func (s *userStore) RemoveRole(userID, roleID int64) error {
	_, err := s.db.Exec(`DELETE FROM user_roles WHERE user_id=? AND role_id=?`, userID, roleID)
	return err
}

func (s *userStore) Roles(userID int64) ([]storage.Role, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.name, r.description FROM roles r
		 JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id=? ORDER BY r.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Role
	for rows.Next() {
		var r storage.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanUser(row *sql.Row) (storage.User, error) {
	var u storage.User
	var created string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return u, storage.ErrNotFound
		}
		return u, err
	}
	return u, nil
}

func scanUserRows(rows *sql.Rows) (storage.User, error) {
	var u storage.User
	var created string
	if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &created); err != nil {
		return u, err
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// RoleStore
// ---------------------------------------------------------------------------

type roleStore struct{ db *sql.DB }

func (s *roleStore) Save(r storage.Role) (storage.Role, error) {
	if r.ID == 0 {
		if existing, err := s.GetByName(r.Name); err == nil {
			r.ID = existing.ID // upsert by name
		}
	}
	if r.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO roles(name, description) VALUES(?,?)`, r.Name, r.Description)
		if err != nil {
			return r, err
		}
		r.ID, _ = res.LastInsertId()
		return r, nil
	}
	_, err := s.db.Exec(`UPDATE roles SET name=?, description=? WHERE id=?`, r.Name, r.Description, r.ID)
	return r, err
}

func (s *roleStore) GetByName(name string) (storage.Role, error) {
	row := s.db.QueryRow(`SELECT id, name, description FROM roles WHERE name=?`, name)
	return scanRole(row)
}

func (s *roleStore) GetByID(id int64) (storage.Role, error) {
	row := s.db.QueryRow(`SELECT id, name, description FROM roles WHERE id=?`, id)
	return scanRole(row)
}

func (s *roleStore) List() ([]storage.Role, error) {
	rows, err := s.db.Query(`SELECT id, name, description FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Role
	for rows.Next() {
		var r storage.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *roleStore) AddOperation(roleID, opID int64) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO role_operations(role_id, operation_id) VALUES(?,?)`, roleID, opID)
	return err
}

func (s *roleStore) RemoveOperation(roleID, opID int64) error {
	_, err := s.db.Exec(`DELETE FROM role_operations WHERE role_id=? AND operation_id=?`, roleID, opID)
	return err
}

func (s *roleStore) Operations(roleID int64) ([]storage.Operation, error) {
	rows, err := s.db.Query(
		`SELECT o.id, o.name, o.resource_type, o.action_type, o.risk, o.source, o.enabled
		 FROM operations o JOIN role_operations ro ON o.id = ro.operation_id
		 WHERE ro.role_id=? ORDER BY o.id`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOps(rows)
}

func scanRole(row *sql.Row) (storage.Role, error) {
	var r storage.Role
	if err := row.Scan(&r.ID, &r.Name, &r.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, storage.ErrNotFound
		}
		return r, err
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

type auditStore struct{ db *sql.DB }

func (s *auditStore) Append(e storage.AuditEvent) (storage.AuditEvent, error) {
	res, err := s.db.Exec(
		`INSERT INTO audit_events(timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		nowRFC3339(), e.Actor, e.Operation, e.Action, e.Target, e.Result, e.Detail, e.CapabilityHash, e.ExecutionID, e.SnapshotSchemaVersion, e.Revision, e.CorrelationID)
	if err != nil {
		return e, err
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

func (s *auditStore) List(limit int) ([]storage.AuditEvent, error) {
	return s.query(`SELECT id, timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id
		FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
}

func (s *auditStore) ListByOperation(op string, limit int) ([]storage.AuditEvent, error) {
	return s.query(`SELECT id, timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id
		FROM audit_events WHERE operation=? ORDER BY id DESC LIMIT ?`, op, limit)
}

// ListByCorrelation is the Phase 17.3 additive read view (ADR-038 §3.1). The
// correlation_id column already exists in schema v4, so this is a plain query
// with no migration.
func (s *auditStore) ListByCorrelation(correlationID string, limit int) ([]storage.AuditEvent, error) {
	return s.query(`SELECT id, timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id
		FROM audit_events WHERE correlation_id = ? ORDER BY id DESC LIMIT ?`, correlationID, limit)
}

// auditColumns is the single projection shared by every audit read, so the
// SELECT list and the Scan order in query() cannot drift apart.
const auditColumns = `SELECT id, timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id
		FROM audit_events`

// buildAuditQuery renders an AuditQuery into SQL. It is a PURE function —
// no database, no clock, no state — which is what makes ADR-040 §3.4's
// TestAuditQueryUsesBoundParameters an assertion rather than a code review.
//
// Two invariants it exists to make testable:
//
//   - every predicate value travels as a bound `?` argument and NEVER appears
//     inside the SQL text (no fmt.Sprintf, ever);
//   - the bound limit is n+1, so truncation is DETECTED by the same round trip
//     that fetches the page — no second COUNT(*), no guessing.
func buildAuditQuery(q storage.AuditQuery) (string, []any) {
	limit := storage.EffectiveAuditLimit(q.Limit)

	var where []string
	var args []any
	// Order is fixed (target, result, action, correlation) so the generated SQL
	// is deterministic and the prepared-statement cache stays warm.
	if q.Target != "" {
		where = append(where, "target = ?")
		args = append(args, q.Target)
	}
	if q.Result != "" {
		where = append(where, "result = ?")
		args = append(args, q.Result)
	}
	if q.Action != "" {
		where = append(where, "action = ?")
		args = append(args, q.Action)
	}
	if q.CorrelationID != "" {
		where = append(where, "correlation_id = ?")
		args = append(args, q.CorrelationID)
	}
	// Phase 19 additive cursor (ADR-042 §3.2). id < ? is a bound parameter, so
	// it satisfies TestAuditQueryUsesBoundParameters; After==0 emits nothing and
	// leaves every existing caller's SQL byte-for-byte unchanged.
	if q.After != 0 {
		where = append(where, "id < ?")
		args = append(args, q.After)
	}

	var sb strings.Builder
	sb.WriteString(auditColumns)
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	// ORDER BY id DESC matches List: id is a monotonic INTEGER PRIMARY KEY, so
	// the order is total and stable even for same-timestamp rows.
	sb.WriteString(" ORDER BY id DESC LIMIT ?")
	args = append(args, limit+1)

	return sb.String(), args
}

// Query is the Phase 18 evidence read (ADR-040 §3.1). The predicate is pushed
// into the statement, so the limit bounds rows RETURNED — a match in the oldest
// row is still found under a small limit.
func (s *auditStore) Query(q storage.AuditQuery) (storage.AuditPage, error) {
	limit := storage.EffectiveAuditLimit(q.Limit)
	sqlText, args := buildAuditQuery(q)
	rows, err := s.query(sqlText, args...)
	if err != nil {
		// "Could not search" is NOT "searched, found nothing" (R18-1). The
		// zero page is returned only alongside a non-nil error, and every
		// caller is forced by the signature to look at it.
		return storage.AuditPage{}, err
	}
	truncated := false
	if len(rows) > limit {
		// The n+1 probe came back: drop it and declare the window.
		rows = rows[:limit]
		truncated = true
	}
	if rows == nil {
		rows = []storage.AuditEvent{}
	}
	return storage.AuditPage{Events: rows, Limit: limit, Truncated: truncated}, nil
}

func (s *auditStore) query(q string, args ...interface{}) ([]storage.AuditEvent, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.AuditEvent
	for rows.Next() {
		var e storage.AuditEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Operation, &e.Action, &e.Target, &e.Result, &e.Detail, &e.CapabilityHash, &e.ExecutionID, &e.SnapshotSchemaVersion, &e.Revision, &e.CorrelationID); err != nil {
			return nil, err
		}
		// R79-B: the timestamp column is read into ts above; it MUST be parsed back
		// into e.Timestamp or every List/ListByOperation read-back yields a zero time.
		parsed, perr := time.Parse(time.RFC3339, ts)
		if perr != nil {
			return nil, fmt.Errorf("audit_events: parse timestamp %q: %w", ts, perr)
		}
		e.Timestamp = parsed
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// ConfigStore
// ---------------------------------------------------------------------------

type configStore struct{ db *sql.DB }

func (s *configStore) Get(key string) (string, bool, error) {
	row := s.db.QueryRow(`SELECT value FROM config WHERE key=?`, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

func (s *configStore) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO config(key, value, updated_at) VALUES(?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, nowRFC3339())
	return err
}

func (s *configStore) List() ([]storage.Config, error) {
	rows, err := s.db.Query(`SELECT key, value, updated_at FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Config
	for rows.Next() {
		var c storage.Config
		if err := rows.Scan(&c.Key, &c.Value, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// TaskStore
// ---------------------------------------------------------------------------

type taskStore struct{ db *sql.DB }

func (s *taskStore) Save(t storage.Task) (storage.Task, error) {
	if t.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO tasks(operation, status, created_at) VALUES(?,?,?)`,
			t.Operation, t.Status, nowRFC3339())
		if err != nil {
			return t, err
		}
		t.ID, _ = res.LastInsertId()
		return t, nil
	}
	_, err := s.db.Exec(`UPDATE tasks SET operation=?, status=? WHERE id=?`, t.Operation, t.Status, t.ID)
	return t, err
}

func (s *taskStore) GetByID(id int64) (storage.Task, error) {
	row := s.db.QueryRow(`SELECT id, operation, status, created_at FROM tasks WHERE id=?`, id)
	var t storage.Task
	var created string
	if err := row.Scan(&t.ID, &t.Operation, &t.Status, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, storage.ErrNotFound
		}
		return t, err
	}
	return t, nil
}

func (s *taskStore) AppendStep(step storage.TaskStep) (storage.TaskStep, error) {
	res, err := s.db.Exec(
		`INSERT INTO task_steps(task_id, step_name, command, status) VALUES(?,?,?,?)`,
		step.TaskID, step.StepName, step.Command, step.Status)
	if err != nil {
		return step, err
	}
	step.ID, _ = res.LastInsertId()
	return step, nil
}

func (s *taskStore) UpdateStep(id int64, status, output string, durationMs int64) error {
	res, err := s.db.Exec(
		`UPDATE task_steps SET status=?, output=?, duration_ms=? WHERE id=?`,
		status, output, durationMs, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *taskStore) Steps(taskID int64) ([]storage.TaskStep, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, step_name, command, status, output, duration_ms FROM task_steps WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.TaskStep
	for rows.Next() {
		var st storage.TaskStep
		if err := rows.Scan(&st.ID, &st.TaskID, &st.StepName, &st.Command, &st.Status, &st.Output, &st.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// CapabilitySnapshotStore (content-addressed by hash)
// ---------------------------------------------------------------------------

type snapshotStore struct{ db *sql.DB }

// Put upserts on hash (UNIQUE). If the hash already exists the row is left
// untouched and its id is returned — content-addressed idempotency.
func (s *snapshotStore) Put(hash string, payload []byte, schemaVersion int) (int64, error) {
	if _, err := s.db.Exec(
		`INSERT INTO capability_snapshots(hash, payload, schema_version, created_at) VALUES(?,?,?,?)
		 ON CONFLICT(hash) DO NOTHING`, hash, payload, schemaVersion, nowRFC3339()); err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM capability_snapshots WHERE hash=?`, hash).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *snapshotStore) GetByHash(hash string) ([]byte, error) {
	var p []byte
	err := s.db.QueryRow(`SELECT payload FROM capability_snapshots WHERE hash=?`, hash).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *snapshotStore) GetByID(id int64) ([]byte, error) {
	var p []byte
	err := s.db.QueryRow(`SELECT payload FROM capability_snapshots WHERE id=?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *snapshotStore) IDForHash(hash string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM capability_snapshots WHERE hash=?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, storage.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}
