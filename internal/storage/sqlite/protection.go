package sqlite

import (
	"database/sql"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// protectionStore is the SQLite-backed implementation of
// protection.KillPersistence (R93-③ single persistence owner). It imports the
// protection package (which defines the interface) but the protection package
// never imports storage — no cycle.
type protectionStore struct {
	db *sql.DB
}

// NewProtectionStore builds the kill-state persistence backend from the shared
// *sql.DB (the same handle the controlplane server already owns).
func NewProtectionStore(db *sql.DB) protection.KillPersistence {
	return &protectionStore{db: db}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *protectionStore) LoadKills() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT capability_id, killed FROM kill_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]bool)
	for rows.Next() {
		var id string
		var killed int
		if err := rows.Scan(&id, &killed); err != nil {
			return nil, err
		}
		m[id] = killed != 0
	}
	return m, rows.Err()
}

func (s *protectionStore) LoadPrincipalKills() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT principal_hash, killed FROM principal_kill_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]bool)
	for rows.Next() {
		var h string
		var killed int
		if err := rows.Scan(&h, &killed); err != nil {
			return nil, err
		}
		m[h] = killed != 0
	}
	return m, rows.Err()
}

func (s *protectionStore) SetKilled(capID string, killed bool) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO kill_state(capability_id, killed, killed_at, killed_by) VALUES(?,?,?,?)`,
		capID, b2i(killed), time.Now().Format(time.RFC3339), "system")
	return err
}

func (s *protectionStore) SetPrincipalKilled(hash string, killed bool) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO principal_kill_state(principal_hash, killed, killed_at, killed_by) VALUES(?,?,?,?)`,
		hash, b2i(killed), time.Now().Format(time.RFC3339), "system")
	return err
}

func (s *protectionStore) ListKills() ([]protection.KillEntry, error) {
	out := make([]protection.KillEntry, 0, 8)

	rows, err := s.db.Query("SELECT capability_id, killed, killed_at, killed_by FROM kill_state")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var killed int
		var at, by sql.NullString
		if err := rows.Scan(&id, &killed, &at, &by); err != nil {
			rows.Close()
			return nil, err
		}
		e := protection.KillEntry{CapabilityID: id, Killed: killed != 0}
		if at.Valid {
			e.KilledAt, _ = time.Parse(time.RFC3339, at.String)
		}
		if by.Valid {
			e.KilledBy = by.String
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := s.db.Query("SELECT principal_hash, killed, killed_at, killed_by FROM principal_kill_state")
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var h string
		var killed int
		var at, by sql.NullString
		if err := prows.Scan(&h, &killed, &at, &by); err != nil {
			prows.Close()
			return nil, err
		}
		e := protection.KillEntry{Principal: true, PrincipalHash: h, Killed: killed != 0}
		if at.Valid {
			e.KilledAt, _ = time.Parse(time.RFC3339, at.String)
		}
		if by.Valid {
			e.KilledBy = by.String
		}
		out = append(out, e)
	}
	prows.Close()
	return out, prows.Err()
}
