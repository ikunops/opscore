package protection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// timeNow is a fixed-ish clock for deterministic server-derived timestamps.
var timeNow = time.Now

// recordingAudit captures events with an injectable write fault.
type recordingAudit struct {
	events []ProtectionEvent
	failOn int // index (0-based) at which WriteEvent returns an error; -1 = never
	calls  int
}

func (r *recordingAudit) WriteEvent(_ context.Context, ev ProtectionEvent) error {
	r.calls++
	if r.failOn >= 0 && r.calls == r.failOn+1 {
		return errors.New("boom")
	}
	r.events = append(r.events, ev)
	return nil
}

func newTestKillStore(t *testing.T) *KillStore {
	t.Helper()
	ks := NewKillStore(newFakeKillPersistence(), timeNow)
	if err := ks.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return ks
}

func TestOperatorMutation_Kill_ServerDerivedAndAudit(t *testing.T) {
	ks := newTestKillStore(t)
	aud := &recordingAudit{failOn: -1}
	mut := NewOperatorMutation(ks, aud, timeNow)

	res, err := mut.Kill(context.Background(), "capX", "breaking change", "alice")
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if res.PrevKilled != false || res.NewKilled != true {
		t.Fatalf("result prev/new = %v/%v, want false/true", res.PrevKilled, res.NewKilled)
	}
	if res.Operator != "alice" || res.Reason != "breaking change" {
		t.Fatalf("operator/reason not server-derived: %+v", res)
	}
	// Gate now blocks the capability (single owner).
	if !ks.IsKilled("capX") {
		t.Fatal("KillStore should block capX after operator kill")
	}
	// Two audit events: intent + outcome, both protection.kill.
	if len(aud.events) != 2 {
		t.Fatalf("want 2 audit events, got %d", len(aud.events))
	}
	if aud.events[0].Action != ActionOperatorKill || aud.events[0].Detail != "intent" {
		t.Fatalf("first event should be intent: %+v", aud.events[0])
	}
	if aud.events[1].Action != ActionOperatorKill || !strings.Contains(aud.events[1].Detail, "prev_killed") {
		t.Fatalf("second event should carry server-derived prev/new: %+v", aud.events[1])
	}
	// operator kill attribution visible on read surface.
	ops := ks.ListOperatorKills()
	if len(ops) != 1 || ops[0].Operator != "alice" || ops[0].Reason != "breaking change" {
		t.Fatalf("operator kills not recorded: %+v", ops)
	}
}

func TestOperatorMutation_Release_NoRestoreExecution(t *testing.T) {
	ks := newTestKillStore(t)
	aud := &recordingAudit{failOn: -1}
	mut := NewOperatorMutation(ks, aud, timeNow)

	if _, err := mut.Kill(context.Background(), "capX", "reason", "alice"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	res, err := mut.Release(context.Background(), "capX", "alice")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if res.PrevKilled != true || res.NewKilled != false {
		t.Fatalf("release result prev/new = %v/%v, want true/false", res.PrevKilled, res.NewKilled)
	}
	// Kill flag cleared — but release does NOT mean "restored"; Gate just
	// re-evaluates. Here the kill is gone, so IsKilled is false (no other block).
	if ks.IsKilled("capX") {
		t.Fatal("kill flag should be cleared after release")
	}
	if len(ks.ListOperatorKills()) != 0 {
		t.Fatal("operator attribution should be dropped on release")
	}
	// release audit vocabulary distinct from kill.
	last := aud.events[len(aud.events)-1]
	if last.Action != ActionOperatorRelease {
		t.Fatalf("release outcome should be protection.release, got %q", last.Action)
	}
}

func TestOperatorMutation_OutcomeFailure_DegradedNoRollback(t *testing.T) {
	ks := newTestKillStore(t)
	// fail on the 2nd write (outcome) → mutation already applied, degraded error.
	aud := &recordingAudit{failOn: 1}
	mut := NewOperatorMutation(ks, aud, timeNow)

	res, err := mut.Kill(context.Background(), "capX", "reason", "alice")
	if !errors.Is(err, ErrAuditOutcomeFailed) {
		t.Fatalf("want ErrAuditOutcomeFailed, got %v", err)
	}
	// Mutation stands: Gate still blocks (no rollback claim, P22-8).
	if !ks.IsKilled("capX") {
		t.Fatal("kill must persist even when audit outcome fails (no rollback)")
	}
	// The durable intent (1st event) remains queryable.
	if len(aud.events) != 1 || aud.events[0].Detail != "intent" {
		t.Fatalf("intent event must remain: %+v", aud.events)
	}
	if !res.NewKilled {
		t.Fatal("result should reflect applied mutation")
	}
}

func TestOperatorMutation_IgnoresClientState(t *testing.T) {
	// Confirms the service never trusts client-supplied prev/new: it reads the
	// real KillStore. Start with capX already killed (simulate prior state).
	ks := newTestKillStore(t)
	_ = ks.SetKilled("capX", true) // pre-existing kill
	aud := &recordingAudit{failOn: -1}
	mut := NewOperatorMutation(ks, aud, timeNow)

	res, err := mut.Kill(context.Background(), "capX", "reason", "alice")
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	// prev MUST be server-derived true (capX was already killed), not fabricated.
	if res.PrevKilled != true {
		t.Fatalf("prev must be server-derived; got %v", res.PrevKilled)
	}
}
