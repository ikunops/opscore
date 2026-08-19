package sqlite

import (
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Phase 18 — SQL-side audit query (ADR-040 §3.1 / §3.4)
// ---------------------------------------------------------------------------

func newAuditDB(t *testing.T) *SQLiteStorage {
	t.Helper()
	s, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "opscore.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAuditQueryUsesBoundParameters is the mechanical guard from ADR-040 §3.4.
// It asserts two things about the SQL builder, both of which are properties a
// human reviewer would have to re-check on every future edit:
//
//  1. behaviourally — every predicate value travels as a bound `?` argument and
//     NEVER appears inside the SQL text;
//  2. syntactically — buildAuditQuery contains no fmt.Sprintf, so a future edit
//     cannot reintroduce string interpolation without tripping this test.
func TestAuditQueryUsesBoundParameters(t *testing.T) {
	// A deliberately hostile value: if it is ever concatenated instead of
	// bound, it is both visible in the SQL text and an injection.
	const hostile = "pol-x'; DROP TABLE audit_events;--"
	q := storage.AuditQuery{
		Target:        hostile,
		Result:        "success",
		Action:        "policy.create",
		CorrelationID: "corr-abc",
		Limit:         7,
	}
	sqlText, args := buildAuditQuery(q)

	for _, a := range args {
		s, ok := a.(string)
		if !ok || s == "" {
			continue
		}
		if strings.Contains(sqlText, s) {
			t.Errorf("argument %q appears literally in the SQL text — it must be bound, not concatenated:\n%s", s, sqlText)
		}
	}
	if got, want := strings.Count(sqlText, "?"), len(args); got != want {
		t.Errorf("placeholder count = %d, args = %d — every argument must have exactly one `?`:\n%s", got, want, sqlText)
	}
	if !strings.Contains(sqlText, "ORDER BY id DESC") {
		t.Errorf("query must order newest-first (ORDER BY id DESC) to match List:\n%s", sqlText)
	}
	// LIMIT is bound as n+1 so truncation is detected in a single round trip.
	last, ok := args[len(args)-1].(int)
	if !ok {
		t.Fatalf("last argument must be the integer limit, got %T", args[len(args)-1])
	}
	if last != 8 {
		t.Errorf("bound limit = %d, want 8 (requested 7 + 1 truncation probe)", last)
	}

	// Syntactic half: no interpolation anywhere inside the builder.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "stores.go", nil, 0)
	if err != nil {
		t.Fatalf("parse stores.go: %v", err)
	}
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildAuditQuery" {
			return true
		}
		found = true
		ast.Inspect(fn, func(in ast.Node) bool {
			call, ok := in.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Sprint") {
				t.Errorf("buildAuditQuery calls fmt.%s — SQL values must be bound parameters, never interpolated", sel.Sel.Name)
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("buildAuditQuery not found in stores.go — guard would be vacuous")
	}
}

// TestBuildAuditQueryOmitsEmptyPredicates: an empty field is a wildcard and
// must contribute neither a WHERE clause nor an argument.
func TestBuildAuditQueryOmitsEmptyPredicates(t *testing.T) {
	sqlText, args := buildAuditQuery(storage.AuditQuery{Limit: 5})
	if strings.Contains(sqlText, "WHERE") {
		t.Errorf("an all-wildcard query must not emit WHERE:\n%s", sqlText)
	}
	if len(args) != 1 {
		t.Errorf("args = %d, want 1 (the limit only)", len(args))
	}

	sqlText2, args2 := buildAuditQuery(storage.AuditQuery{Target: "p", Limit: 5})
	if !strings.Contains(sqlText2, "target = ?") {
		t.Errorf("target predicate missing:\n%s", sqlText2)
	}
	if len(args2) != 2 {
		t.Errorf("args = %d, want 2 (target + limit)", len(args2))
	}
}

// TestV5MigrationIsIndexOnly: R18-6 forbids anything but index creation in the
// Phase 18 migration — no column, no table, no data rewrite, and above all no
// edit to the v1 baseline.
func TestV5MigrationIsIndexOnly(t *testing.T) {
	var v5 *Migration
	for i := range Migrations {
		if Migrations[i].Version == 5 {
			v5 = &Migrations[i]
		}
	}
	if v5 == nil {
		t.Fatal("migration v5 not found — Phase 18 requires an index-only v5 (ADR-040 §3.1)")
	}
	upper := strings.ToUpper(v5.Up)
	for _, forbidden := range []string{"CREATE TABLE", "ALTER TABLE", "DROP", "INSERT", "UPDATE", "DELETE"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("v5 migration contains %q — it must create indexes and nothing else:\n%s", forbidden, v5.Up)
		}
	}
	for _, want := range []string{"idx_audit_target", "idx_audit_result"} {
		if !strings.Contains(v5.Up, want) {
			t.Errorf("v5 migration does not create %s", want)
		}
	}
	// The v1 baseline must be untouched: an index on a column belongs in the
	// migration where the column already exists (R83 §1.1 ruling).
	if strings.Contains(Migrations[0].Up, "idx_audit_target") {
		t.Error("idx_audit_target leaked into the v1 baseline — baseline is frozen (R18-6)")
	}
}

// TestV5IndexesExistAfterMigrate: the migration actually runs on a fresh DB.
func TestV5IndexesExistAfterMigrate(t *testing.T) {
	s := newAuditDB(t)
	rows, err := s.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='index' AND name IN ('idx_audit_target','idx_audit_result')`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, want := range []string{"idx_audit_target", "idx_audit_result"} {
		if !got[want] {
			t.Errorf("index %s missing after migration", want)
		}
	}
}

// TestV5UpgradesExistingDatabase: the path that matters is an EXISTING v4
// database, not a fresh one.
func TestV5UpgradesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opscore.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var upToV4 []Migration
	for _, m := range Migrations {
		if m.Version <= 4 {
			upToV4 = append(upToV4, m)
		}
	}
	if err := Ensure(db, upToV4); err != nil {
		t.Fatalf("ensure v1..v4: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO audit_events(timestamp, actor, operation, action, target, result, detail, capability_hash, execution_id, snapshot_schema_version, revision, correlation_id)
		 VALUES('2026-01-01T00:00:00Z','a','o','policy.create','p','success','','','',0,1,'c1')`); err != nil {
		t.Fatalf("seed v4 row: %v", err)
	}
	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("ensure v5 over populated v4: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1 — an index-only migration must not touch data", n)
	}
}

// ---------------------------------------------------------------------------
// Conformance: the two stores must be behaviourally interchangeable
// ---------------------------------------------------------------------------

// TestAuditStoreConformance runs one predicate matrix against BOTH the SQLite
// and in-memory stores and requires identical answers (ADR-040 §3.1). Without
// this, the SQL and the Go filter drift and a test that passes on memory lies
// about production.
func TestAuditStoreConformance(t *testing.T) {
	sq := newAuditDB(t).Audit()
	mem := storage.NewMemoryStorage()
	defer mem.Close()
	me := mem.Audit()

	seed := []storage.AuditEvent{
		{Actor: "a", Operation: "management", Action: "policy.create", Target: "p1", Result: "intent", CorrelationID: "c1", Revision: 0},
		{Actor: "a", Operation: "management", Action: "policy.create", Target: "p1", Result: "success", CorrelationID: "c1", Revision: 1},
		{Actor: "b", Operation: "management", Action: "policy.update", Target: "p2", Result: "intent", CorrelationID: "c2", Revision: 1},
		{Actor: "b", Operation: "management", Action: "policy.update", Target: "p2", Result: "failure", CorrelationID: "c2", Revision: 1},
		{Actor: "c", Operation: "management", Action: "policy.activate", Target: "p1", Result: "success", CorrelationID: "c3", Revision: 2},
	}
	for _, e := range seed {
		if _, err := sq.Append(e); err != nil {
			t.Fatalf("sqlite append: %v", err)
		}
		if _, err := me.Append(e); err != nil {
			t.Fatalf("memory append: %v", err)
		}
	}

	queries := []storage.AuditQuery{
		{},
		{Limit: 2},
		{Target: "p1"},
		{Target: "p1", Result: "success"},
		{Result: "intent"},
		{Action: "policy.update"},
		{CorrelationID: "c2"},
		{Target: "p1", Limit: 1},
		{Target: "nope"},
		{Limit: 99999},
	}
	for i, q := range queries {
		sp, serr := sq.Query(q)
		mp, merr := me.Query(q)
		if (serr == nil) != (merr == nil) {
			t.Fatalf("query %d: error disagreement sqlite=%v memory=%v", i, serr, merr)
		}
		if serr != nil {
			continue
		}
		if sp.Limit != mp.Limit {
			t.Errorf("query %d: Limit sqlite=%d memory=%d", i, sp.Limit, mp.Limit)
		}
		if sp.Truncated != mp.Truncated {
			t.Errorf("query %d (%+v): Truncated sqlite=%v memory=%v", i, q, sp.Truncated, mp.Truncated)
		}
		if len(sp.Events) != len(mp.Events) {
			t.Fatalf("query %d (%+v): len sqlite=%d memory=%d", i, q, len(sp.Events), len(mp.Events))
		}
		for j := range sp.Events {
			s, m := sp.Events[j], mp.Events[j]
			if s.Target != m.Target || s.Result != m.Result || s.Action != m.Action || s.CorrelationID != m.CorrelationID {
				t.Errorf("query %d row %d: sqlite=%+v memory=%+v", i, j,
					[]string{s.Target, s.Result, s.Action, s.CorrelationID},
					[]string{m.Target, m.Result, m.Action, m.CorrelationID})
			}
		}
	}
}

// TestSQLiteAuditQueryFiltersBeforeLimit mirrors the memory-store assertion on
// real SQL: the WHERE clause must be pushed into the statement, not applied to
// an already-limited page.
func TestSQLiteAuditQueryFiltersBeforeLimit(t *testing.T) {
	a := newAuditDB(t).Audit()
	for i := 0; i < 2; i++ {
		if _, err := a.Append(storage.AuditEvent{Target: "rare", Result: "success"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	for i := 0; i < 50; i++ {
		if _, err := a.Append(storage.AuditEvent{Target: "common", Result: "success"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	page, err := a.Query(storage.AuditQuery{Target: "rare", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2 — the predicate must be in the SQL, not applied after LIMIT", len(page.Events))
	}
}

// TestSQLiteAuditQueryCursorAfter proves the Phase 19 additive cursor (ADR-042
// §3.2) on the SQL side: id < After returns only older rows, and the bound `?`
// keeps TestAuditQueryUsesBoundParameters satisfied. After==0 stays a wildcard.
func TestSQLiteAuditQueryCursorAfter(t *testing.T) {
	a := newAuditDB(t).Audit()
	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		e, err := a.Append(storage.AuditEvent{Target: "p", Result: "success"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		ids = append(ids, e.ID)
	}
	// ?after=<id3> → only rows with id < id3 are returned (ids 1 and 2).
	page, err := a.Query(storage.AuditQuery{After: ids[2]})
	if err != nil {
		t.Fatalf("Query with After: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2 (ids 1,2 < id3=%d)", len(page.Events), ids[2])
	}
	for _, e := range page.Events {
		if e.ID >= ids[2] {
			t.Errorf("cursor leaked id=%d (after=%d) — must be strictly id < after", e.ID, ids[2])
		}
	}
	// Wildcard: omitting After returns all five.
	all, err := a.Query(storage.AuditQuery{})
	if err != nil {
		t.Fatalf("Query without After: %v", err)
	}
	if len(all.Events) != 5 {
		t.Fatalf("wildcard got %d events, want 5", len(all.Events))
	}
}
