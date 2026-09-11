package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 39 helpers
// ---------------------------------------------------------------------------

func (f *chainFixture) ledgerPath() string { return filepath.Join(f.snapDir, chainLedgerFile) }

func (f *chainFixture) ledgerLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func (f *chainFixture) writeLedgerLines(t *testing.T, lines []string) {
	t.Helper()
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(f.ledgerPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *chainFixture) ledgerIDs(t *testing.T) []int64 {
	t.Helper()
	var ids []int64
	for _, ln := range f.ledgerLines(t) {
		var e ledgerEntry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			continue
		}
		ids = append(ids, e.PublicationID)
	}
	return ids
}

// newLedgerFixture is like newChainFixture but with an explicit ledger capacity.
func newLedgerFixture(t *testing.T, capacity int) *chainFixture {
	t.Helper()
	root := t.TempDir()
	snapDir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{snapDir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	privPath, pubPath, _, _ := genKeyPair(t, keyDir, "keyA")
	st := &fakeExportStore{}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: snapDir, Interval: time.Hour, Formats: []string{"json"},
		SignKeyPath: privPath, TrustKeyPaths: []string{pubPath}, LedgerCapacity: capacity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &chainFixture{snapDir: snapDir, sched: s, store: st, privPath: privPath, pubPath: pubPath}
}

// ---------------------------------------------------------------------------
// T104 — a published manifest also lands in the ledger, consistently
// ---------------------------------------------------------------------------
func TestLedgerRecordsPublishedManifests(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	ids := f.ledgerIDs(t)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ledger ids = %v, want [1 2]", ids)
	}
	// Entry digest must equal the manifest digest (one canonical form).
	m := mustLoadManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	want, err := ledgerDigestOf(m)
	if err != nil {
		t.Fatal(err)
	}
	var e ledgerEntry
	if err := json.Unmarshal([]byte(f.ledgerLines(t)[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.ManifestDigest != want {
		t.Fatalf("ledger digest = %q, want the manifest digest %q", e.ManifestDigest, want)
	}
	if e.PrevPublicationID != 0 {
		t.Fatalf("first entry prev = %d, want 0", e.PrevPublicationID)
	}

	res, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
	for _, r := range res {
		if r.ChainSource != "disk+ledger" {
			t.Fatalf("chain_source for %s = %q, want disk+ledger", r.Snapshot, r.ChainSource)
		}
	}
}

// ---------------------------------------------------------------------------
// T105 — pruning a chain node no longer breaks the chain (the core MUST)
// ---------------------------------------------------------------------------
func TestLedgerPruneOfChainNodeStaysOK(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	f.drop(t, at(0)) // retention reclaims the oldest snapshot; ledger keeps its commitment

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("a pruned chain node must not break the chain: %+v", verdict)
	}
	if verdict.LedgerEntriesUsed == 0 {
		t.Fatal("the backtrack must have consumed the pruned node's ledger entry")
	}
	if verdict.VerifiableFromPublicationID != 1 {
		t.Fatalf("verifiable_from = %d, want 1", verdict.VerifiableFromPublicationID)
	}
}

// ---------------------------------------------------------------------------
// T106 / T112 — bounded ledger: eviction shrinks the range, it never breaks
// ---------------------------------------------------------------------------
func TestLedgerEvictionShrinksRange(t *testing.T) {
	f := newLedgerFixture(t, 1) // keep only the newest entry
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	if ids := f.ledgerIDs(t); len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("ledger ids = %v, want [3] (capacity 1)", ids)
	}
	f.drop(t, at(0))
	f.drop(t, at(1))

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("evicted ledger entries must shrink the range, not break it: %+v", verdict)
	}
	if verdict.VerifiableFromPublicationID != 3 {
		t.Fatalf("verifiable_from = %d, want 3 (the oldest provable node)", verdict.VerifiableFromPublicationID)
	}
	if verdict.LedgerEntriesUsed != 0 {
		t.Fatalf("ledger_entries_used = %d, want 0 (nothing provable below)", verdict.LedgerEntriesUsed)
	}
}

// ---------------------------------------------------------------------------
// T107 — a break INSIDE the retained set is still detected
// ---------------------------------------------------------------------------
func TestLedgerDoesNotHideInternalBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)
	f.tick(t, at(3), 31, 40)

	f.drop(t, at(1)) // a middle node disappears, its successor still commits to it

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("an internal break must still be detected: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T108 / T109 — untrustworthy ledger entries are exposed, never ignored
// ---------------------------------------------------------------------------
func TestLedgerUntrustedEntriesAreExposed(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Tamper the SECOND ledger entry's business field: its signature no longer
	// verifies, so it must not be usable — and must not vanish silently.
	lines := f.ledgerLines(t)
	var e ledgerEntry
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	e.ManifestDigest = strings.Repeat("ff", 32)
	raw, err := serializeLedgerEntryBytes(&e)
	if err != nil {
		t.Fatal(err)
	}
	f.writeLedgerLines(t, []string{lines[0], strings.TrimSpace(string(raw))})

	_, verdict := mustVerifyDetailed(t, f.sched)
	if len(verdict.LedgerErrors) == 0 {
		t.Fatal("an untrustworthy ledger entry must be exposed in ledger_errors")
	}
	if !strings.Contains(strings.Join(verdict.LedgerErrors, " "), "signature_invalid") {
		t.Fatalf("ledger_errors = %v, want a signature_invalid diagnostic", verdict.LedgerErrors)
	}
}

// T109 — an unknown key is reported as key_unknown, never collapsed to absent.
func TestLedgerUnknownKeyIsNotAbsent(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)

	keyDir := t.TempDir()
	_, otherPub, _, _ := genKeyPair(t, keyDir, "other")
	s2, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: f.store, Dir: f.snapDir, Interval: time.Hour, Formats: []string{"json"},
		TrustKeyPaths: []string{otherPub},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, verdict := mustVerifyDetailed(t, s2)
	joined := strings.Join(verdict.LedgerErrors, " ")
	if !strings.Contains(joined, "key_unknown") {
		t.Fatalf("ledger_errors = %v, want key_unknown (never collapsed to absent)", verdict.LedgerErrors)
	}
}

// ---------------------------------------------------------------------------
// T111 — disk and ledger disagreeing about one id is a replacement
// ---------------------------------------------------------------------------
func TestLedgerDiskDigestMismatchBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Re-sign an entry for id 2 that claims a DIFFERENT digest (a valid
	// signature over a contradictory statement).
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	fake := ledgerEntry{
		PublicationID:     2,
		ManifestDigest:    strings.Repeat("ab", 32),
		PrevPublicationID: 1,
		RecordedAt:        at(1).Format(time.RFC3339Nano),
	}
	if err := signer.signLedgerEntry(&fake, at(1)); err != nil {
		t.Fatal(err)
	}
	raw, err := serializeLedgerEntryBytes(&fake)
	if err != nil {
		t.Fatal(err)
	}
	f.writeLedgerLines(t, []string{f.ledgerLines(t)[0], strings.TrimSpace(string(raw))})

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("disk/ledger digest disagreement must break: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T122 — a duplicate id with conflicting digests is a conflict, not last-write
// ---------------------------------------------------------------------------
func TestLedgerConflictingDuplicateIDBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	line1 := f.ledgerLines(t)[1]
	conflicting := ledgerEntry{
		PublicationID:     2,
		ManifestDigest:    strings.Repeat("cd", 32),
		PrevPublicationID: 1,
		RecordedAt:        at(1).Format(time.RFC3339Nano),
	}
	if err := signer.signLedgerEntry(&conflicting, at(1)); err != nil {
		t.Fatal(err)
	}
	raw, err := serializeLedgerEntryBytes(&conflicting)
	if err != nil {
		t.Fatal(err)
	}
	// Both statements are validly signed but contradict each other.
	f.writeLedgerLines(t, []string{f.ledgerLines(t)[0], line1, strings.TrimSpace(string(raw))})

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("a conflicting duplicate id must break the chain (no last-write-wins): %+v", verdict)
	}
	if !strings.Contains(strings.Join(verdict.LedgerErrors, " "), "conflicting") {
		t.Fatalf("ledger_errors = %v, want a conflict diagnostic", verdict.LedgerErrors)
	}
}

// ---------------------------------------------------------------------------
// T114 — no signing key ⇒ no chain, no ledger (default output unchanged)
// ---------------------------------------------------------------------------
func TestLedgerAbsentWhenSigningDisabled(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp, MinSeq: 1, MaxSeq: 3}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())

	if _, err := os.Stat(filepath.Join(dir, chainLedgerFile)); !os.IsNotExist(err) {
		t.Fatalf("no ledger may be written when signing is disabled (stat err=%v)", err)
	}
	if _, verdict := mustVerifyDetailed(t, s); verdict.Verdict != "chain_absent" {
		t.Fatalf("chain = %+v, want chain_absent", verdict)
	}
}

// ---------------------------------------------------------------------------
// T115 — ledger-backed verification is strictly read-only
// ---------------------------------------------------------------------------
func TestLedgerVerifyIsReadOnly(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.drop(t, at(0))

	before := dirFingerprint(t, f.snapDir)
	mustVerifyDetailed(t, f.sched)
	if after := dirFingerprint(t, f.snapDir); after != before {
		t.Fatal("ledger-backed verification mutated the directory")
	}
}

// ---------------------------------------------------------------------------
// T117 / T118 — eviction and prefix deletion are indistinguishable, and neither
// is ever reported as tampering (Scope 必修 1)
// ---------------------------------------------------------------------------
func TestLedgerPrefixLossIsNotTamperEvidence(t *testing.T) {
	evict := func(t *testing.T) *chainFixture {
		f := newChainFixture(t)
		for i := 0; i < 3; i++ {
			f.tick(t, at(i), int64(i*10+1), int64(i*10+10))
		}
		// Reclaim the oldest snapshot AND its ledger entry (a legal eviction).
		f.drop(t, at(0))
		lines := f.ledgerLines(t)
		f.writeLedgerLines(t, lines[1:])
		return f
	}
	deleted := func(t *testing.T) *chainFixture {
		f := newChainFixture(t)
		for i := 0; i < 3; i++ {
			f.tick(t, at(i), int64(i*10+1), int64(i*10+10))
		}
		// Same on-disk shape, but the prefix was REMOVED by an actor.
		f.drop(t, at(0))
		lines := f.ledgerLines(t)
		f.writeLedgerLines(t, lines[1:])
		return f
	}

	for name, mk := range map[string]func(*testing.T) *chainFixture{"eviction": evict, "prefix_deletion": deleted} {
		t.Run(name, func(t *testing.T) {
			f := mk(t)
			_, verdict := mustVerifyDetailed(t, f.sched)
			if verdict.Verdict != "chain_ok" {
				t.Fatalf("prefix loss must shrink the range, not break: %+v", verdict)
			}
			if verdict.VerifiableFromPublicationID != 2 {
				t.Fatalf("verifiable_from = %d, want 2", verdict.VerifiableFromPublicationID)
			}
			blob := strings.ToLower(verdict.Detail + " " + strings.Join(verdict.LedgerErrors, " "))
			for _, banned := range []string{"tamper", "deleted", "evicted", "lost"} {
				if strings.Contains(blob, banned) {
					t.Fatalf("prefix loss must not be narrated as %q: %q", banned, blob)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T119 — manifest-first crash: no phantom evidence, and recovery backfills
// ---------------------------------------------------------------------------
func TestLedgerManifestFirstCrashRecovers(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Simulate the crash window: the manifest is durably published but its
	// ledger append never happened.
	lines := f.ledgerLines(t)
	f.writeLedgerLines(t, lines[:1]) // drop id=2's entry

	if ids := f.ledgerIDs(t); len(ids) != 1 {
		t.Fatalf("precondition: ledger ids = %v, want [1]", ids)
	}

	f.tick(t, at(2), 21, 30) // next tick performs idempotent recovery, then publishes 3

	ids := f.ledgerIDs(t)
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("ledger ids = %v, want [1 2 3] (recovery backfilled 2)", ids)
	}
	if _, verdict := mustVerifyDetailed(t, f.sched); verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
}

// ---------------------------------------------------------------------------
// T120 — a ledger entry that precedes its manifest never enters the chain
// ---------------------------------------------------------------------------
func TestLedgerFirstEntryDoesNotJoinTheChain(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.drop(t, at(0)) // the backtrack must walk id=1's real entry

	// An orphan entry for an id that was never published (phantom evidence).
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	phantom := ledgerEntry{
		PublicationID:     42,
		ManifestDigest:    strings.Repeat("11", 32),
		PrevPublicationID: 1,
		RecordedAt:        at(0).Format(time.RFC3339Nano),
	}
	if err := signer.signLedgerEntry(&phantom, at(0)); err != nil {
		t.Fatal(err)
	}
	raw, err := serializeLedgerEntryBytes(&phantom)
	if err != nil {
		t.Fatal(err)
	}
	f.writeLedgerLines(t, append(f.ledgerLines(t), strings.TrimSpace(string(raw))))

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("an orphan ledger entry must not disturb the chain: %+v", verdict)
	}
	if verdict.LedgerEntriesUsed != 1 {
		t.Fatalf("ledger_entries_used = %d, want 1 (only the real node is walked)", verdict.LedgerEntriesUsed)
	}
	for _, id := range verdict.BrokenAt {
		if id == 42 {
			t.Fatal("the phantom entry must never enter the chain")
		}
	}
}

// T116 — a failed publication leaves no ledger entry behind
func TestLedgerNotWrittenWhenManifestFails(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	before := len(f.ledgerLines(t))

	f.sched.beforeManifestSign = func(string) { f.sched.signer.priv = nil }
	f.tick(t, at(1), 11, 20)

	if after := len(f.ledgerLines(t)); after != before {
		t.Fatalf("ledger grew on a failed publication: %d -> %d", before, after)
	}
}

// T121 — recovery can never bless a tampered manifest
func TestLedgerRecoveryRefusesTamperedManifest(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)

	m := mustLoadManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	m.MinSeq = 999 // tamper: the signature no longer verifies

	if err := f.sched.recordLedgerEntry(m, at(0)); err == nil {
		t.Fatal("recovery must refuse to record a manifest whose signature does not verify")
	}
	if ids := f.ledgerIDs(t); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("precondition: the original entry should be untouched, got %v", ids)
	}
}

// T113 — crash gap vs deletion (Phase 38 discriminator) still holds with a ledger
func TestLedgerKeepsCrashGapDiscriminator(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.burn(t, 3)
	f.tick(t, at(2), 21, 30)

	if _, verdict := mustVerifyDetailed(t, f.sched); verdict.Verdict != "chain_ok" {
		t.Fatalf("a legal crash gap must stay chain_ok: %+v", verdict)
	}

	f2 := newChainFixture(t)
	f2.tick(t, at(0), 1, 10)
	f2.tick(t, at(1), 11, 20)
	f2.tick(t, at(2), 21, 30)
	f2.tick(t, at(3), 31, 40)
	f2.drop(t, at(2)) // delete a PUBLISHED node mid-chain

	if _, verdict := mustVerifyDetailed(t, f2.sched); verdict.Verdict != "chain_broken" {
		t.Fatalf("a deleted published node must break the chain: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T123 — a normal append never rewrites the ledger, and a malformed line is
// never washed away as a side effect of recording something new (R205).
// ---------------------------------------------------------------------------
func TestLedgerAppendDoesNotWashMalformedEvidence(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)

	// Inject a malformed line after the valid one.
	f.writeLedgerLines(t, append(f.ledgerLines(t), "{ this-is-not-json"))

	f.tick(t, at(1), 11, 20) // a legitimate publication

	lines := f.ledgerLines(t)
	foundBad := false
	for _, ln := range lines {
		if strings.Contains(ln, "this-is-not-json") {
			foundBad = true
		}
		if strings.Contains(ln, "\"publication_id\":2") {
			t.Fatalf("the append must be refused while unparseable evidence is present, got a line for id 2: %s", ln)
		}
	}
	if !foundBad {
		t.Fatalf("the malformed line was silently discarded: %v", lines)
	}
	if st := f.sched.Status(); strings.TrimSpace(st.LedgerError) == "" {
		t.Fatal("a refused append must be surfaced as ledger_error")
	}
}

// ---------------------------------------------------------------------------
// T124 — capacity compaction is an explicit PREFIX eviction only; it never
// reinterprets or rebuilds entries, and survivors stay byte-identical.
// ---------------------------------------------------------------------------
func TestLedgerCompactionIsPrefixOnly(t *testing.T) {
	f := newLedgerFixture(t, 2)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	before := f.ledgerLines(t)
	f.tick(t, at(2), 21, 30) // third publication ⇒ compaction to the newest two
	after := f.ledgerLines(t)

	if len(after) != 2 {
		t.Fatalf("ledger lines = %d, want 2 (capacity)", len(after))
	}
	if ids := f.ledgerIDs(t); ids[0] != 2 || ids[1] != 3 {
		t.Fatalf("ledger ids = %v, want [2 3] (oldest prefix evicted only)", ids)
	}
	// The surviving PRE-EXISTING line must be byte-identical to its original:
	// compaction copies, it never reinterprets or rebuilds.
	if after[0] != before[1] {
		t.Fatalf("compaction rewrote a pre-existing entry:\n got %s\nwant %s", after[0], before[1])
	}
	if strings.Contains(strings.Join(after, ""), `"publication_id":1`) {
		t.Fatal("the evicted prefix must be gone")
	}
}
