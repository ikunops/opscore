package sqlite

import (
	"database/sql"

	"github.com/YuDong999/opscore/internal/protection"
)

// quotaStore is the SQLite-backed implementation of protection.QuotaPersistence
// (R23-3 single persistence owner for quota DEFINITIONS). It stores definitions
// only; consumption is owned by the evidence source (QuotaEvidenceReader) and
// is never persisted here. The protection package defines the interface; this
// package implements it — there is no import cycle.
type quotaStore struct {
	db *sql.DB
}

// NewQuotaStore builds the quota-definition persistence backend from the shared
// *sql.DB (the same handle the controlplane server already owns).
func NewQuotaStore(db *sql.DB) protection.QuotaPersistence {
	return &quotaStore{db: db}
}

func (s *quotaStore) LoadDefinitions() ([]protection.QuotaDefinition, error) {
	rows, err := s.db.Query("SELECT capability_id, principal, rss_bytes, cpu_secs FROM quota_definition")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protection.QuotaDefinition, 0, 8)
	for rows.Next() {
		var capID, principal string
		var rss int64
		var cpu float64
		if err := rows.Scan(&capID, &principal, &rss, &cpu); err != nil {
			return nil, err
		}
		out = append(out, protection.QuotaDefinition{
			Capability: capID,
			Principal:  principal,
			RSSBytes:   rss,
			CPUSecs:    cpu,
		})
	}
	return out, rows.Err()
}

func (s *quotaStore) SaveDefinition(d protection.QuotaDefinition) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO quota_definition(capability_id, principal, rss_bytes, cpu_secs) VALUES(?,?,?,?)`,
		d.Capability, d.Principal, d.RSSBytes, d.CPUSecs)
	return err
}

func (s *quotaStore) ClearDefinition(capability, principal string) error {
	_, err := s.db.Exec(
		`DELETE FROM quota_definition WHERE capability_id = ? AND principal = ?`,
		capability, principal)
	return err
}
