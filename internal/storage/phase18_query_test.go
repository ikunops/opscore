package storage

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Phase 18 — AuditStore.Query (ADR-040 §3.1)
//
// These tests encode the correctness defect ADR-039 §2 F-3 named: Phase 17.3
// read the newest N rows and THEN filtered, so a predicate whose only matches
// live outside that window returned an empty list that LOOKED like "no such
// events". Filtering must happen in the store, and `limit` must mean "rows
// returned".
// ---------------------------------------------------------------------------

// seedAudit appends n events, oldest first, invoking shape(i) to decorate each.
func seedAudit(t *testing.T, s AuditStore, n int, shape func(i int) AuditEvent) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.Append(shape(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

// TestAuditQueryFiltersBeforeLimit is THE decisive test of Phase 18 (ADR-040
// §3.4). Policy "rare" exists only in the OLDEST rows. A limit smaller than the
// distance to those rows must still find them: the predicate is applied by the
// store, not to a pre-truncated page.
//
// Under the Phase 17.3 behaviour (List(limit) then filter in the handler) this
// returns zero events — a false "no such policy".
func TestAuditQueryFiltersBeforeLimit(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()

	// Two oldest rows target "rare"; 50 newer rows target "common".
	seedAudit(t, a, 2, func(i int) AuditEvent {
		return AuditEvent{Target: "rare", Result: "success", Action: "policy.create"}
	})
	seedAudit(t, a, 50, func(i int) AuditEvent {
		return AuditEvent{Target: "common", Result: "success", Action: "policy.update"}
	})

	page, err := a.Query(AuditQuery{Target: "rare", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("Query(target=rare, limit=10) returned %d events, want 2 — "+
			"the predicate must be applied BEFORE the limit, or an old row is invisible", len(page.Events))
	}
	for _, e := range page.Events {
		if e.Target != "rare" {
			t.Errorf("predicate leaked row with target %q", e.Target)
		}
	}
	if page.Truncated {
		t.Errorf("Truncated = true, want false: 2 matches fit inside limit 10")
	}
}

// TestAuditQueryTruncationFlag: more matching rows than the limit must set
// Truncated, so an operator can tell "this is a window" from "this is all".
func TestAuditQueryTruncationFlag(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 25, func(i int) AuditEvent {
		return AuditEvent{Target: "p", Result: "success"}
	})

	page, err := a.Query(AuditQuery{Target: "p", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 10 {
		t.Fatalf("len(Events) = %d, want 10", len(page.Events))
	}
	if !page.Truncated {
		t.Error("Truncated = false, want true: 25 matches exist beyond a limit of 10 (R18-2)")
	}
	if page.Limit != 10 {
		t.Errorf("Limit = %d, want 10 (the effective limit must be reported)", page.Limit)
	}
}

// TestAuditQueryLimitClampIsVisible: an over-large limit is clamped, and the
// clamp is REPORTED. A silent clamp is a silent truncation (R18-2).
func TestAuditQueryLimitClampIsVisible(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 3, func(i int) AuditEvent { return AuditEvent{Target: "p"} })

	page, err := a.Query(AuditQuery{Target: "p", Limit: 99999})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Limit != MaxAuditQueryLimit {
		t.Errorf("Limit = %d, want %d — the clamp must be visible in the page, never silent",
			page.Limit, MaxAuditQueryLimit)
	}
	if len(page.Events) != 3 {
		t.Errorf("len(Events) = %d, want 3", len(page.Events))
	}
}

// TestAuditQueryZeroLimitUsesDefault: an unset limit resolves to the documented
// default and reports it.
func TestAuditQueryZeroLimitUsesDefault(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 2, func(i int) AuditEvent { return AuditEvent{Target: "p"} })

	page, err := a.Query(AuditQuery{Target: "p"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Limit != DefaultAuditQueryLimit {
		t.Errorf("Limit = %d, want %d for an unset limit", page.Limit, DefaultAuditQueryLimit)
	}
}

// TestAuditQueryEmptyIsNotAnError: "searched, found nothing" is a successful
// answer with an empty slice — distinct from "could not search", which is an
// error. Collapsing the two is the false-clean this phase exists to forbid
// (ADR-040 §3.1).
func TestAuditQueryEmptyIsNotAnError(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 3, func(i int) AuditEvent { return AuditEvent{Target: "present"} })

	page, err := a.Query(AuditQuery{Target: "absent", Limit: 10})
	if err != nil {
		t.Fatalf("a miss must not be an error, got %v", err)
	}
	if len(page.Events) != 0 {
		t.Errorf("len(Events) = %d, want 0", len(page.Events))
	}
	if page.Events == nil {
		t.Error("Events must be a non-nil empty slice so it marshals as [] not null")
	}
}

// TestAuditQueryPredicatesAreConjunctive: every non-empty field must match.
func TestAuditQueryPredicatesAreConjunctive(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 1, func(i int) AuditEvent {
		return AuditEvent{Target: "p1", Result: "success", Action: "policy.create", CorrelationID: "c1"}
	})
	seedAudit(t, a, 1, func(i int) AuditEvent {
		return AuditEvent{Target: "p1", Result: "failure", Action: "policy.create", CorrelationID: "c2"}
	})

	cases := []struct {
		name string
		q    AuditQuery
		want int
	}{
		{"target only", AuditQuery{Target: "p1"}, 2},
		{"target+result", AuditQuery{Target: "p1", Result: "failure"}, 1},
		{"action", AuditQuery{Action: "policy.create"}, 2},
		{"correlation", AuditQuery{CorrelationID: "c1"}, 1},
		{"all four, consistent", AuditQuery{Target: "p1", Result: "success", Action: "policy.create", CorrelationID: "c1"}, 1},
		{"all four, contradictory", AuditQuery{Target: "p1", Result: "success", CorrelationID: "c2"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := a.Query(tc.q)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(page.Events) != tc.want {
				t.Errorf("len(Events) = %d, want %d", len(page.Events), tc.want)
			}
		})
	}
}

// TestAuditQueryOrderIsNewestFirst: ordering matches List so the two read paths
// cannot disagree about what "the latest event" means.
func TestAuditQueryOrderIsNewestFirst(t *testing.T) {
	m := NewMemoryStorage()
	defer m.Close()
	a := m.Audit()
	seedAudit(t, a, 3, func(i int) AuditEvent {
		return AuditEvent{Target: "p", Revision: i + 1}
	})

	page, err := a.Query(AuditQuery{Target: "p", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("len(Events) = %d, want 3", len(page.Events))
	}
	for i := 1; i < len(page.Events); i++ {
		if page.Events[i-1].ID <= page.Events[i].ID {
			t.Fatalf("events not newest-first: id[%d]=%d <= id[%d]=%d",
				i-1, page.Events[i-1].ID, i, page.Events[i].ID)
		}
	}
}
