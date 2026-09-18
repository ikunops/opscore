package server

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 40 — shared append-only log primitive + the Phase 39 ledger migration.
//
// The golden fixture `testdata/p40_ledger_golden.jsonl` was captured from the
// PRE-migration ledger implementation (R209 step 1, judge's ordering): a
// deterministic key plus fixed timestamps is replayed through a representative
// event sequence, and the resulting file bytes were frozen. Everything below
// asserts that the migration changed the plumbing and NOT the bytes.
// ---------------------------------------------------------------------------

// deterministicSigner builds a signer from a fixed seed. Ed25519 signing is
// deterministic (RFC 8032), so a fixed seed + fixed timestamps make the emitted
// bytes reproducible on every platform and in every run.
func deterministicSigner(t *testing.T) *exportSigner {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i * 7)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("test key is not Ed25519")
	}
	return &exportSigner{priv: priv, keyID: keyIDForPublicKey(pub)}
}

// goldenDigest recreates the frozen digest for publication id i.
func goldenDigest(i int) string { return hex.EncodeToString([]byte{byte(i), 0xAA, byte(i * 3)}) }

// goldenEntries is the frozen event sequence: five chained appends.
func goldenEntries(t *testing.T) []ledgerEntry {
	t.Helper()
	s := deterministicSigner(t)
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	var out []ledgerEntry
	prevID := int64(0)
	prevDigest := ""
	for i := 1; i <= 5; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		e := ledgerEntry{
			PublicationID:      int64(i),
			ManifestDigest:     goldenDigest(i),
			PrevPublicationID:  prevID,
			PrevManifestDigest: prevDigest,
			RecordedAt:         ts.UTC().Format(time.RFC3339Nano),
		}
		if err := s.signLedgerEntry(&e, ts); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		prevID = int64(i)
		prevDigest = e.ManifestDigest
	}
	return out
}

func goldenLine(t *testing.T, e ledgerEntry) []byte {
	t.Helper()
	raw, err := serializeLedgerEntryBytes(&e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const goldenCapacity = 3

// goldenLedgerSequence replays the frozen sequence through the LEDGER entry
// point (capacity 3), then performs an idempotent re-append of the newest entry.
func goldenLedgerSequence(t *testing.T, dir string) []byte {
	t.Helper()
	for _, e := range goldenEntries(t) {
		if err := appendLedgerEntry(dir, goldenCapacity, e); err != nil {
			t.Fatalf("append id=%d: %v", e.PublicationID, err)
		}
	}
	last := goldenEntries(t)[4]
	if err := appendLedgerEntry(dir, goldenCapacity, last); err != nil {
		t.Fatalf("idempotent re-append: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, chainLedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// goldenPrimitiveSequence replays the same sequence through the SHARED
// primitive, bypassing the ledger's own conflict/idempotency logic and using
// the same classifier the ledger injects. Identical bytes prove the primitive
// reproduces the pre-migration persistence behaviour exactly.
func goldenPrimitiveSequence(t *testing.T, dir string) []byte {
	t.Helper()
	path := filepath.Join(dir, chainLedgerFile)
	for _, e := range goldenEntries(t) {
		if err := appendLogLine(path, goldenLine(t, e)); err != nil {
			t.Fatalf("primitive append id=%d: %v", e.PublicationID, err)
		}
		if err := compactLogPrefixGroups(path, goldenCapacity, ledgerGroupOf); err != nil {
			t.Fatalf("primitive compact: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ---------------------------------------------------------------------------
// T124b — ledger migration byte equivalence (ADR-053 §3.3 item 2)
// ---------------------------------------------------------------------------
func TestLedgerMigrationPreservesBytesExactly(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "p40_ledger_golden.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("golden fixture is empty")
	}

	gotLedger := goldenLedgerSequence(t, t.TempDir())
	if !bytes.Equal(gotLedger, want) {
		t.Fatalf("ledger path diverged from the pre-migration golden bytes:\n got %d bytes\nwant %d bytes\ngot:\n%s", len(gotLedger), len(want), gotLedger)
	}

	gotPrimitive := goldenPrimitiveSequence(t, t.TempDir())
	if !bytes.Equal(gotPrimitive, want) {
		t.Fatalf("primitive path diverged from the pre-migration golden bytes:\n got %d bytes\nwant %d bytes\ngot:\n%s", len(gotPrimitive), len(want), gotPrimitive)
	}
}

// ---------------------------------------------------------------------------
// T124c — a normal append is a TRUE append: bytes already on disk are never
// rewritten (ADR-053 I1).
// ---------------------------------------------------------------------------
func TestLogAppendIsATrueAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	first := []byte("{\"n\":1}\n")
	if err := appendLogLine(path, first); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendLogLine(path, []byte("{\"n\":2}\n")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("existing bytes were rewritten:\nbefore %q\nafter  %q", before, after)
	}
	if string(after) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Fatalf("append produced %q", after)
	}
}

// ---------------------------------------------------------------------------
// T124d — prefix compaction's unit is a whole GROUP: a group is dropped with
// all of its lines or kept with all of them, and survivors are copied verbatim
// (ADR-053 I3).
// ---------------------------------------------------------------------------
func TestLogCompactionKeepsWholeGroupsVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	lines := []string{"g1-a", "g1-b", "g2-a", "g3-a", "g3-b"}
	for _, l := range lines {
		if err := appendLogLine(path, []byte(l+"\n")); err != nil {
			t.Fatal(err)
		}
	}
	groupOf := func(raw []byte) (int64, bool) {
		// "g<N>-..." ⇒ group N. An intentionally non-JSON classifier: the
		// primitive must not care what a line means.
		s := string(raw)
		if len(s) < 2 || s[0] != 'g' {
			return 0, false
		}
		n := int64(s[1] - '0')
		if n <= 0 || n > 9 {
			return 0, false
		}
		return n, true
	}
	if err := compactLogPrefixGroups(path, 2, groupOf); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "g2-a\ng3-a\ng3-b\n"
	if string(got) != want {
		t.Fatalf("compaction produced %q, want %q (group 1 dropped whole, group 2 kept)", got, want)
	}
}

// ---------------------------------------------------------------------------
// T124e — [R40-4] a line the classifier cannot read makes compaction
// FAIL-CLOSED: the file is left byte-identical and an explicit error is
// returned. Both consumers (ledger, anchor) inherit this.
// ---------------------------------------------------------------------------
func TestLogCompactionRefusesUnclassifiedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	body := "g1-a\ng2-a\n{ this-is-not-json\ng3-a\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	groupOf := func(raw []byte) (int64, bool) {
		s := string(raw)
		if len(s) < 2 || s[0] != 'g' {
			return 0, false
		}
		return int64(s[1] - '0'), true
	}
	err := compactLogPrefixGroups(path, 1, groupOf)
	if err == nil {
		t.Fatal("compaction must be refused while an unclassifiable line is present")
	}
	if !strings.Contains(err.Error(), "unclassifiable") {
		t.Fatalf("error must name the reason, got %q", err.Error())
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != body {
		t.Fatalf("a refused compaction mutated the file:\n got %q\nwant %q", after, body)
	}
}

// ---------------------------------------------------------------------------
// T124f — grouping is injected, never hardcoded: two different classifiers over
// the same bytes produce two different, well-defined cuts. (A hardcoded policy
// inside the primitive would make one of these cases impossible.)
// ---------------------------------------------------------------------------
func TestLogGroupingIsInjectedNotHardcoded(t *testing.T) {
	body := "A:x\nB:x\nA:y\nC:x\n"
	byFirst := func(raw []byte) (int64, bool) {
		s := string(raw)
		if len(s) == 0 {
			return 0, false
		}
		return int64(s[0]), true
	}
	bySecond := func(raw []byte) (int64, bool) {
		s := string(raw)
		if len(s) < 3 {
			return 0, false
		}
		return int64(s[2]), true
	}

	dir1 := t.TempDir()
	p1 := filepath.Join(dir1, "log.jsonl")
	if err := os.WriteFile(p1, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// body: groups by the leading token = {A, B, C}; by the trailing token = {x, y}.
	// Keeping ONE group under each policy must cut a different set of lines.
	if err := compactLogPrefixGroups(p1, 1, byFirst); err != nil {
		t.Fatal(err)
	}
	got1, _ := os.ReadFile(p1)

	dir2 := t.TempDir()
	p2 := filepath.Join(dir2, "log.jsonl")
	if err := os.WriteFile(p2, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compactLogPrefixGroups(p2, 1, bySecond); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(p2)

	if string(got1) == string(got2) {
		t.Fatalf("both classifiers produced the same cut (%q); grouping is hardcoded", got1)
	}
	if string(got1) != "C:x\n" {
		t.Fatalf("classifier-1 cut = %q, want \"C:x\\n\" (oldest two leading-token groups dropped)", got1)
	}
	if string(got2) != "A:y\n" {
		t.Fatalf("classifier-2 cut = %q, want \"A:y\\n\" (the x group dropped whole, A:y survives)", got2)
	}
}

// ---------------------------------------------------------------------------
// T124g — reading reports unclassified lines instead of dropping them, and a
// missing file is not an error.
// ---------------------------------------------------------------------------
func TestLogReadReportsUnclassifiedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	body := "{\"a\":1}\nnot-json\n{\"a\":2}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	groupOf := func(raw []byte) (int64, bool) {
		if len(raw) == 0 || raw[0] != '{' {
			return 0, false
		}
		return int64(len(raw)), true
	}
	lines, ok, err := readLogLines(path, groupOf)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("read must report that at least one line could not be classified")
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (unclassified lines are preserved, never dropped)", len(lines))
	}
	if lines[1].classified {
		t.Fatal("the unclassifiable line must be flagged as such")
	}
	if string(lines[1].raw) != "not-json" {
		t.Fatalf("raw line = %q, want verbatim \"not-json\"", lines[1].raw)
	}

	missing := filepath.Join(dir, "absent.jsonl")
	lines2, ok2, err2 := readLogLines(missing, groupOf)
	if err2 != nil || !ok2 || len(lines2) != 0 {
		t.Fatalf("a missing file must read as empty and not an error (got %v, ok=%v, n=%d)", err2, ok2, len(lines2))
	}
}

// ---------------------------------------------------------------------------
// T123b (ledger leg) — an unclassifiable line makes the LEDGER refuse to append
// and leave the file byte-identical; the anchor leg is added with Phase 40's
// anchor persistence (same primitive, same branch).
// ---------------------------------------------------------------------------
func TestLedgerRefusesAppendWhenUnclassifiablePresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, chainLedgerFile)
	entries := goldenEntries(t)
	if err := appendLedgerEntry(dir, 0, entries[0]); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(before, []byte("{ this-is-not-json\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	poisoned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := appendLedgerEntry(dir, 0, entries[1]); err == nil {
		t.Fatal("the ledger must refuse to append while an unclassifiable line is present")
	} else if !strings.Contains(err.Error(), "unclassifiable") {
		t.Fatalf("error must name the reason, got %q", err.Error())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, poisoned) {
		t.Fatalf("a refused append mutated the ledger:\n got %q\nwant %q", after, poisoned)
	}
}
