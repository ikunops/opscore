package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// Migration is a single forward-only schema change, identified by a monotonic
// Version. Up MUST be idempotent DDL — `CREATE TABLE IF NOT EXISTS` /
// `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` — so re-running on an existing
// database (or after a crash mid-migration) is always safe.
//
// Rationale (Round 5, S5): OpsCore has grown several tables (operations,
// users, roles, audit, tasks, capability_snapshots, execution_records). The
// ad-hoc `CREATE TABLE IF NOT EXISTS` on open is fine while we only ADD
// columns, but the moment a non-additive change lands (rename, retract,
// re-type) there is no record of what state a given .db file is in. A
// versioned migration history is a Release-MUST before v0.1.
type Migration struct {
	Version int
	Name    string
	Up      string
}

// Migrations is the ordered, append-only list of all schema changes.
// To evolve the schema, add a NEW entry at the TAIL with a higher Version.
// Never reorder, rename, or edit an existing entry — that would corrupt the
// migration history of deployed databases.
//
// v1 (baseline) reproduces the original Schema verbatim. Pre-existing
// databases open fine: every statement is "IF NOT EXISTS", and the version
// is simply recorded as applied.
var Migrations = []Migration{
	{Version: 1, Name: "baseline", Up: Schema},
	// v2 (Phase 3.0 / MUST-3 + MUST-1 + SHOULD): introduce the durable
	// plugin lifecycle registry and stamp operation/execution origin onto
	// each execution record. Ensure applies each version exactly once per
	// database, so the ALTER ADD COLUMN statements are safe (SQLite has
	// no "ADD COLUMN IF NOT EXISTS"; re-applying the same version would
	// fail, but the schema_migrations tracker prevents that).
	{Version: 2, Name: "plugin_registry_and_exec_origin", Up: `
CREATE TABLE IF NOT EXISTS plugin_registry (
  name       TEXT PRIMARY KEY,
  version    TEXT NOT NULL,
  status     TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 0,
  loaded_at  TEXT NOT NULL DEFAULT ''
);
ALTER TABLE execution_records ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_records ADD COLUMN origin TEXT NOT NULL DEFAULT '';
`},
	// v3 (Phase 3.4.1 / Plugin Identity Migration): give plugin_registry a
	// stable, version-aware identity column `id` (= name@version). This makes
	// two plugins with the SAME display Name but DIFFERENT versions coexist
	// (mysql@1.0.0 vs mysql@2.0.0) instead of clobbering each other on the
	// former Name PRIMARY KEY. Backfill existing rows (id='') with their
	// Name (safe: Name is already UNIQUE), then add a UNIQUE index on id.
	//
	// NOTE (GPT Round 12 SHOULD): legacy rows may have id == name (migrated
	// historical data). New plugin identities MUST use name@version — never
	// produce a bare-name id. Migration is responsible for HISTORY, not the
	// future format.
	{Version: 3, Name: "plugin_identity", Up: `
ALTER TABLE plugin_registry ADD COLUMN id TEXT NOT NULL DEFAULT '';
UPDATE plugin_registry SET id = name WHERE id = '';
CREATE UNIQUE INDEX IF NOT EXISTS plugin_registry_id_uniq ON plugin_registry(id);
`},
	// v4 (Phase 17.2 / ADR-036 §3.5, OQ-17.1-B): the audit row must carry the
	// policy revision it concerns and the correlation id of the request that
	// produced it. Without `revision` an audit reader cannot tell WHICH version
	// of a policy an entry is about — the one asked for, the one committed, or
	// the one that caused a conflict. Without `correlation_id` an intent row
	// and its outcome row are linked only by adjacency, which stops being true
	// the moment two operators mutate concurrently.
	//
	// Both columns are added ONLY here, deliberately NOT in the v1 baseline
	// Schema. Declaring them in both would break every FRESH database: v1 would
	// create the columns and this ALTER would then fail with "duplicate column
	// name". Additive DEFAULTs mean pre-existing rows read back as (0, "") —
	// the honest value for events that never had a revision to record.
	{Version: 4, Name: "audit_revision_correlation", Up: `
ALTER TABLE audit_events ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit_events ADD COLUMN correlation_id TEXT NOT NULL DEFAULT '';
-- Phase 17.3 (ADR-038 §3.1): optional, non-breaking lookup index on
-- correlation_id to speed the replay guard + reconciliation scans. Placed HERE
-- (not in the v1 baseline) because the column is added by this same migration;
-- an index in the baseline would reference a not-yet-existing column.
-- IF NOT EXISTS keeps it idempotent across opens.
CREATE INDEX IF NOT EXISTS idx_audit_corr ON audit_events(correlation_id);
`},
	// v5 (Phase 18 / ADR-040 §3.1): index-only. AuditStore.Query pushes the
	// target/result predicates into SQL so `limit` can mean "rows returned"
	// instead of "rows scanned"; without these two indexes that push-down turns
	// every filtered read into a full table walk.
	//
	// R18-6 constrains this migration to indexes and nothing else — no column,
	// no table, no data rewrite, and above all no edit to the v1 baseline. Both
	// indexed columns already exist there, and per the R83 §1.1 ruling an index
	// belongs in a migration where its column already exists (adding it to the
	// baseline would silently change the schema of every deployed database's
	// migration history). idx_audit_corr from v4 already covers correlation_id.
	//
	// The Up text is deliberately comment-free: TestV5MigrationIsIndexOnly
	// upper-cases the whole string and rejects the DDL/DML verbs, so prose here
	// would trip the guard. The rationale lives in Go comments instead.
	{Version: 5, Name: "audit_query_indexes", Up: `
CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_events(target);
CREATE INDEX IF NOT EXISTS idx_audit_result ON audit_events(result);
`},
	// v6 (Phase 21 / ADR-048): protection kill-state + per-capability protection
	// config. All three tables are additive (CREATE TABLE IF NOT EXISTS).
	//
	// NOTE: the ADR-048 §4 schema declares `REFERENCES operation(name)`, but the
	// real baseline table is named `operations` (plural) and these protection
	// tables must bootstrap even when no operation rows exist yet, so the FK is
	// intentionally omitted — kill-state is keyed by capability_id string and
	// does not require a referential guarantee to function. This is a
	// documented deviation from the ADR's DDL, harmless to the protection
	// semantics.
	{Version: 6, Name: "protection_kill_state", Up: `
CREATE TABLE IF NOT EXISTS operation_protection (
  capability_id      TEXT PRIMARY KEY,
  rate_limit          INTEGER NOT NULL DEFAULT 60,
  timeout_seconds     INTEGER NOT NULL DEFAULT 30,
  breaker_threshold   INTEGER NOT NULL DEFAULT 5,
  breaker_window_s    INTEGER NOT NULL DEFAULT 60,
  cooldown_seconds    INTEGER NOT NULL DEFAULT 30,
  concurrency_cap     INTEGER NOT NULL DEFAULT 8
);
CREATE TABLE IF NOT EXISTS kill_state (
  capability_id  TEXT PRIMARY KEY,
  killed        INTEGER NOT NULL DEFAULT 0,
  killed_at     TEXT,
  killed_by     TEXT
);
CREATE TABLE IF NOT EXISTS principal_kill_state (
  principal_hash TEXT PRIMARY KEY,
  killed        INTEGER NOT NULL DEFAULT 0,
  killed_at     TEXT,
  killed_by     TEXT
);
`},
}

// Ensure brings the database up to the latest migration version, recording each
// applied migration in the schema_migrations tracking table. It is safe to
// call on every open; already-applied migrations are skipped, and each
// unapplied one runs inside a transaction so a failure leaves the DB at a
// known (previous) version.
func Ensure(db *sql.DB, migrations []Migration) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("migrate: init tracking table: %w", err)
	}

	applied := make(map[int]bool)
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("migrate: read tracking: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("migrate: scan tracking: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migrate: iterate tracking: %w", err)
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migrate: begin v%d: %w", m.Version, err)
		}
		if _, err := tx.Exec(m.Up); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate: apply v%d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
			m.Version, m.Name, time.Now().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate: record v%d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate: commit v%d: %w", m.Version, err)
		}
	}
	return nil
}
