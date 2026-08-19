package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// Phase 17.2 / ADR-036 §3.5 (OQ-17.1-B): the audit row carries the policy
// revision it concerns and the correlation id of the request that produced it.

// The migration must work on the path that actually matters: an EXISTING
// database already at v3, with rows in it. A test that only ever exercises a
// fresh database proves nothing about deployed data.
func TestMigrateV4UpgradesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opscore.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Bring the database only as far as v3, i.e. the state a deployment that
	// predates Phase 17 is in.
	var upToV3 []Migration
	for _, m := range Migrations {
		if m.Version <= 3 {
			upToV3 = append(upToV3, m)
		}
	}
	if err := Ensure(db, upToV3); err != nil {
		t.Fatalf("ensure v1..v3: %v", err)
	}
	if _, err := db.Exec("SELECT revision FROM audit_events LIMIT 1"); err == nil {
		t.Fatal("revision column must NOT exist before v4 — test premise broken")
	}

	// A legacy row, written the pre-Phase-17 way.
	if _, err := db.Exec(
		`INSERT INTO audit_events(timestamp, actor, operation, action, target, result, detail)
		 VALUES('2020-01-01T00:00:00Z','legacy','op','execute','svc','success','old row')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Now upgrade.
	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("ensure v4: %v", err)
	}

	// The legacy row survives and reads back with the honest defaults: it never
	// had a revision, so 0 / "" is the truth, not a placeholder.
	var rev int
	var corr, detail string
	if err := db.QueryRow(
		`SELECT revision, correlation_id, detail FROM audit_events WHERE actor='legacy'`,
	).Scan(&rev, &corr, &detail); err != nil {
		t.Fatalf("read legacy row after upgrade: %v", err)
	}
	if rev != 0 || corr != "" {
		t.Fatalf("legacy row backfilled with junk: revision=%d correlation=%q", rev, corr)
	}
	if detail != "old row" {
		t.Fatalf("legacy row corrupted by migration: detail=%q", detail)
	}

	// And the upgrade is idempotent — Ensure runs on every open.
	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("re-ensure after v4: %v", err)
	}
}

// Round-trip: what Append writes, List must read back — including the two new
// columns. Their whole purpose is to be readable later.
func TestAuditRevisionCorrelationRoundTrip(t *testing.T) {
	s := newTestDB(t)
	audit := s.Audit()

	if _, err := audit.Append(storage.AuditEvent{
		Actor:         "alice",
		Operation:     "policy",
		Action:        "policy.activate",
		Target:        "pol-1",
		Result:        "intent",
		Detail:        "if-match=3",
		Revision:      3,
		CorrelationID: "req-abc",
	}); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	if _, err := audit.Append(storage.AuditEvent{
		Actor:         "alice",
		Operation:     "policy",
		Action:        "policy.activate",
		Target:        "pol-1",
		Result:        "success",
		Detail:        "committed",
		Revision:      4,
		CorrelationID: "req-abc",
	}); err != nil {
		t.Fatalf("append outcome: %v", err)
	}

	events, err := audit.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	// List is id DESC: outcome first, then intent.
	outcome, intent := events[0], events[1]
	if intent.Result != "intent" || intent.Revision != 3 {
		t.Fatalf("intent row = %+v", intent)
	}
	if outcome.Result != "success" || outcome.Revision != 4 {
		t.Fatalf("outcome row = %+v", outcome)
	}
	if intent.CorrelationID != outcome.CorrelationID || intent.CorrelationID != "req-abc" {
		t.Fatalf("correlation lost: intent=%q outcome=%q", intent.CorrelationID, outcome.CorrelationID)
	}

	byOp, err := audit.ListByOperation("policy", 10)
	if err != nil {
		t.Fatalf("list by operation: %v", err)
	}
	if len(byOp) != 2 || byOp[0].Revision != 4 || byOp[1].Revision != 3 {
		t.Fatalf("ListByOperation dropped the new columns: %+v", byOp)
	}
}

// R79-B: the timestamp written by Append MUST read back as a real time, not the zero value.
// Regression guard — query() previously scanned the column into a throwaway local and never
// assigned it to e.Timestamp.
func TestAuditTimestampRoundTrip(t *testing.T) {
	s := newTestDB(t)
	audit := s.Audit()

	before := time.Now().Add(-time.Second).UTC()
	if _, err := audit.Append(storage.AuditEvent{
		Actor:     "ts-actor",
		Operation: "policy",
		Action:    "policy.activate",
		Target:    "pol-ts",
		Result:    "success",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	after := time.Now().Add(time.Second).UTC()

	events, err := audit.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events returned")
	}
	e := events[0] // List is id DESC
	if e.Timestamp.IsZero() {
		t.Fatalf("Timestamp read back as zero value (R79-B not fixed): %+v", e)
	}
	got := e.Timestamp.UTC()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Timestamp %v outside write window [%v, %v]", got, before, after)
	}

	// ListByOperation shares the same query path — exercise it too.
	byOp, err := audit.ListByOperation("policy", 10)
	if err != nil {
		t.Fatalf("list by operation: %v", err)
	}
	if len(byOp) == 0 || byOp[0].Timestamp.IsZero() {
		t.Fatalf("ListByOperation dropped Timestamp: %+v", byOp)
	}
}

// The pairing must survive interleaving. This is the case CorrelationID exists
// for: with two operators mutating at once, adjacency and timestamps no longer
// identify which outcome belongs to which intent.
func TestAuditCorrelationSurvivesInterleaving(t *testing.T) {
	s := newTestDB(t)
	audit := s.Audit()

	rows := []storage.AuditEvent{
		{Operation: "policy", Action: "policy.activate", Target: "pol-1", Result: "intent", Revision: 1, CorrelationID: "req-A"},
		{Operation: "policy", Action: "policy.activate", Target: "pol-1", Result: "intent", Revision: 1, CorrelationID: "req-B"},
		{Operation: "policy", Action: "policy.activate", Target: "pol-1", Result: "success", Revision: 2, CorrelationID: "req-A"},
		{Operation: "policy", Action: "policy.activate", Target: "pol-1", Result: "failure", Revision: 2, CorrelationID: "req-B", Detail: "revision conflict"},
	}
	for i, r := range rows {
		if _, err := audit.Append(r); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	events, err := audit.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byCorr := map[string][]storage.AuditEvent{}
	for _, e := range events {
		byCorr[e.CorrelationID] = append(byCorr[e.CorrelationID], e)
	}
	if len(byCorr["req-A"]) != 2 || len(byCorr["req-B"]) != 2 {
		t.Fatalf("correlation grouping failed: %v", byCorr)
	}

	// req-B lost the race. Per the FROZEN table in ADR-036 §3.3.2.1 a CAS
	// refusal is a "failure" whose Revision carries the ACTUAL stored revision
	// (2) and whose Detail carries the reason — NOT a separate "conflict"
	// result class. req-A's committed 2 and req-B's observed 2 are the same
	// number and are told apart by Result, not by position or by value.
	var bIntent, bOutcome storage.AuditEvent
	for _, e := range byCorr["req-B"] {
		if e.Result == "intent" {
			bIntent = e
		} else {
			bOutcome = e
		}
	}
	if bIntent.Revision != 1 {
		t.Fatalf("req-B intent revision = %d, want the expected revision 1", bIntent.Revision)
	}
	if bOutcome.Result != "failure" || bOutcome.Revision != 2 {
		t.Fatalf("req-B outcome = %+v, want failure at actual revision 2", bOutcome)
	}
	if bOutcome.Detail == "" {
		t.Fatal("req-B failure row carries no reason; the conflict must be recorded as the failure reason")
	}
}
