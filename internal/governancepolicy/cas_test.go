package governancepolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/YuDong999/opscore/internal/governance"
)

// This file is the executable form of the Phase 17 mutation contract
// (ADR-036 §3.2.1 CAS-1…CAS-6, §3.2.2 CT-1…CT-9, signed R77-A). Every clause
// gets a test: a frozen contract nobody checks is a comment.

func rulesA() []governance.Rule {
	return []governance.Rule{
		{RuleID: "r1", Priority: 10, Kind: governance.RuleGroupAllow, Param: "g1"},
	}
}

func rulesB() []governance.Rule {
	return []governance.Rule{
		{RuleID: "r1", Priority: 10, Kind: governance.RuleGroupAllow, Param: "OTHER"},
	}
}

// seed creates pol via the CAS create path and returns the stored record.
func seed(t *testing.T, repo Repository, id string) PolicyRecord {
	t.Helper()
	rec, err := repo.CompareAndSave(PolicyRecord{PolicyID: id, Rules: rulesA()}, 0)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return rec
}

// --- CompareAndSave -------------------------------------------------------

// CAS-1: expectedRevision 0 means "must not exist"; the create lands at
// revision 1 as a Draft, with lifecycle fields owned by the repository.
func TestCompareAndSaveCreate(t *testing.T) {
	repo := newTestRepo(t)

	rec, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesA()}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Revision != 1 {
		t.Fatalf("revision = %d, want 1", rec.Revision)
	}
	if rec.Status != StatusDraft {
		t.Fatalf("status = %q, want draft", rec.Status)
	}
	if rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", rec)
	}
	if !rec.ActivatedAt.IsZero() {
		t.Fatalf("ActivatedAt must stay zero on create, got %v", rec.ActivatedAt)
	}

	got, ok, err := repo.Get("pol-1")
	if err != nil || !ok {
		t.Fatalf("get after create: ok=%v err=%v", ok, err)
	}
	if got.Revision != 1 || got.Status != StatusDraft {
		t.Fatalf("persisted = %+v", got)
	}
}

// CAS-1 (negative): a second create for the same PolicyID is a revision
// conflict, NOT a silent overwrite. The stored record must be untouched.
func TestCompareAndSaveCreateConflict(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")

	_, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, 0)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}

	got, _, _ := repo.Get("pol-1")
	if got.Revision != first.Revision || !SameContent(got, first) {
		t.Fatalf("store mutated by a rejected create: %+v", got)
	}
}

// A create must not let the caller conjure an Active policy in one step: the
// Status field is repository-owned (P17-4).
func TestCompareAndSaveCreateForcesDraft(t *testing.T) {
	repo := newTestRepo(t)
	rec, err := repo.CompareAndSave(PolicyRecord{
		PolicyID: "pol-1",
		Rules:    rulesA(),
		Status:   StatusActive,
		Revision: 99,
	}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Status != StatusDraft || rec.Revision != 1 {
		t.Fatalf("caller-supplied lifecycle honoured: %+v", rec)
	}
}

// CAS-2 / CAS-4: a matching revision replaces content, bumps the revision, and
// preserves repository-owned CreatedAt — all in one write.
func TestCompareAndSaveUpdate(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")

	updated, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, first.Revision)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != first.Revision+1 {
		t.Fatalf("revision = %d, want %d", updated.Revision, first.Revision+1)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt drifted: %v -> %v", first.CreatedAt, updated.CreatedAt)
	}
	if updated.Rules[0].Param != "OTHER" {
		t.Fatalf("content not replaced: %+v", updated.Rules)
	}

	got, _, _ := repo.Get("pol-1")
	if got.Revision != updated.Revision || !SameContent(got, updated) {
		t.Fatalf("persisted != returned: %+v vs %+v", got, updated)
	}
}

// CAS-2 (negative): a stale expectedRevision is rejected and writes nothing.
func TestCompareAndSaveStale(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")
	second, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, first.Revision)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// Someone still holding revision 1 tries again.
	_, err = repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesA()}, first.Revision)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != second.Revision || !SameContent(got, second) {
		t.Fatalf("stale write leaked through: %+v", got)
	}
}

// CAS-2 with a revision for a policy that does not exist is ErrNotFound, which
// is a different remedy from ErrRevisionConflict (create it vs refetch it).
func TestCompareAndSaveMissing(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.CompareAndSave(PolicyRecord{PolicyID: "ghost", Rules: rulesA()}, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Update is Draft-only (ADR-036 §3.4): editing an Active policy in place would
// change what is in force without a lifecycle decision.
func TestCompareAndSaveNonDraftRejected(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")
	active, err := repo.CompareAndTransition("pol-1", first.Revision, StatusActive)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	_, err = repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, active.Revision)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != active.Revision {
		t.Fatalf("rejected update still bumped revision: %d", got.Revision)
	}
}

func TestCompareAndSaveInvalidInput(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.CompareAndSave(PolicyRecord{Rules: rulesA()}, 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("empty id err = %v, want ErrInvalidID", err)
	}
	if _, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1"}, -1); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("negative revision err = %v, want ErrInvalidID", err)
	}
}

// --- CompareAndTransition -------------------------------------------------

// CT-3: a real transition mutates status AND bumps the revision in one write.
func TestCompareAndTransitionActivate(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")

	act, err := repo.CompareAndTransition("pol-1", first.Revision, StatusActive)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if act.Status != StatusActive {
		t.Fatalf("status = %q", act.Status)
	}
	if act.Revision != first.Revision+1 {
		t.Fatalf("revision = %d, want %d", act.Revision, first.Revision+1)
	}
	if act.ActivatedAt.IsZero() {
		t.Fatal("ActivatedAt not stamped on activate")
	}

	// Draft <-> Active both ways, then Active -> Archived.
	back, err := repo.CompareAndTransition("pol-1", act.Revision, StatusDraft)
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if back.Status != StatusDraft || back.Revision != act.Revision+1 {
		t.Fatalf("deactivate = %+v", back)
	}
	arch, err := repo.CompareAndTransition("pol-1", back.Revision, StatusArchived)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if arch.Status != StatusArchived {
		t.Fatalf("archive = %+v", arch)
	}
}

// CT-8 (self-transition): succeeds, returns the record unchanged, and must NOT
// bump the revision. A bump here would invalidate the caller's own If-Match on
// every retry — the exact opposite of idempotent.
func TestCompareAndTransitionSelfIsNoOp(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")
	act, err := repo.CompareAndTransition("pol-1", first.Revision, StatusActive)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	again, err := repo.CompareAndTransition("pol-1", act.Revision, StatusActive)
	if err != nil {
		t.Fatalf("self-transition must succeed, got %v", err)
	}
	if again.Revision != act.Revision {
		t.Fatalf("self-transition bumped revision: %d -> %d", act.Revision, again.Revision)
	}
	if !again.UpdatedAt.Equal(act.UpdatedAt) {
		t.Fatalf("self-transition rewrote UpdatedAt: %v -> %v", act.UpdatedAt, again.UpdatedAt)
	}

	// Repeating it many times must stay a fixpoint.
	for i := 0; i < 5; i++ {
		if _, err := repo.CompareAndTransition("pol-1", act.Revision, StatusActive); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != act.Revision {
		t.Fatalf("revision drifted under replay: %d", got.Revision)
	}
}

// CT-8 also applies to a terminal state: archive-on-archived is a no-op
// success, not the ErrIllegalTransition the state machine would otherwise give
// (Archived has no outgoing edges).
func TestCompareAndTransitionArchivedSelf(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")
	arch, err := repo.CompareAndTransition("pol-1", first.Revision, StatusArchived)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	again, err := repo.CompareAndTransition("pol-1", arch.Revision, StatusArchived)
	if err != nil {
		t.Fatalf("archive replay: %v", err)
	}
	if again.Revision != arch.Revision {
		t.Fatalf("archive replay bumped revision: %d", again.Revision)
	}
}

// CT-9: when the caller is BOTH stale and asking for something illegal, the
// answer is ErrRevisionConflict. Reporting the illegality first would send them
// to fix a problem that may not exist once they refetch.
func TestCompareAndTransitionRevisionBeatsLegality(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")
	arch, err := repo.CompareAndTransition("pol-1", first.Revision, StatusArchived)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Archived -> Active is illegal AND the revision is stale.
	_, err = repo.CompareAndTransition("pol-1", first.Revision, StatusActive)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict (CT-9)", err)
	}
	// With a fresh revision the same request is judged on legality.
	_, err = repo.CompareAndTransition("pol-1", arch.Revision, StatusActive)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestCompareAndTransitionRejects(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")

	if _, err := repo.CompareAndTransition("", 1, StatusActive); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("empty id err = %v, want ErrInvalidID", err)
	}
	if _, err := repo.CompareAndTransition("ghost", 1, StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CompareAndTransition("pol-1", first.Revision+7, StatusActive); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale err = %v, want ErrRevisionConflict", err)
	}
	if _, err := repo.CompareAndTransition("pol-1", first.Revision, PolicyStatus("banana")); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("unknown target err = %v, want ErrIllegalTransition", err)
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != first.Revision || got.Status != StatusDraft {
		t.Fatalf("a rejected transition mutated the store: %+v", got)
	}
}

// --- Concurrency ----------------------------------------------------------

// The point of a CAS is that concurrent writers holding the SAME expected
// revision cannot both win. Without the repository mutex this test fails: two
// goroutines read revision N, both compare equal, both write N+1, and one
// update is silently lost.
func TestCompareAndTransitionConcurrentSingleWinner(t *testing.T) {
	repo := newTestRepo(t)
	first := seed(t, repo, "pol-1")

	const writers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, conflicts int

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			_, err := repo.CompareAndTransition("pol-1", first.Revision, StatusActive)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrRevisionConflict):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (lost update)", wins)
	}
	if conflicts != writers-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, writers-1)
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != first.Revision+1 || got.Status != StatusActive {
		t.Fatalf("final = %+v", got)
	}
}

// Same guarantee for creates: only one of N racing creates may exist.
func TestCompareAndSaveConcurrentCreateSingleWinner(t *testing.T) {
	repo := newTestRepo(t)

	const writers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins int

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			_, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesA()}, 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrRevisionConflict) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
	got, _, _ := repo.Get("pol-1")
	if got.Revision != 1 {
		t.Fatalf("revision = %d, want 1", got.Revision)
	}
}

// Concurrent updates must produce a revision chain with no gaps and no
// duplicates: each winner's revision is exactly its predecessor + 1.
func TestCompareAndSaveConcurrentUpdatesNoLostWrite(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo, "pol-1")

	const rounds = 12
	for i := 0; i < rounds; i++ {
		cur, _, err := repo.Get("pol-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(4)
		for j := 0; j < 4; j++ {
			go func() {
				defer wg.Done()
				if _, err := repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, cur.Revision); err != nil && !errors.Is(err, ErrRevisionConflict) {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()
		next, _, _ := repo.Get("pol-1")
		if next.Revision != cur.Revision+1 {
			t.Fatalf("round %d: revision %d -> %d, want +1 exactly", i, cur.Revision, next.Revision)
		}
	}
}

// --- Store hardening ------------------------------------------------------

// Atomic publication must not litter the store directory with temp files, and
// every published file must be a complete, parseable record.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewFileRepository(dir)
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	rec := seed(t, repo, "pol-1")
	for i := 0; i < 5; i++ {
		rec, err = repo.CompareAndSave(PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}, rec.Revision)
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".policy-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("store dir = %v, want exactly one published file", names)
	}
	if got := filepath.Base(entries[0].Name()); got != "policy-pol-1.json" {
		t.Fatalf("published file = %s", got)
	}
}

// --- SameContent ----------------------------------------------------------

func TestSameContent(t *testing.T) {
	base := PolicyRecord{PolicyID: "pol-1", Rules: rulesA()}

	// Repository-owned fields are ignored: a replay carries no revision.
	stored := PolicyRecord{
		PolicyID: "pol-1",
		Rules:    rulesA(),
		Revision: 7,
		Status:   StatusActive,
	}
	if !SameContent(base, stored) {
		t.Fatal("revision/status/timestamps must not affect content equality")
	}

	// nil and empty rule sets mean the same thing.
	if !SameContent(PolicyRecord{PolicyID: "p"}, PolicyRecord{PolicyID: "p", Rules: []governance.Rule{}}) {
		t.Fatal("nil vs empty rules must compare equal")
	}

	if SameContent(base, PolicyRecord{PolicyID: "pol-1", Rules: rulesB()}) {
		t.Fatal("different rule param must not compare equal")
	}
	if SameContent(base, PolicyRecord{PolicyID: "pol-2", Rules: rulesA()}) {
		t.Fatal("different PolicyID must not compare equal")
	}

	// Order counts: payload equality, not semantic equality (documented choice).
	two := []governance.Rule{
		{RuleID: "a", Priority: 1, Kind: governance.RuleChangeFreeze},
		{RuleID: "b", Priority: 2, Kind: governance.RuleChangeFreeze},
	}
	rev := []governance.Rule{two[1], two[0]}
	if SameContent(PolicyRecord{PolicyID: "p", Rules: two}, PolicyRecord{PolicyID: "p", Rules: rev}) {
		t.Fatal("reordered rules must be reported as a different payload")
	}
}
