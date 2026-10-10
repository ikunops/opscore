package server

// Maintenance-round concurrency evidence (ADR-079 §3 discipline: detector
// evidence, no phase contract, no new judgement; ADR-080 registers what this
// test found).
//
// What this test drives: TWO families' compaction observers completing their
// destructions with FORCED window overlap — the shape A7-⑦ (ADR-069 §4)
// registered as "the destructionObserver path's dispatch is not protected by
// destructionDispatchMu".
//
// What it FOUND (2026-10-11, evidence below — see ADR-080):
//
//  1. A7-⑦'s literal claim is STALE: the inline dispatch DOES go through
//     dispatchDestructionPending (destructionDispatchMu) in the current code —
//     P47's I8/B1 rework wrapped it. Under -race, this concurrent shape runs
//     with ZERO memory races (3/3 counts).
//  2. A NEW, reachable, FAIL-CLOSED behaviour: the destruction log's chain-tail
//     rule ("a completed line must directly follow its intended line") means the
//     SECOND family's begin makes the FIRST family's completion impossible —
//     completeDestruction refuses it ("the group is no longer the chain tail")
//     and that destruction stays `intended` forever. It is never a silent loss:
//     the status face reports it as destruction_unconfirmed, the chain stays
//     verifiable, and no anchor is dispatched for it.
//
// This test pins the resulting INVARIANTS deterministically (the begins are
// ordered, so exactly one window loses): the destruction log stays verifiable
// with exactly one completed and one refused-intended group, the refusal is
// loudly visible as destruction_unconfirmed on the read face, and the
// destruction-anchor stream stays conflict-free with every entry verified.
//
// The fix direction (a destruction-window mutex) is NOT taken here: the
// compaction machinery does not guarantee completion on the rewrite-failure
// path, so a window mutex could leak and block every future destruction
// (ADR-080 §4).

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestADR080ConcurrentFamilyDestructionsFailClosedAndStayVisible(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 4096 // keep the destruction log un-compacted: every group stays inspectable
		cfg.AnchorCapacity = 4096      // keep the anchor streams un-compacted too
	})
	f.driveAllFamilies()

	// Real dropped payloads from two DIFFERENT ledgers: the observers hash the
	// raw lines into the destruction target, so the inputs must be the bytes the
	// production path would actually drop.
	drop := func(path string, classify groupClassifier) ([]int64, []logLine) {
		t.Helper()
		lines, ok, err := readLogLines(path, classify)
		if err != nil || !ok || len(lines) == 0 {
			t.Fatalf("fixture: %s must hold classifiable lines (err=%v ok=%v n=%d)", path, err, ok, len(lines))
		}
		groups := make([]int64, 0, len(lines))
		for _, l := range lines {
			groups = append(groups, l.group)
		}
		return groups, lines
	}
	gL, lL := drop(filepath.Join(f.dir, chainLedgerFile), ledgerGroupOf)
	gK, lK := drop(keyLifecycleLogPath(f.dir), keyLifecycleGroupOf)

	obsLedger := f.sched.destructionObserver(destructionKindLedgerCompaction)
	obsKl := f.sched.destructionObserver(destructionKindKeyLifecycleCompaction)
	if obsLedger == nil || obsKl == nil {
		t.Fatal("fixture: destruction + anchoring must both be on")
	}

	// Baselines: the fixture's own driveAllFamilies already recorded ONE
	// destruction (the ledger compaction at publication #3) and anchored it.
	beforeAnchors, err := loadAnchorStatePath(destructionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}

	// Force the window overlap: BOTH begins land (in a deterministic order, so
	// the ledger family's group is the chain tail when the lifecycle family's
	// intended line lands after it) BEFORE either completion runs.
	doneL, berrL := obsLedger(filepath.Join(f.dir, chainLedgerFile), gL, lL)
	doneK, berrK := obsKl(keyLifecycleLogPath(f.dir), gK, lK)
	if berrL != nil || berrK != nil {
		t.Fatalf("begin: %v / %v", berrL, berrK)
	}

	// THE CONCURRENT PART: both completions at once — each runs
	// completeDestruction (destructionWriteMu) and then the anchor dispatch
	// (destructionDispatchMu → anchorDestructionEntry → nextAnchorSeqPath
	// read-modify-write → appendAnchorEntryPathObserved) — the exact pair of
	// paths the registered residual says is unprotected. Under -race this is
	// where a memory race would be reported; see ADR-080 for what actually
	// happens instead.
	var wg sync.WaitGroup
	var eL, eK error
	wg.Add(2)
	go func() { defer wg.Done(); eL = doneL() }()
	go func() { defer wg.Done(); eK = doneK() }()
	wg.Wait()

	// Invariant 0 — exactly one window completes; the loser's completion is
	// refused with the chain-tail reason (fail-closed, never a silent loss and
	// never a partial write).
	refused, succeeded := 0, 0
	for name, e := range map[string]error{"ledger": eL, "key_lifecycle": eK} {
		switch {
		case e == nil:
			succeeded++
		case strings.Contains(e.Error(), "no longer the chain tail"):
			refused++
		default:
			t.Fatalf("%s completion failed with an UNEXPECTED error (neither success nor the documented chain-tail refusal): %v", name, e)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("want exactly 1 completed window and 1 chain-tail refusal, got %d/%d (eL=%v eK=%v)", succeeded, refused, eL, eK)
	}

	// Invariant 1 — the destruction log stays VERIFIABLE and holds exactly two
	// groups: one COMPLETED pair, one refused INTENDED-ONLY group; nothing else,
	// and no unusable (torn/forged) group.
	dst, err := loadDestructionState(f.sched.destructionConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !dst.verifiable {
		t.Fatalf("the destruction log must stay verifiable: %v", dst.errs)
	}
	completed, intendedOnly := 0, 0
	for _, g := range dst.groups {
		if !g.usable {
			t.Fatalf("destruction_seq %d became unusable (%s): %s", g.seq, g.verdict, g.detail)
		}
		switch g.state {
		case destructionStateCompleted:
			completed++
		case destructionStateIntended:
			intendedOnly++
		default:
			t.Fatalf("destruction_seq %d is in state %q — neither completed nor loudly intended", g.seq, g.state)
		}
	}
	if completed != 1 || intendedOnly != 1 {
		t.Fatalf("want 1 completed group (the winner) and 1 refused intended-only group (the loser), got completed=%d intendedOnly=%d", completed, intendedOnly)
	}

	// Invariant 2 — the refusal is LOUD on the read face: the refused group
	// surfaces as destruction_unconfirmed (never folded into confirmed or
	// accounted), so an operator can see the accounting degradation.
	view, err := f.sched.DestructionView()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Unconfirmed) != 1 {
		t.Fatalf("the read face must report the refused window as unconfirmed: want 1, got %v", view.Unconfirmed)
	}

	// Invariant 3 — the destruction-anchor stream: zero conflicting anchor_seq
	// (two concurrent dispatches allocating the same seq is the shape A7-⑦
	// predicted), every entry verified, and exactly one NEW entry (the refused
	// window dispatches nothing — the conservative degradation ADR-080
	// registers).
	anchors, err := loadAnchorStatePath(destructionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors.conflicts) != 0 {
		t.Fatalf("the destruction-anchor stream must stay conflict-free under concurrent dispatch: %v", anchors.conflicts)
	}
	if len(anchors.unusable) != 0 {
		t.Fatalf("the destruction-anchor stream holds %d unusable entry(ies): %+v", len(anchors.unusable), anchors.unusable)
	}
	if want := len(beforeAnchors.latest) + 1; len(anchors.latest) != want {
		t.Fatalf("want %d destruction anchor entries (one fixture entry + one per COMPLETED window), got %d", want, len(anchors.latest))
	}
}
