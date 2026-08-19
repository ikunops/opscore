package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// pluginStore persists plugin lifecycle state (Phase 3.0 / MUST-3) in the
// plugin_registry table, created by migration v2. It is the durable half of
// "a loaded plugin survives a process restart".
type pluginStore struct{ db *sql.DB }

func (s *pluginStore) Upsert(p storage.Plugin) error {
	_, err := s.db.Exec(
		`INSERT INTO plugin_registry(id, name, version, status, enabled, loaded_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, version=excluded.version, status=excluded.status,
		   enabled=excluded.enabled, loaded_at=excluded.loaded_at`,
		p.ID, p.Name, p.Version, p.Status, boolToInt(p.Enabled), p.LoadedAt.Format("2006-01-02T15:04:05Z07:00"))
	if err != nil {
		return fmt.Errorf("plugin store: upsert: %w", err)
	}
	return nil
}

func (s *pluginStore) Get(id string) (storage.Plugin, error) {
	row := s.db.QueryRow(
		`SELECT id, name, version, status, enabled, loaded_at FROM plugin_registry WHERE id=?`, id)
	return scanPlugin(row)
}

func (s *pluginStore) List() ([]storage.Plugin, error) {
	rows, err := s.db.Query(
		`SELECT id, name, version, status, enabled, loaded_at FROM plugin_registry ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("plugin store: list: %w", err)
	}
	defer rows.Close()
	out := []storage.Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *pluginStore) SetEnabled(id string, enabled bool) error {
	res, err := s.db.Exec(
		`UPDATE plugin_registry SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	if err != nil {
		return fmt.Errorf("plugin store: set enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *pluginStore) SetStatus(id string, status string) error {
	res, err := s.db.Exec(
		`UPDATE plugin_registry SET status=? WHERE id=?`, status, id)
	if err != nil {
		return fmt.Errorf("plugin store: set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// scanPlugin reads one plugin row from *sql.Row or *sql.Rows.
func scanPlugin(scanner interface {
	Scan(dest ...interface{}) error
}) (storage.Plugin, error) {
	var (
		id, name, version, status, loadedAt string
		enabled                             int
	)
	if err := scanner.Scan(&id, &name, &version, &status, &enabled, &loadedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Plugin{}, storage.ErrNotFound
		}
		return storage.Plugin{}, err
	}
	p := storage.Plugin{
		ID:      id,
		Name:    name,
		Version: version,
		Status:  status,
		Enabled: enabled != 0,
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", loadedAt); err == nil {
		p.LoadedAt = t
	}
	return p, nil
}
