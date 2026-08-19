package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// ExecutionStore is the SQLite-backed implementation of execution.Store. It
// satisfies both the Recorder (Executor writes) and the read side (Execution
// API) through one type, mirroring the contract of execution.MemoryStore so
// swapping the backend at the composition root is a one-line change.
//
// Steps are persisted inline as a JSON array in the `steps` column, matching
// the model's `Steps []ExecutionStepRecord` shape — no separate step table, no
// JSON blob of the whole record.
type ExecutionStore struct {
	db *sql.DB
}

// NewExecutionStore builds an ExecutionStore on top of an already-open *sql.DB.
// The schema (execution_records table) is expected to have been applied by
// NewSQLiteStorage; if you open the DB yourself, run Schema first.
func NewExecutionStore(db *sql.DB) *ExecutionStore {
	return &ExecutionStore{db: db}
}

func (s *ExecutionStore) Create(rec execution.ExecutionRecord) error {
	now := time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.Status == execution.StatusRunning && rec.StartedAt == nil {
		st := rec.CreatedAt
		rec.StartedAt = &st
	}
	stepsJSON, err := json.Marshal(rec.Steps)
	if err != nil {
		return fmt.Errorf("execution store: marshal steps: %w", err)
	}
	ver := rec.Version
	if ver == 0 {
		ver = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO execution_records
		 (id, operation, permission, risk, status, user_id, user_name, target,
		  trace_id, capability_hash, version, source, origin,
		  created_at, started_at, finished_at, duration_ms, error, steps)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Operation, rec.Permission, rec.Risk, string(rec.Status),
		rec.UserID, rec.UserName, rec.Target, rec.TraceID, rec.CapabilityHash, ver,
		rec.Source, rec.Origin,
		rec.CreatedAt.Format(time.RFC3339), optTime(rec.StartedAt), optTime(rec.FinishedAt),
		rec.DurationMs, rec.Error, string(stepsJSON))
	if err != nil {
		return fmt.Errorf("execution store: create: %w", err)
	}
	return nil
}

func (s *ExecutionStore) UpdateStatus(id string, status execution.Status) error {
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	now := time.Now()
	switch status {
	case execution.StatusRunning:
		if cur.StartedAt == nil {
			cur.StartedAt = &now
		}
	case execution.StatusSuccess, execution.StatusFailed, execution.StatusCancelled:
		if cur.FinishedAt == nil {
			cur.FinishedAt = &now
		}
	}
	cur.Status = status
	cur.Version++
	_, err = s.db.Exec(
		`UPDATE execution_records SET status=?, started_at=?, finished_at=?, version=? WHERE id=?`,
		string(cur.Status), optTime(cur.StartedAt), optTime(cur.FinishedAt), cur.Version, id)
	if err != nil {
		return fmt.Errorf("execution store: update status: %w", err)
	}
	return nil
}

// Transition atomically moves id from 'from' to 'to' only when the
// record is currently in 'from' (and its version is unchanged since we
// read it). A 0-rows UPDATE means a concurrent transition already
// changed the status (or version), so we return ErrConflict rather than
// overwrite it. This is the durable half of the S3 CAS lifecycle.
func (s *ExecutionStore) Transition(id string, from, to execution.Status) error {
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	now := time.Now()
	switch to {
	case execution.StatusRunning:
		if cur.StartedAt == nil {
			cur.StartedAt = &now
		}
	case execution.StatusSuccess, execution.StatusFailed, execution.StatusCancelled:
		if cur.FinishedAt == nil {
			cur.FinishedAt = &now
		}
	}
	res, err := s.db.Exec(
		`UPDATE execution_records
		    SET status=?, started_at=?, finished_at=?, version=version+1
		    WHERE id=? AND status=? AND version=?`,
		string(to), optTime(cur.StartedAt), optTime(cur.FinishedAt),
		id, string(from), cur.Version)
	if err != nil {
		return fmt.Errorf("execution store: transition: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return execution.ErrConflict
	}
	return nil
}

func (s *ExecutionStore) UpdateStep(id string, step execution.ExecutionStepRecord) error {
	rec, err := s.Get(id)
	if err != nil {
		return err
	}
	found := false
	for i := range rec.Steps {
		if rec.Steps[i].ID == step.ID {
			rec.Steps[i] = step
			found = true
			break
		}
	}
	if !found {
		rec.Steps = append(rec.Steps, step)
	}
	stepsJSON, err := json.Marshal(rec.Steps)
	if err != nil {
		return fmt.Errorf("execution store: marshal steps: %w", err)
	}
	_, err = s.db.Exec(`UPDATE execution_records SET steps=? WHERE id=?`, string(stepsJSON), id)
	if err != nil {
		return fmt.Errorf("execution store: update step: %w", err)
	}
	return nil
}

func (s *ExecutionStore) Get(id string) (*execution.ExecutionRecord, error) {
	row := s.db.QueryRow(
		`SELECT id, operation, permission, risk, status, user_id, user_name, target,
		        trace_id, capability_hash, version, source, origin,
		        created_at, started_at, finished_at, duration_ms, error, steps
		 FROM execution_records WHERE id=?`, id)
	rec, err := scanExecution(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, execution.ErrNotFound
		}
		return nil, err
	}
	return rec, nil
}

func (s *ExecutionStore) List(q execution.Query) ([]execution.ExecutionRecord, error) {
	where := ""
	args := []interface{}{}
	if q.Operation != "" {
		where += " AND operation=?"
		args = append(args, q.Operation)
	}
	if q.Status != "" {
		where += " AND status=?"
		args = append(args, string(q.Status))
	}
	rows, err := s.db.Query(
		`SELECT id, operation, permission, risk, status, user_id, user_name, target,
		        trace_id, capability_hash, version, source, origin,
		        created_at, started_at, finished_at, duration_ms, error, steps
		 FROM execution_records WHERE 1=1`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, clampLimit(q.Limit), q.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("execution store: list: %w", err)
	}
	defer rows.Close()
	out := []execution.ExecutionRecord{}
	for rows.Next() {
		rec, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// scanExecution reads one execution record row (from *sql.Row or *sql.Rows).
func scanExecution(scanner interface {
	Scan(dest ...interface{}) error
}) (*execution.ExecutionRecord, error) {
	var (
		id, op, perm, risk, status, uid, uname, target, traceID, capHash, createdAt,
		errMsg, stepsJSON, source, origin string
		startedAt, finishedAt sql.NullString
		version, durationMs   int64
	)
	if err := scanner.Scan(&id, &op, &perm, &risk, &status, &uid, &uname, &target,
		&traceID, &capHash, &version, &source, &origin,
		&createdAt, &startedAt, &finishedAt, &durationMs, &errMsg, &stepsJSON); err != nil {
		return nil, err
	}
	rec := &execution.ExecutionRecord{
		ID:             id,
		Operation:      op,
		Permission:     perm,
		Risk:           risk,
		Status:         execution.Status(status),
		UserID:         uid,
		UserName:       uname,
		Target:         target,
		TraceID:        traceID,
		CapabilityHash: capHash,
		Source:         source,
		Origin:         origin,
		Version:        uint64(version),
		DurationMs:     durationMs,
		Error:          errMsg,
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		rec.CreatedAt = t
	}
	if startedAt.Valid && startedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, startedAt.String); err == nil {
			rec.StartedAt = &t
		}
	}
	if finishedAt.Valid && finishedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, finishedAt.String); err == nil {
			rec.FinishedAt = &t
		}
	}
	if stepsJSON != "" {
		var steps []execution.ExecutionStepRecord
		if err := json.Unmarshal([]byte(stepsJSON), &steps); err == nil {
			rec.Steps = steps
		}
	}
	return rec, nil
}

func optTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

// clampLimit mirrors the in-memory store's behaviour: a non-positive Limit
// (zero/negative) means "no limit", which we translate to a large cap so the
// SQL LIMIT clause still binds a value.
func clampLimit(n int) int {
	if n <= 0 {
		return 1 << 30
	}
	return n
}
