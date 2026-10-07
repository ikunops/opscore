package server

// Phase 49 — Anchor Realization tests (T317~T336; ADR-073 §5, ADR-074 §7).
//
// The fixture family drives the REAL scheduler with EVERY anchored family on
// (newP46Fixture: publications + KAK lifecycle + verification attestation +
// destruction accountability + acceptance ledger), so every anchor entry under
// test is produced by production code. Forgeries, tail removals and prefix
// rewrites are done by writing bytes on disk — the only way those states are
// reachable.
//
// The core discriminator of this Phase is that an anchor entry is an ASSERTION,
// not a credential: T319 asserts, in ONE case, that a forged (but perfectly
// signed) entry reads `unrealized` on the NEW face while P46 still reads
// family_intact and P47 still reads converged — so "this is a new mechanism, not
// a reuse of P46/P47" is a measured claim, not a narrative one.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixture + accessors
// ---------------------------------------------------------------------------

type p49Fixture struct{ *p46Fixture }

func newP49Fixture(t *testing.T, tune func(cfg *HistoryExportConfig)) *p49Fixture {
	t.Helper()
	return &p49Fixture{newP46Fixture(t, tune)}
}

// realization reads the face directly (the same call Status() makes).
func (f *p49Fixture) realization() anchorRealizationStatusSummary {
	return f.sched.AnchorRealizationStatus()
}

func p49Row(t *testing.T, sum anchorRealizationStatusSummary, name string) anchorRealizationFamilyStatus {
	t.Helper()
	switch name {
	case witnessFamilyLedger:
		return sum.Ledger
	case witnessFamilyKeyLifecycle:
		return sum.KeyLifecycle
	case witnessFamilyDestruction:
		return sum.Destruction
	case witnessFamilyVerification:
		return sum.Verification
	case witnessFamilyAcceptance:
		return sum.Acceptance
	case witnessFamilyProtectionDecision:
		return sum.ProtectionDecision
	}
	t.Fatalf("unknown realization family %q", name)
	return anchorRealizationFamilyStatus{}
}

// p49NonDelegated is the family set that participates in the global scalar.
func p49NonDelegated() []string {
	return []string{
		witnessFamilyLedger, witnessFamilyKeyLifecycle, witnessFamilyDestruction,
		witnessFamilyVerification, witnessFamilyAcceptance,
	}
}

// forgeAnchor signs ONE anchor entry with the EXPORT key — the A-3 adversary
// (write access + the export private key) — appends it, and optionally delivers
// it so the witness (and therefore P46/P47) sees the same world.
func (f *p49Fixture) forgeAnchor(path string, e anchorEntry, dispatch bool) anchorEntry {
	f.t.Helper()
	if e.AnchorSeq == 0 {
		seq, err := nextAnchorSeqPath(path)
		if err != nil {
			f.t.Fatal(err)
		}
		e.AnchorSeq = seq
	}
	if e.RecordedAt == "" {
		e.RecordedAt = p46At(4).Format(time.RFC3339Nano)
	}
	if err := f.sched.signer.signAnchorEntry(&e, f.dir, p46T0); err != nil {
		f.t.Fatal(err)
	}
	e.State = anchorStatePending
	if err := appendAnchorEntryPath(path, 0, e); err != nil {
		f.t.Fatal(err)
	}
	if dispatch {
		if derr := f.sched.dispatchAnchorPath(context.Background(), path, e); derr != nil {
			f.t.Fatalf("dispatch forged anchor entry: %v", derr)
		}
	}
	return e
}

// p49KeepLines rewrites a log keeping only its FIRST `keep` lines — the tail
// removal (or prefix removal, via p49KeepFrom) that no production path produces.
func p49KeepLines(t *testing.T, path string, keep int, classify groupClassifier) {
	t.Helper()
	lines, ok, err := readLogLines(path, classify)
	if err != nil || !ok {
		t.Fatalf("read %s: err=%v ok=%v", path, err, ok)
	}
	if keep < 0 || keep > len(lines) {
		t.Fatalf("keep %d of %d lines in %s", keep, len(lines), path)
	}
	if err := rewriteLogLines(path, lines[:keep]); err != nil {
		t.Fatal(err)
	}
}

// p49KeepFrom rewrites a log dropping its FIRST `drop` lines — a prefix removal
// (what a legal compaction does, but WITHOUT the accounting record).
func p49KeepFrom(t *testing.T, path string, drop int, classify groupClassifier) {
	t.Helper()
	lines, ok, err := readLogLines(path, classify)
	if err != nil || !ok {
		t.Fatalf("read %s: err=%v ok=%v", path, err, ok)
	}
	if drop < 0 || drop > len(lines) {
		t.Fatalf("drop %d of %d lines in %s", drop, len(lines), path)
	}
	if err := rewriteLogLines(path, lines[drop:]); err != nil {
		t.Fatal(err)
	}
}

// p49DrivePublications publishes n manifests through the REAL tick path and
// returns the number of publications driven.
func (f *p49Fixture) drivePublications(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.append(p46At(i+1), int64(3+i))
		f.publish(p46At(i+1), int64(2*i+1), int64(2*i+2))
	}
}

// ---------------------------------------------------------------------------
// T317 — frozen face: zero diff, and no Phase 49 vocabulary anywhere frozen
// ---------------------------------------------------------------------------

func TestP49T317FrozenFaceUnchanged(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	frozenFiles := []string{
		"internal/controlplane/server/appendonly_log.go",
		"internal/controlplane/server/snapshot_anchor.go",
		"internal/controlplane/server/snapshot_witness_reconcile.go",
		"internal/controlplane/server/snapshot_signature.go",
		"internal/controlplane/server/snapshot_chain.go",
		"internal/controlplane/server/snapshot_ledger.go",
		"internal/controlplane/server/history_export_coverage.go",
		"internal/controlplane/server/history_export_manifest.go",
	}
	// The frozen face is pinned BY CONTENT, not by vocabulary (review M3): the
	// marker scan below cannot see an unrelated rewrite, and "zero diff" is a
	// claim about BYTES. These are the sha256 of the files at the Phase 49 base
	// (45a580e); a frozen file is frozen forever, so a mismatch is a violation
	// and never a legitimate update.
	frozenSHA256 := map[string]string{
		"internal/controlplane/server/appendonly_log.go":             "f4ae703323c3c9c49f5209f2b70f287dacc8ad61cb8ff84c1c1c9aa93fcb81ee",
		"internal/controlplane/server/snapshot_anchor.go":            "eca582354359fde49c3e2b1f6e664551dbcd31ff300a310938ea2429acdebc63",
		"internal/controlplane/server/snapshot_witness_reconcile.go": "4313822e3bdfb3b2bace3eed1643469be23611097ea728e17412c4b39200f9b2",
		"internal/controlplane/server/snapshot_signature.go":         "17d9ced5dff8b31be576a9473e5175083ce03e60f21b3cfbd12f78960d406d23",
		"internal/controlplane/server/snapshot_chain.go":             "736c29b9b6ce114ca0efeb4068586b42c32af832dad20b699aadea8c7c9a47b2",
		"internal/controlplane/server/snapshot_ledger.go":            "e9efe6fd1a7013639f0539cfb2a2940957edb9201173df005b39f2a80f50d325",
		"internal/controlplane/server/history_export_coverage.go":    "478f0ad9bd9f6189f04f198b90f2493c138ad1678a0c2aaed6364d89fe133999",
		"internal/controlplane/server/history_export_manifest.go":    "86d56214e4e69a8a33b3f4293fa72e2a3c69148e300ee1f1f8f980b335892588",
		"go.mod": "6dfc9eea3dcba0f32b4ef8229b1a9a912dddf6db1891ed3b6623538f987e6b32",
		"go.sum": "48a94c452c1c0b794722b90652f70a8bd6ea4a3a82f86b50b31bb0683bd9cd27",
	}
	markers := []string{"phase 49", "anchor_realization", "realizefamily", "artifactdigest", "compactionkind"}
	for _, rel := range frozenFiles {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("frozen file %s unreadable: %v", rel, err)
		}
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != frozenSHA256[rel] {
			t.Fatalf("T317/I1 VIOLATION: frozen file %s changed:\n got %s\nwant %s", rel, got, frozenSHA256[rel])
		}
		low := strings.ToLower(string(src))
		for _, m := range markers {
			if strings.Contains(low, m) {
				t.Fatalf("T317: frozen file %s must carry no Phase 49 vocabulary (%q)", rel, m)
			}
		}
	}
	// internal/protection: zero diff (no Phase 49 vocabulary at all).
	entries, err := os.ReadDir(filepath.Join(root, "internal", "protection"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(root, "internal", "protection", e.Name()))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if strings.Contains(strings.ToLower(string(src)), "phase 49") {
			t.Fatalf("T317: internal/protection/%s must be zero-diff for Phase 49", e.Name())
		}
	}
	// go.mod / go.sum: byte-identical to the Phase 49 base (no new requirement).
	for _, rel := range []string{"go.mod", "go.sum"} {
		src, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatalf("%s unreadable: %v", rel, rerr)
		}
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != frozenSHA256[rel] {
			t.Fatalf("T317: %s must be byte-identical to the Phase 49 base:\n got %s\nwant %s", rel, got, frozenSHA256[rel])
		}
	}
	// The vocabulary itself is pinned to ADR-073 §3's words, so a silent rename
	// cannot drift the read surface past the (unchanged) P35~P48 assertions.
	want := map[string]string{
		realizationRowRealized:           "realized",
		realizationRowCompacted:          "compacted",
		realizationRowOutOfWindow:        "out_of_window",
		realizationRowUnrealized:         "unrealized",
		realizationRowIndeterminate:      "indeterminate",
		realizationFamilyNothingAssessed: "nothing_assessed",
		realizationFamilyDelegated:       "delegated",
	}
	for got, exp := range want {
		if got != exp {
			t.Fatalf("T317: the vocabulary drifted: %q != %q", got, exp)
		}
	}
}

// ---------------------------------------------------------------------------
// T318 — default deployment: the group is absent, so the document is byte-frozen
// ---------------------------------------------------------------------------

func TestP49T318DefaultDeploymentByteFrozen(t *testing.T) {
	off := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorEndpoint = "" })
	offJSON, err := json.Marshal(off.sched.Status())
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"anchor_realization", "anchor_realized", "nothing_assessed", "out_of_window", "delegated", "unusable"} {
		if bytes.Contains(offJSON, []byte(banned)) {
			t.Fatalf("T318/I3 VIOLATION: a disabled anchor must leave the status byte-identical, found %q in %s", banned, offJSON)
		}
	}
	if st := off.sched.Status(); st.AnchorRealization != nil {
		t.Fatalf("T318: a disabled anchor must leave the group nil, got %+v", st.AnchorRealization)
	}

	on := newP49Fixture(t, nil)
	onJSON, err := json.Marshal(on.sched.Status())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(onJSON, []byte(`"anchor_realization"`)) {
		t.Fatalf("T318: an enabled anchor must expose the realization group: %s", onJSON)
	}
	// The group is fully omitempty except the two always-meaningful scalars.
	if st := on.sched.Status(); st.AnchorRealization == nil {
		t.Fatal("T318: the enabled group must be reachable from the status document")
	}
	// P47's append-only discipline (T276) still holds: anchor_delivery is LAST.
	keys := p47TopLevelKeys(t, onJSON)
	if len(keys) == 0 || keys[len(keys)-1] != "anchor_delivery" {
		t.Fatalf("T318: anchor_delivery must still be the last top-level key, got %v", keys)
	}
}

// ---------------------------------------------------------------------------
// T319 — THE CORE RED CASE (A-3, export-key branch): a forged, perfectly signed
// verification anchor entry reads `unrealized` on the new face while P46 and P47
// stay green in the SAME case.
// ---------------------------------------------------------------------------

func TestP49T319ForgedVerificationAnchorUnrealized(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	vst, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil || len(vst.entries) != 1 {
		t.Fatalf("fixture: want exactly one verification report (err=%v, n=%d)", err, len(vst.entries))
	}
	real := vst.entries[0]

	// The adversary holds write access AND the export private key, so the entry
	// is signed with the key this deployment trusts: P37 verifies it and the
	// witness accepts it (that is exactly why A-3 was undetectable before). It
	// claims the SAME report identity with an ARBITRARY digest — the carrier
	// asserts that a report with this commitment exists, and none does.
	path := verificationAnchorPath(f.dir)
	forged := f.forgeAnchor(path, anchorEntry{
		Kind:         anchorKindVerification,
		ReportSeq:    real.ReportSeq,
		ReportDigest: strings.Repeat("ab", 32),
		Overall:      real.Overall,
	}, true)

	sum := f.realization()
	row := p49Row(t, sum, witnessFamilyVerification)
	if row.Verdict != realizationFamilyUnrealized {
		t.Fatalf("T319: a forged verification anchor entry must read unrealized, got %+v", row)
	}
	if row.Unrealized != 1 || row.Realized != 1 || row.Unusable != 0 {
		t.Fatalf("T319: the forged entry must be counted as unrealized (not unusable — it verifies), got %+v", row)
	}
	if sum.State != realizationStateUnrealized || sum.AnchorRealized {
		t.Fatalf("T319: the global scalar must read unrealized, got state=%q realized=%v", sum.State, sum.AnchorRealized)
	}
	if sum.Claims <= 0 {
		t.Fatalf("T319: the verdict must rest on claims actually checked, got claims=%d", sum.Claims)
	}
	if len(sum.UnrealizedFamilies) != 1 || sum.UnrealizedFamilies[0] != witnessFamilyVerification {
		t.Fatalf("T319: the unrealized family list must name verification, got %v", sum.UnrealizedFamilies)
	}

	// THE SPLIT, asserted in the same case: P46 and P47 see nothing wrong.
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyVerification, 2)
	_, res := f.postReconcile(f.sched, byFamily)
	if got := res.Families[witnessFamilyVerification].Verdict; got != witnessVerdictIntact {
		t.Fatalf("T319: P46 must still read family_intact (it never reads the main ledger), got %q", got)
	}
	if r := p47Row(t, f.sched, witnessFamilyVerification); !r.Converged || r.Anchored != 2 || r.Pending != 0 {
		t.Fatalf("T319: P47 must still read converged (delivery is not content), got %+v", r)
	}
	_ = forged
}

// ---------------------------------------------------------------------------
// T320 — same identity, wrong digest / contradicting content field
// ---------------------------------------------------------------------------

// p49VerificationWorld builds a world with exactly ONE verification report and
// returns the ledger entry it was built from.
func p49VerificationWorld(t *testing.T) (*p49Fixture, verificationLogEntry) {
	t.Helper()
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.append(p46At(2), 5)
	f.publish(p46At(1), 1, 2)
	if _, err := f.sched.AttestVerification(10); err != nil {
		t.Fatalf("attestation: %v", err)
	}
	st, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.entries) != 1 {
		t.Fatalf("fixture: want exactly one verification entry, got %d", len(st.entries))
	}
	return f, st.entries[0]
}

func TestP49T320SameIdentityWrongDigest(t *testing.T) {
	f, entry := p49VerificationWorld(t)
	// SAME identity, correct content fields, WRONG digest.
	f.forgeAnchor(verificationAnchorPath(f.dir), anchorEntry{
		Kind:         anchorKindVerification,
		ReportSeq:    entry.ReportSeq,
		ReportDigest: strings.Repeat("cd", 32),
		Overall:      entry.Overall,
	}, false)
	row := p49Row(t, f.realization(), witnessFamilyVerification)
	if row.Verdict != realizationFamilyUnrealized || row.Unrealized != 1 || row.Realized != 1 {
		t.Fatalf("T320: an anchor entry whose digest does not match its own evidence must read unrealized, got %+v", row)
	}
}

func TestP49T320SameIdentityWrongContentField(t *testing.T) {
	f, entry := p49VerificationWorld(t)
	// The CORRECT digest (recomputed from the ledger row's seven fields) but a
	// contradicting content field: the claim is still false.
	dg, err := reportDigest(&VerificationReport{
		ReportSeq: entry.ReportSeq, Subject: entry.Subject, Evaluable: entry.Evaluable,
		Items: entry.Items, Overall: entry.Overall, Reasons: entry.Reasons, VerifiedAt: entry.VerifiedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "contradicted"
	if entry.Overall == wrong {
		wrong = "unattested"
	}
	f.forgeAnchor(verificationAnchorPath(f.dir), anchorEntry{
		Kind:         anchorKindVerification,
		ReportSeq:    entry.ReportSeq,
		ReportDigest: dg,
		Overall:      wrong,
	}, false)
	row := p49Row(t, f.realization(), witnessFamilyVerification)
	if row.Verdict != realizationFamilyUnrealized || row.Unrealized != 1 || row.Realized != 1 {
		t.Fatalf("T320: a matching digest with a contradicting content field must still read unrealized, got %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T321 — ledger TAIL removed (anchor entry still present) ⇒ unrealized
// ---------------------------------------------------------------------------

func TestP49T321LedgerTailRemovedUnrealized(t *testing.T) {
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.append(p46At(2), 5)
	f.publish(p46At(1), 1, 2)
	if _, err := f.sched.AttestVerification(10); err != nil {
		t.Fatal(err)
	}
	f.append(p46At(3), 7)
	f.publish(p46At(2), 3, 4)
	if _, err := f.sched.AttestVerification(10); err != nil {
		t.Fatal(err)
	}
	if n := p49AnchorCount(t, verificationAnchorPath(f.dir)); n != 2 {
		t.Fatalf("fixture: want two verification anchor entries, got %d", n)
	}

	// Remove the LAST ledger line: report_seq 2 survives only as a CLAIM.
	p49KeepLines(t, verificationLogPath(f.dir), 1, verificationGroupOf)

	sum := f.realization()
	row := p49Row(t, sum, witnessFamilyVerification)
	if row.Verdict != realizationFamilyUnrealized || row.Unrealized != 1 || row.Realized != 1 {
		t.Fatalf("T321: a removed ledger tail must read unrealized, got %+v", row)
	}
	// P46 stays intact: its window runs on the ANCHOR seq axis and never reads
	// the main ledger's content.
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyVerification, 2)
	_, res := f.postReconcile(f.sched, byFamily)
	if got := res.Families[witnessFamilyVerification].Verdict; got != witnessVerdictIntact {
		t.Fatalf("T321: P46 must stay family_intact (its window is the anchor seq axis), got %q", got)
	}
}

func p49AnchorCount(t *testing.T, path string) int {
	t.Helper()
	st, err := loadAnchorStatePath(path, filepath.Dir(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(st.latest)
}

// ---------------------------------------------------------------------------
// T322 — legal prefix compaction (accounted) is `compacted`, never unrealized
// ---------------------------------------------------------------------------

func TestP49T322LegalPrefixCompactionIsCompacted(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()

	sum := f.realization()
	row := p49Row(t, sum, witnessFamilyLedger)
	if row.Compacted != 1 || row.Unrealized != 0 || row.OutOfWindow != 0 {
		t.Fatalf("T322/A4: a legally compacted prefix must read compacted (never unrealized), got %+v", row)
	}
	if row.Verdict != realizationFamilyRealized {
		t.Fatalf("T322: the family may still be realized while one row is compacted, got %+v", row)
	}
	if row.Checked < 2 || row.Realized < 2 {
		t.Fatalf("T322: the two surviving publications must actually be checked, got %+v", row)
	}
	if sum.State != realizationStateRealized || !sum.AnchorRealized {
		t.Fatalf("T322: the global scalar must still be realized, got %q", sum.State)
	}
}

// ---------------------------------------------------------------------------
// T323 — destruction face OFF + absent prefix ⇒ out_of_window, LOUD
// ---------------------------------------------------------------------------

func TestP49T323OutOfWindowIsLoud(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.LedgerCapacity = 2
		cfg.DestructionLog = false
		cfg.AcceptanceLog = false
	})
	f.drivePublications(3) // publication #3 legally compacts #1, with NO accounting

	sum := f.realization()
	row := p49Row(t, sum, witnessFamilyLedger)
	if row.OutOfWindow != 1 {
		t.Fatalf("T323/A5: an unaccounted prefix must read out_of_window, got %+v", row)
	}
	if row.Compacted != 0 || row.Unrealized != 0 {
		t.Fatalf("T323: out_of_window must not be folded into compacted or unrealized, got %+v", row)
	}
	if row.Reason == "" {
		t.Fatal("T323/A5: an unjudgeable row must carry its reason on the same face")
	}
	if row.Verdict == realizationFamilyUnrealized {
		t.Fatal("T323: a disabled destruction face must never turn a legal compaction into a forgery claim")
	}
}

// ---------------------------------------------------------------------------
// T324 — fail-closed + family isolation
// ---------------------------------------------------------------------------

func TestP49T324FailClosedAndFamilyIsolation(t *testing.T) {
	// (a) the ANCHOR side: one unclassifiable line poisons exactly one family.
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	baseline := f.realization()
	if baseline.State != realizationStateRealized {
		t.Fatalf("fixture: the driven world must be realized, got %q", baseline.State)
	}
	if err := appendLogLine(verificationAnchorPath(f.dir), []byte("this is not an anchor entry")); err != nil {
		t.Fatal(err)
	}
	sum := f.realization()
	if row := p49Row(t, sum, witnessFamilyVerification); row.Verdict != realizationFamilyIndeterminate || row.Reason == "" {
		t.Fatalf("T324: an unreadable anchor stream must read indeterminate and say why, got %+v", row)
	}
	if sum.State != realizationStateIndeterminate || sum.AnchorRealized {
		t.Fatalf("T324: the global scalar must read indeterminate, got %q", sum.State)
	}
	for _, name := range []string{witnessFamilyLedger, witnessFamilyKeyLifecycle, witnessFamilyDestruction, witnessFamilyAcceptance} {
		if got, want := p49RowJSON(t, sum, name), p49RowJSON(t, baseline, name); got != want {
			t.Fatalf("T324/I8: family %q must be UNAFFECTED by another family's failure:\n got %s\nwant %s", name, got, want)
		}
	}

	// (b) the MAIN LEDGER side: an unclassifiable line poisons exactly one family.
	g := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	g.driveAllFamilies()
	gBase := g.realization()
	if err := appendLogLine(keyLifecycleLogPath(g.dir), []byte("not json at all")); err != nil {
		t.Fatal(err)
	}
	gSum := g.realization()
	if row := p49Row(t, gSum, witnessFamilyKeyLifecycle); row.Verdict != realizationFamilyIndeterminate || row.Reason == "" {
		t.Fatalf("T324/I8: an unreadable main ledger must read indeterminate and say why, got %+v", row)
	}
	if gSum.AnchorRealized {
		t.Fatal("T324: a fail-closed family must never leave the global scalar realized")
	}
	for _, name := range []string{witnessFamilyLedger, witnessFamilyDestruction, witnessFamilyVerification, witnessFamilyAcceptance} {
		if got, want := p49RowJSON(t, gSum, name), p49RowJSON(t, gBase, name); got != want {
			t.Fatalf("T324/I8: family %q must be UNAFFECTED:\n got %s\nwant %s", name, got, want)
		}
	}
}

// p49RowJSON renders one row for comparison (the window pointer makes plain
// struct equality useless, and the JSON is what the read face actually serves).
func p49RowJSON(t *testing.T, sum anchorRealizationStatusSummary, name string) string {
	t.Helper()
	data, err := json.Marshal(p49Row(t, sum, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// T325 — the per-family artifact digests; verification MUST recompute the
// 7-field report digest, never the 11-field entry digest (I9)
// ---------------------------------------------------------------------------

func TestP49T325ArtifactDigestRecompute(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()

	st, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil || len(st.entries) == 0 {
		t.Fatalf("load verification: %v (%d entries)", err, len(st.entries))
	}
	e := st.entries[0]
	got, derr := realizationVerificationArtifactDigest(e)
	if derr != nil {
		t.Fatal(derr)
	}
	want, werr := reportDigest(&VerificationReport{
		ReportSeq: e.ReportSeq, Subject: e.Subject, Evaluable: e.Evaluable,
		Items: e.Items, Overall: e.Overall, Reasons: e.Reasons, VerifiedAt: e.VerifiedAt,
	})
	if werr != nil {
		t.Fatal(werr)
	}
	if got != want {
		t.Fatalf("T325/I9: the verification artifact digest must be the 7-field report digest:\n got %s\nwant %s", got, want)
	}
	alt, aerr := verificationEntryDigest(&e)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if alt == got {
		t.Fatal("T325/I9: reportDigest (7 fields) and verificationEntryDigest (11 fields) must be different serializations")
	}
	// The anchor entry's claim must equal the recomputed value — and the family
	// must actually have compared it.
	ast, aerr2 := loadAnchorStatePath(verificationAnchorPath(f.dir), f.dir, f.sched.trust)
	if aerr2 != nil {
		t.Fatal(aerr2)
	}
	found := false
	for _, seq := range ast.seqs() {
		if ast.latest[seq].ReportSeq == e.ReportSeq {
			found = true
			if ast.latest[seq].ReportDigest != got {
				t.Fatalf("T325: the anchor claim must equal the recomputed report digest")
			}
		}
	}
	if !found {
		t.Fatal("fixture: the report's anchor entry is missing")
	}

	sum := f.realization()
	for _, name := range p49NonDelegated() {
		row := p49Row(t, sum, name)
		if row.Verdict != realizationFamilyRealized || row.Checked < 1 {
			t.Fatalf("T325: family %q must be realized by an actual comparison, got %+v", name, row)
		}
	}
}

// ---------------------------------------------------------------------------
// T326 — `realized` is non-vacuous; the empty set is NEVER realized (M1)
// ---------------------------------------------------------------------------

func TestP49T326RealizedNonVacuousAndEmptyNotTrue(t *testing.T) {
	// (a) a real tick, every family realized by an actual comparison.
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	sum := f.realization()
	if sum.State != realizationStateRealized || !sum.AnchorRealized {
		t.Fatalf("T326(a): want state=realized, got %q (realized=%v)", sum.State, sum.AnchorRealized)
	}
	if sum.Claims <= 0 {
		t.Fatalf("T326(a): anchor_realized must imply claims > 0, got %d", sum.Claims)
	}
	compared := 0
	for _, name := range p49NonDelegated() {
		row := p49Row(t, sum, name)
		if row.Verdict != realizationFamilyRealized {
			t.Fatalf("T326(a): family %q must be realized, got %+v", name, row)
		}
		compared += row.Checked
	}
	if compared != sum.Claims || compared == 0 {
		t.Fatalf("T326(a): claims must be the sum of the families' checked rows (%d != %d)", compared, sum.Claims)
	}

	// (b) every non-delegated family's anchor window is EMPTY: nothing assessed.
	g := newP49Fixture(t, nil) // anchoring ON, nothing ever anchored
	s2 := g.realization()
	if s2.State != realizationStateNothingAssessed || s2.AnchorRealized {
		t.Fatalf("T326(b): an empty row set must read nothing_assessed, got %q (realized=%v)", s2.State, s2.AnchorRealized)
	}
	if s2.Claims != 0 {
		t.Fatalf("T326(b): claims must be 0, got %d", s2.Claims)
	}
	for _, name := range p49NonDelegated() {
		row := p49Row(t, s2, name)
		if row.Verdict != realizationFamilyNothingAssessed || row.Reason == "" {
			t.Fatalf("T326(b)/A5: family %q must read nothing_assessed with a reason, got %+v", name, row)
		}
	}
	if s2.ProtectionDecision.Verdict != realizationFamilyDelegated {
		t.Fatalf("T326(b): the sixth family must read delegated, got %+v", s2.ProtectionDecision)
	}
}

// ---------------------------------------------------------------------------
// T327 — the `ledger` family's declared VACUITY (A8-①): same key on both sides
// ---------------------------------------------------------------------------

func TestP49T327LedgerFamilyIsVacuous(t *testing.T) {
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.publish(p46At(1), 1, 2)

	// The adversary holds the EXPORT key — the SAME key that signs the chain
	// ledger (snapshot_ledger.go: "same key as manifests") — so it can forge the
	// ledger row AND the anchor entry that claims it. This is the family's
	// declared vacuity: it is registered here, not hidden.
	rogue := ledgerEntry{
		PublicationID:  42,
		ManifestDigest: strings.Repeat("de", 32),
		RecordedAt:     p46At(2).Format(time.RFC3339Nano),
	}
	if err := f.sched.signer.signLedgerEntry(&rogue, p46At(2)); err != nil {
		t.Fatal(err)
	}
	raw, err := serializeLedgerEntryBytes(&rogue)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendLogLine(filepath.Join(f.dir, chainLedgerFile), raw); err != nil {
		t.Fatal(err)
	}
	f.forgeAnchor(anchorLogPath(f.dir), anchorEntry{
		PublicationID:  42,
		ManifestDigest: rogue.ManifestDigest,
	}, false)

	row := p49Row(t, f.realization(), witnessFamilyLedger)
	if row.Verdict != realizationFamilyRealized || row.Realized < 2 {
		t.Fatalf("T327: the ledger family is VACUOUS by construction (same key both sides) and must read realized, got %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T328 — the sixth family is DELEGATED; the two faces never contradict
// ---------------------------------------------------------------------------

func TestP49T328ProtectionDecisionDelegated(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(3)
	f.tick()

	st := f.sched.Status()
	if st.AnchorRealization == nil {
		t.Fatal("T328: the realization group must be present while anchoring is on")
	}
	row := st.AnchorRealization.ProtectionDecision
	if row.Verdict != realizationFamilyDelegated {
		t.Fatalf("T328/I10: the sixth family must read delegated, got %+v", row)
	}
	if row.Checked != 0 || row.Realized != 0 || row.Unrealized != 0 || row.Compacted != 0 || row.OutOfWindow != 0 {
		t.Fatalf("T328/I10: a delegated family makes NO row-level claim, got %+v", row)
	}
	for _, name := range st.AnchorRealization.UnrealizedFamilies {
		if name == witnessFamilyProtectionDecision {
			t.Fatal("T328/I10: the delegated family must not participate in the global scalar")
		}
	}
	for _, name := range st.AnchorRealization.IndeterminateFamilies {
		if name == witnessFamilyProtectionDecision {
			t.Fatal("T328/I10: the delegated family must not participate in the global scalar")
		}
	}
	// P48's own face still produces its (strictly stronger) verdict — the two
	// faces do not conflict, because `delegated` asserts nothing.
	if got := f.state(); got != decisionLogStateAttested {
		t.Fatalf("T328: P48 must still produce its own verdict, got %q", got)
	}
	if !f.attested() {
		t.Fatal("T328: P48's conjunction must still hold on an intact chain")
	}
	// ...and a TAMPERED chain moves P48 while the realization face keeps saying
	// `delegated` — no contradiction, because delegated is not a green light.
	p48TruncateTail(t, decisionLogPath(f.dir), 1)
	if got := f.state(); got == decisionLogStateAttested {
		t.Fatalf("T328: P48 must move off attested after the tail removal, got %q", got)
	}
	if got := f.sched.Status().AnchorRealization.ProtectionDecision.Verdict; got != realizationFamilyDelegated {
		t.Fatalf("T328/I10: the realization face must stay delegated (it makes no claim), got %q", got)
	}
}

// ---------------------------------------------------------------------------
// T329 — an UNUSABLE anchor entry has a landing place and never reads green
// ---------------------------------------------------------------------------

func TestP49T329UnusableAnchorEntryNeverGreen(t *testing.T) {
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.append(p46At(2), 5)
	f.publish(p46At(1), 1, 2)
	if _, err := f.sched.AttestVerification(10); err != nil {
		t.Fatal(err)
	}
	path := verificationAnchorPath(f.dir)
	seq, err := nextAnchorSeqPath(path)
	if err != nil {
		t.Fatal(err)
	}
	e := anchorEntry{
		AnchorSeq:    seq,
		Kind:         anchorKindVerification,
		ReportSeq:    999,
		ReportDigest: strings.Repeat("ab", 32),
		Overall:      "attested",
		RecordedAt:   p46At(3).Format(time.RFC3339Nano),
	}
	if err := f.sched.signer.signAnchorEntry(&e, f.dir, p46At(3)); err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(e.Sig)
	if err != nil {
		t.Fatal(err)
	}
	sig[0] ^= 0xff // well-formed, no longer verifiable
	e.Sig = base64.StdEncoding.EncodeToString(sig)
	e.State = anchorStatePending
	if err := appendAnchorEntryPath(path, 0, e); err != nil {
		t.Fatal(err)
	}

	sum := f.realization()
	row := p49Row(t, sum, witnessFamilyVerification)
	if row.Unusable < 1 {
		t.Fatalf("T329: a failing signature must be counted as unusable, got %+v", row)
	}
	if row.Verdict != realizationFamilyIndeterminate {
		t.Fatalf("T329: an unusable entry must make the family indeterminate, got %+v", row)
	}
	if row.Verdict == realizationFamilyRealized || sum.AnchorRealized {
		t.Fatal("T329: 'we cannot trust this entry' must never read green")
	}
}

// ---------------------------------------------------------------------------
// T330 — zero side effects (I4)
// ---------------------------------------------------------------------------

type p49FileState struct {
	sum  string
	size int64
	mod  int64
}

func p49FilesSnapshot(t *testing.T, dirs ...string) map[string]p49FileState {
	t.Helper()
	out := map[string]p49FileState{}
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(data)
			out[path] = p49FileState{sum: hex.EncodeToString(sum[:]), size: info.Size(), mod: info.ModTime().UnixNano()}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("snapshot %s: %v", dir, err)
		}
	}
	return out
}

func TestP49T330ZeroSideEffects(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	// Any use of the witness transport during a READ is an alarm.
	f.sched.anchorTransport = &p46AlarmTransport{t: t}

	before := p49FilesSnapshot(t, f.dir, f.witnessDir)
	_ = f.realization()
	_ = f.sched.Status()
	after := p49FilesSnapshot(t, f.dir, f.witnessDir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("T330/I4: the read face must not touch a single byte or mtime\nbefore=%v\nafter=%v", before, after)
	}
}

// ---------------------------------------------------------------------------
// T331 — zero new families / streams / routes; the realization table is CLOSED
// ---------------------------------------------------------------------------

func TestP49T331NoNewFamiliesStreamsOrRoutes(t *testing.T) {
	if got := len(witnessFamilyRegistry()); got != 6 {
		t.Fatalf("T331/I2: the family registry must still hold six families, got %d", got)
	}
	if got := len(anchorDeliveryStreams()); got != 6 {
		t.Fatalf("T331/I2: the partition must still hold six streams (T277), got %d", got)
	}
	if got := len(deliverySweepStreams()); got != 4 {
		t.Fatalf("T331/I2: deliverySweepStreams must still hold four streams (T283), got %d", got)
	}
	// The realization table is isomorphic to the registry: every registered
	// family has exactly one row (closed table, I10).
	for _, fam := range witnessFamilyRegistry() {
		var probe anchorRealizationStatusSummary
		if !assignRealizationRow(&probe, fam.Name, familyRealization{name: fam.Name, verdict: realizationFamilyDelegated}) {
			t.Fatalf("T331/I10: registry family %q has no realization row — the closed table drifted", fam.Name)
		}
	}
	// Zero new routes: the group rides the EXISTING scheduler route.
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.publish(p46At(1), 1, 2)
	srv, token := newProtectionTestServer(t, false)
	srv.historyScheduler = f.sched
	h := srv.ProtectionReadMux()
	r := httptest.NewRequest(http.MethodGet, "/management/v1/protection/alerts/history/export/scheduler", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("T331: the existing scheduler route must serve the group, got %d (%s)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"anchor_realization"`)) {
		t.Fatalf("T331: the group must be served by the EXISTING route, body=%s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// T332 — the three-way discrimination (A4/I6)
// ---------------------------------------------------------------------------

func TestP49T332ThreeWayDiscrimination(t *testing.T) {
	// (1) accounted legal compaction ⇒ compacted.
	compacted := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	compacted.drivePublications(3) // publication #3 legally compacts #1
	// (2) the SAME construction with the accounting face off ⇒ out_of_window.
	unaccounted := newP49Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.LedgerCapacity = 2
		cfg.DestructionLog = false
		cfg.AcceptanceLog = false
	})
	unaccounted.drivePublications(3)
	// (3) the SAME construction with the ledger tail removed ⇒ unrealized.
	tail := newP49Fixture(t, nil)
	tail.drivePublications(3)
	p49KeepLines(t, filepath.Join(tail.dir, chainLedgerFile), 2, ledgerGroupOf)

	r1 := p49Row(t, compacted.realization(), witnessFamilyLedger)
	r2 := p49Row(t, unaccounted.realization(), witnessFamilyLedger)
	r3 := p49Row(t, tail.realization(), witnessFamilyLedger)

	if r1.Compacted != 1 || r1.OutOfWindow != 0 || r1.Unrealized != 0 {
		t.Fatalf("T332: (1) accounted prefix must read compacted, got %+v", r1)
	}
	if r2.OutOfWindow != 1 || r2.Compacted != 0 || r2.Unrealized != 0 {
		t.Fatalf("T332: (2) unaccounted prefix must read out_of_window, got %+v", r2)
	}
	if r3.Unrealized != 1 || r3.Compacted != 0 || r3.OutOfWindow != 0 {
		t.Fatalf("T332: (3) a removed tail must read unrealized, got %+v", r3)
	}
	if r1.Verdict != realizationFamilyRealized || r2.Verdict != realizationFamilyRealized || r3.Verdict != realizationFamilyUnrealized {
		t.Fatalf("T332: family verdicts must be realized/realized/unrealized, got %q/%q/%q", r1.Verdict, r2.Verdict, r3.Verdict)
	}
	// The three row signatures are pairwise distinct — the discrimination itself.
	tuple := func(r anchorRealizationFamilyStatus) string {
		return fmt.Sprintf("c=%d/ow=%d/u=%d", r.Compacted, r.OutOfWindow, r.Unrealized)
	}
	if tuple(r1) == tuple(r2) || tuple(r2) == tuple(r3) || tuple(r1) == tuple(r3) {
		t.Fatalf("T332/I6: the three states must not be confused: %s %s %s", tuple(r1), tuple(r2), tuple(r3))
	}
}

// ---------------------------------------------------------------------------
// T333 — identity boundaries: below min / inside-but-absent / inside-mismatch /
// above max
// ---------------------------------------------------------------------------

func TestP49T333IdentityBoundaries(t *testing.T) {
	// A world with FIVE publications (identities 1..5) and no compaction
	// accounting, so the only variable is WHICH ledger lines survive. Each
	// construction keeps a different subset, so each boundary position gets its
	// own counter signature.
	build := func(keep []int) *p49Fixture {
		f := newP49Fixture(t, func(cfg *HistoryExportConfig) {
			cfg.DestructionLog = false
			cfg.AcceptanceLog = false
		})
		f.drivePublications(5)
		path := filepath.Join(f.dir, chainLedgerFile)
		lines, ok, err := readLogLines(path, ledgerGroupOf)
		if err != nil || !ok || len(lines) != 5 {
			t.Fatalf("fixture: want five ledger lines, got %d (err=%v ok=%v)", len(lines), err, ok)
		}
		kept := make([]logLine, 0, len(keep))
		for _, i := range keep {
			kept = append(kept, lines[i])
		}
		if err := rewriteLogLines(path, kept); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// (a) all five kept: every identity is present and at the window's min/max.
	a := p49Row(t, build([]int{0, 1, 2, 3, 4}).realization(), witnessFamilyLedger)
	if a.Realized != 5 || a.Unrealized != 0 || a.OutOfWindow != 0 || a.Compacted != 0 {
		t.Fatalf("T333: (a) all identities present must all read realized, got %+v", a)
	}
	// The row's window is the family's ANCHOR window (P46 vocabulary): five
	// anchor entries, seq 1..5.
	if a.Window == nil || a.Window.MinSeq != 1 || a.Window.MaxSeq != 5 || a.Window.Entries != 5 {
		t.Fatalf("T333: the row must expose the ANCHOR window [1,5], got %+v", a.Window)
	}

	// (b) the FIRST ledger line removed: identity 1 is BELOW the window's lower
	// bound with no accounting ⇒ out_of_window (unjudgeable, never a forgery
	// claim).
	b := p49Row(t, build([]int{1, 2, 3, 4}).realization(), witnessFamilyLedger)
	if b.OutOfWindow != 1 || b.Realized != 4 || b.Unrealized != 0 || b.Compacted != 0 {
		t.Fatalf("T333: (b) an identity below the window must read out_of_window, got %+v", b)
	}

	// (c) the SECOND ledger line removed: identity 2 is INSIDE the window but
	// absent ⇒ unrealized (a legal prefix compaction cannot explain a hole in
	// the retained range).
	c := p49Row(t, build([]int{0, 2, 3, 4}).realization(), witnessFamilyLedger)
	if c.Unrealized != 1 || c.Realized != 4 || c.OutOfWindow != 0 || c.Compacted != 0 {
		t.Fatalf("T333: (c) an identity inside the window with no record must read unrealized, got %+v", c)
	}

	// (d) the LAST TWO ledger lines removed: identities 4 and 5 are ABOVE the
	// window ⇒ unrealized (the ledger's tail was removed while the anchor
	// entries, which are written AFTER the ledger rows, remain).
	d := p49Row(t, build([]int{0, 1, 2}).realization(), witnessFamilyLedger)
	if d.Unrealized != 2 || d.Realized != 3 || d.OutOfWindow != 0 || d.Compacted != 0 {
		t.Fatalf("T333: (d) an identity above the window must read unrealized, got %+v", d)
	}

	// The four boundary positions are pairwise distinguishable on this face.
	sig := func(r anchorRealizationFamilyStatus) string {
		return fmt.Sprintf("r=%d/u=%d/ow=%d/c=%d", r.Realized, r.Unrealized, r.OutOfWindow, r.Compacted)
	}
	seen := map[string]bool{}
	for _, r := range []anchorRealizationFamilyStatus{a, b, c, d} {
		if seen[sig(r)] {
			t.Fatalf("T333: two boundary positions share the signature %s — the four states must not be confused", sig(r))
		}
		seen[sig(r)] = true
	}
}

// ---------------------------------------------------------------------------
// T334 — interval accounting: closed interval, no over-cover, kind must match,
// only `completed` counts
// ---------------------------------------------------------------------------

func TestP49T334IntervalAccounting(t *testing.T) {
	f := newP49Fixture(t, nil)
	c := f.sched.destructionConfig()
	if !c.writable() {
		t.Fatal("fixture: the KAK must be writable")
	}
	record := func(kind string, from, to int64, complete bool) {
		t.Helper()
		intent, err := beginDestruction(c, kind, "test", nil,
			[]destructionTarget{{FromSeq: from, ToSeq: to, PrefixDigest: strings.Repeat("aa", 32)}}, 1, p46T0)
		if err != nil {
			t.Fatalf("begin destruction: %v", err)
		}
		if !complete {
			return // stays `intended`
		}
		if err := completeDestruction(c, intent.DestructionSeq, destructionStateCompleted, p46T0); err != nil {
			t.Fatalf("complete destruction: %v", err)
		}
	}
	record(destructionKindLedgerCompaction, 5, 7, true)  // completed [5,7]
	record(destructionKindLedgerCompaction, 9, 10, true) // adjacent completed [9,10]
	record(destructionKindLedgerCompaction, 20, 21, false)

	// An ABORTED group: open it, then close it as aborted.
	intent, err := beginDestruction(c, destructionKindLedgerCompaction, "test", nil,
		[]destructionTarget{{FromSeq: 30, ToSeq: 31, PrefixDigest: strings.Repeat("bb", 32)}}, 1, p46T0)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeDestruction(c, intent.DestructionSeq, destructionStateAborted, p46T0); err != nil {
		t.Fatal(err)
	}

	acc := loadRealizationAccounting(f.sched)
	if acc.err != nil {
		t.Fatalf("T334: the accounting must load, got %v", acc.err)
	}
	cases := []struct {
		id   int64
		want bool
		why  string
	}{
		{5, true, "closed interval: from_seq is covered"},
		{6, true, "closed interval: interior"},
		{7, true, "closed interval: to_seq is covered"},
		{4, false, "just below the interval"},
		{8, false, "just above the interval"},
		{9, true, "the adjacent interval is itself covered"},
		{10, true, "the adjacent interval's to_seq"},
		{11, false, "adjacent intervals must not over-cover"},
		{20, false, "an intended (never completed) group accounts for nothing"},
		{21, false, "an intended group's to_seq accounts for nothing"},
		{30, false, "an aborted group accounts for nothing"},
		{31, false, "an aborted group's to_seq accounts for nothing"},
	}
	for _, tc := range cases {
		if got := acc.covers(destructionKindLedgerCompaction, tc.id); got != tc.want {
			t.Fatalf("T334: covers(%d) = %v, want %v (%s)", tc.id, got, tc.want, tc.why)
		}
	}
	// The kind must match the family's own compaction kind.
	if acc.covers(destructionKindKeyLifecycleCompaction, 6) {
		t.Fatal("T334: an accounting of one kind must never cover another family's identity")
	}
	// A disabled destruction face is an EMPTY table, never an error (T323).
	g := newP49Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.DestructionLog = false
		cfg.AcceptanceLog = false
	})
	empty := loadRealizationAccounting(g.sched)
	if empty.err != nil {
		t.Fatalf("T334: a disabled destruction face must be an empty table, got %v", empty.err)
	}
	if empty.covers(destructionKindLedgerCompaction, 6) {
		t.Fatal("T334: an empty table must cover nothing")
	}
}

// ---------------------------------------------------------------------------
// T335 — malformed inputs: no panic, always fail-closed
// ---------------------------------------------------------------------------

func TestP49T335MalformedInputsDoNotPanic(t *testing.T) {
	t.Run("empty world", func(t *testing.T) {
		f := newP49Fixture(t, nil)
		sum := f.realization()
		if sum.State != realizationStateNothingAssessed {
			t.Fatalf("T335: an empty world must read nothing_assessed, got %q", sum.State)
		}
	})

	t.Run("zero identity", func(t *testing.T) {
		f := newP49Fixture(t, nil)
		// A perfectly signed entry whose evidence identity is ZERO: the claim is
		// unjudgeable, never silently realized.
		e := anchorEntry{
			AnchorSeq: 1, Kind: anchorKindVerification, ReportSeq: 0,
			ReportDigest: strings.Repeat("ab", 32), Overall: "attested",
			RecordedAt: p46At(1).Format(time.RFC3339Nano),
		}
		if err := f.sched.signer.signAnchorEntry(&e, f.dir, p46At(1)); err != nil {
			t.Fatal(err)
		}
		e.State = anchorStatePending
		if err := appendAnchorEntryPath(verificationAnchorPath(f.dir), 0, e); err != nil {
			t.Fatal(err)
		}
		row := p49Row(t, f.realization(), witnessFamilyVerification)
		if row.Verdict != realizationFamilyIndeterminate || row.Reason == "" {
			t.Fatalf("T335: a zero identity must read indeterminate with a reason, got %+v", row)
		}
	})

	t.Run("invalid json in the anchor stream", func(t *testing.T) {
		f := newP49Fixture(t, nil)
		if err := appendLogLine(anchorLogPath(f.dir), []byte("{not json")); err != nil {
			t.Fatal(err)
		}
		row := p49Row(t, f.realization(), witnessFamilyLedger)
		if row.Verdict != realizationFamilyIndeterminate {
			t.Fatalf("T335: an unreadable anchor stream must read indeterminate, got %+v", row)
		}
	})

	t.Run("invalid json in a main ledger", func(t *testing.T) {
		// M3: the ledger is only read while there is a claim to check against it.
		// Seed a REAL claim first, or the family is correctly just
		// `nothing_assessed` (the case T339 pins) and the corruption is never
		// observed because nothing needed observing.
		f, _ := p49VerificationWorld(t)
		if err := appendLogLine(verificationLogPath(f.dir), []byte("][ ")); err != nil {
			t.Fatal(err)
		}
		row := p49Row(t, f.realization(), witnessFamilyVerification)
		if row.Verdict != realizationFamilyIndeterminate {
			t.Fatalf("T335: an unreadable main ledger must read indeterminate when the family has a claim to check, got %+v", row)
		}
	})

	t.Run("over-long field", func(t *testing.T) {
		f := newP49Fixture(t, nil)
		e := anchorEntry{
			AnchorSeq: 1, Kind: anchorKindVerification, ReportSeq: 5,
			ReportDigest: strings.Repeat("z", 200000), Overall: strings.Repeat("o", 100000),
			RecordedAt: p46At(1).Format(time.RFC3339Nano),
		}
		if err := f.sched.signer.signAnchorEntry(&e, f.dir, p46At(1)); err != nil {
			t.Fatal(err)
		}
		e.State = anchorStatePending
		if err := appendAnchorEntryPath(verificationAnchorPath(f.dir), 0, e); err != nil {
			t.Fatal(err)
		}
		row := p49Row(t, f.realization(), witnessFamilyVerification)
		if row.Verdict == realizationFamilyRealized {
			t.Fatalf("T335: an absurd claim about an absent report must never read realized, got %+v", row)
		}
	})

	t.Run("zero-value entry", func(t *testing.T) {
		f := newP49Fixture(t, nil)
		if err := appendLogLine(anchorLogPath(f.dir), []byte(`{"anchor_seq":7}`)); err != nil {
			t.Fatal(err)
		}
		sum := f.realization() // must not panic
		if sum.AnchorRealized {
			t.Fatal("T335: a zero-value anchor entry must never read green")
		}
	})
}

// ---------------------------------------------------------------------------
// T336 — the mutation campaign (MU1~MU6)
// ---------------------------------------------------------------------------
//
// EXECUTED, not narrated. Each mutation was applied to the production source,
// its own tests were run, the tests turned RED in every case, and the file was
// restored and re-hashed against the PRE-mutation value:
//
//	snapshot_anchor_realization.go  sha256 6a59a5f1d3b7b32681eca8573fa80d765a5e94d39d3479388a2870a20d8a1e5a
//	                                (mutated -> RED -> restored -> same sha256; MU1/MU2/MU3/MU4/MU6/MU7/MU8/MU9)
//	history_export_scheduler.go     sha256 b2b67121f4400f7dd95805fdd1b08c8b9e05b505706bf03fc5af5b2f46994bcb
//	                                (mutated -> RED -> restored -> same sha256; MU5)
//
// Command per mutation: `go test -count=1 -run '<the named tests>' -timeout 20m ./internal/controlplane/server/`
//
//	MU1  drop the recomputed-digest comparison (`false && dg != claim`, the field
//	     comparison kept so the mutant still compiles)
//	     => RED: TestP49T319ForgedVerificationAnchorUnrealized,
//	             TestP49T320SameIdentityWrongDigest
//	MU2  fold out_of_window into unrealized (`res.outOfWindow++` -> `res.unrealized++`)
//	     => RED: TestP49T323OutOfWindowIsLoud, TestP49T332ThreeWayDiscrimination
//	MU3  drop fail-closed (return the zero verdict instead of indeterminate on a
//	     family's anchor-stream load error)
//	     => RED: TestP49T324FailClosedAndFamilyIsolation
//	MU4  use verificationEntryDigest (11 fields) instead of the recomputed
//	     reportDigest (7 fields)
//	     => RED: TestP49T325ArtifactDigestRecompute
//	MU5  remove the ledger family's vacuity declaration (ArtifactDigest -> nil,
//	     i.e. delegate it so the vacuity is no longer stated)
//	     => RED: TestP49T327LedgerFamilyIsVacuous
//	MU6  give the read face a side effect (write a file inside the export dir;
//	     the two imports are added so the mutant compiles)
//	     => RED: TestP49T330ZeroSideEffects
//
// The three mutations below close the GAPS the first adversarial review found —
// each one is the defect CLASS the review reported, so the matrix and the review
// are pinned to the same mechanisms:
//
//	MU7  stop honouring the delegation (`delegated := false`): the sixth family is
//	     then judged like the others and a corrupt delegated anchor stream makes it
//	     indeterminate
//	     => RED: TestP49T337DelegatedFamilyNeverIndeterminate
//	MU8  drop the "the family's own ledger source is disabled" guard, so a silent
//	     empty destruction load reads as an empty window (window 0,0) and every
//	     anchor becomes a tail-deletion claim
//	     => RED: TestP49T338DisabledDestructionLedgerIsUnjudgeable
//	MU9  drop the "an empty anchor window needs no ledger" early return, so a
//	     family with nothing to check is dragged to indeterminate by a ledger it
//	     never needed (permanent indeterminate in any no-KAK deployment)
//	     => RED: TestP49T339EmptyAnchorWindowNeedsNoLedger
//
// The campaign itself is NOT a Go test (it mutates the code under test). This
// function is the structural guard that keeps the matrix from rotting: every
// mechanism a mutation targets must still be present in the shipped code.
func TestP49T336MutationMatrixDocumented(t *testing.T) {
	// MU2 guard: the three row states must not have collapsed into one word.
	if realizationRowOutOfWindow == realizationRowUnrealized || realizationRowCompacted == realizationRowUnrealized {
		t.Fatal("MU2: the row vocabulary collapsed")
	}
	if realizationStateNothingAssessed == realizationStateRealized {
		t.Fatal("MU2/M1: nothing_assessed must not be realized")
	}
	// MU3 guard: an unreadable line must not classify.
	if _, ok := anchorGroupOf([]byte("not json")); ok {
		t.Fatal("MU3: an unreadable anchor line must not classify")
	}
	if _, ok := keyLifecycleGroupOf([]byte("not json")); ok {
		t.Fatal("MU3: an unreadable ledger line must not classify")
	}
	// MU4 guard: the two canonical serializations must stay distinct.
	e := verificationLogEntry{V: 1, ReportSeq: 1, Overall: "attested", VerifiedAt: p46At(1).Format(time.RFC3339Nano)}
	rep, rerr := realizationVerificationArtifactDigest(e)
	if rerr != nil {
		t.Fatal(rerr)
	}
	ent, eerr := verificationEntryDigest(&e)
	if eerr != nil {
		t.Fatal(eerr)
	}
	if rep == ent {
		t.Fatal("MU4: the verification artifact digest must be the report digest, not the entry digest")
	}
	// MU5 guard: the ledger family's vacuity is DECLARED — it is judged by this
	// face (ArtifactDigest non-nil), not silently delegated away.
	declared := false
	for _, fam := range witnessFamilyRegistry() {
		if fam.Name != witnessFamilyLedger {
			continue
		}
		if fam.ArtifactDigest == nil {
			t.Fatal("MU5: the ledger family must state its (vacuous) realization, not delegate it")
		}
		if fam.CompactionKind != destructionKindLedgerCompaction {
			t.Fatalf("MU5: the ledger family's accounting kind drifted to %q", fam.CompactionKind)
		}
		declared = true
	}
	if !declared {
		t.Fatal("MU5: the ledger family vanished from the registry")
	}
	// MU7 guard: the delegation is DATA-DRIVEN — the sixth family's ArtifactDigest
	// is nil in the registry. That is the fact the delegation-FIRST ordering rests
	// on (T337 pins the ordering itself behaviourally).
	sawDelegated := false
	for _, fam := range witnessFamilyRegistry() {
		if fam.Name != witnessFamilyProtectionDecision {
			continue
		}
		if fam.ArtifactDigest != nil {
			t.Fatal("MU7: the sixth family must be registered as DELEGATED (ArtifactDigest nil)")
		}
		sawDelegated = true
	}
	if !sawDelegated {
		t.Fatal("MU7: the sixth family vanished from the registry")
	}
	// MU8/MU9 guards: the three "we cannot say" family values must stay distinct
	// words. Collapsing them is exactly how an unjudgeable or a disabled-source
	// family would get laundered into a deletion claim (T338) or a failure (T339).
	if realizationFamilyNothingAssessed == realizationFamilyUnrealized ||
		realizationFamilyNothingAssessed == realizationFamilyIndeterminate ||
		realizationRowOutOfWindow == realizationFamilyNothingAssessed {
		t.Fatal("MU8/MU9: the 'cannot say' vocabulary collapsed")
	}
	// MU1 guard: the comparison is the ONLY thing that can turn a matching
	// identity into a verdict — with a wrong claim the row must not be realized.
	f := newP49Fixture(t, nil)
	f.append(p46At(1), 3)
	f.publish(p46At(1), 1, 2)
	if _, err := f.sched.AttestVerification(10); err != nil {
		t.Fatal(err)
	}
	vst, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil || len(vst.entries) != 1 {
		t.Fatalf("fixture: %v (%d entries)", err, len(vst.entries))
	}
	f.forgeAnchor(verificationAnchorPath(f.dir), anchorEntry{
		Kind: anchorKindVerification, ReportSeq: vst.entries[0].ReportSeq,
		ReportDigest: strings.Repeat("00", 32), Overall: vst.entries[0].Overall,
	}, false)
	if got := p49Row(t, f.realization(), witnessFamilyVerification); got.Unrealized != 1 {
		t.Fatalf("MU1 guard: a wrong digest claim must be caught by the comparison, got %+v", got)
	}
	// MU6 guard: the face is read-only — asserted end-to-end by T330.
	if f.sched.cfg.Dir == "" {
		t.Fatal("MU6: the read face must not need a writable directory at all")
	}
}

// ---------------------------------------------------------------------------
// T337 — the DELEGATED family is never indeterminate, and never vetoes the five
// families this face does judge (review M1)
// ---------------------------------------------------------------------------
//
// ADR-073 §3 and ADR-074 I10 say the sixth family is `delegated` UNCONDITIONALLY
// and takes part in no step of the global computation. That ordering is
// load-bearing: if the delegation test ran AFTER the anchor-side fail-closed
// gates, a corrupt decision ANCHOR stream would make the delegated family
// `indeterminate`, and the global scalar would be dragged to `indeterminate` by a
// family this face has explicitly refused to judge. The corruption below is the
// cheapest possible: one unclassifiable line in the decision anchor stream.
func TestP49T337DelegatedFamilyNeverIndeterminate(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(3)
	f.tick()

	base := f.sched.Status().AnchorRealization
	if base == nil {
		t.Fatal("T337: the realization group must be present while anchoring is on")
	}
	if base.ProtectionDecision.Verdict != realizationFamilyDelegated {
		t.Fatalf("T337: the sixth family must read delegated on an intact stream, got %+v", base.ProtectionDecision)
	}
	baseState := base.State
	// Snapshot the five families this face DOES judge, so the corruption below can
	// be shown to leave them untouched (the p48 fixture's own global state is not
	// assumed to be any particular value — the claim under test is ISOLATION).
	baseRows := map[string]string{}
	for _, name := range p49NonDelegated() {
		baseRows[name] = p49RowJSON(t, *base, name)
	}

	// Corrupt the DECISION ANCHOR STREAM (not the decision log): a line the anchor
	// classifier cannot read.
	if err := appendLogLine(decisionAnchorPath(f.dir), []byte("this is not an anchor entry")); err != nil {
		t.Fatal(err)
	}

	sum := f.sched.AnchorRealizationStatus()
	if got := sum.ProtectionDecision.Verdict; got != realizationFamilyDelegated {
		t.Fatalf("T337/M1: a corrupt DELEGATED anchor stream must not move the verdict off delegated, got %q", got)
	}
	for _, name := range sum.IndeterminateFamilies {
		if name == witnessFamilyProtectionDecision {
			t.Fatal("T337/M1: the delegated family must never enter the indeterminate list")
		}
	}
	for _, name := range sum.UnrealizedFamilies {
		if name == witnessFamilyProtectionDecision {
			t.Fatal("T337/M1: the delegated family must never enter the unrealized list")
		}
	}
	if sum.State != baseState {
		t.Fatalf("T337/M1: the delegated family must not move the global scalar: %q -> %q", baseState, sum.State)
	}
	for _, name := range p49NonDelegated() {
		if got := p49RowJSON(t, sum, name); got != baseRows[name] {
			t.Fatalf("T337/M1: family %q must be UNAFFECTED by a corrupt DELEGATED anchor stream:\n got %s\nwant %s", name, got, baseRows[name])
		}
	}
}

// ---------------------------------------------------------------------------
// T338 — a family whose OWN ledger source is disabled is UNJUDGEABLE, never a
// deletion claim (review M2 / ADR-073 A8-②)
// ---------------------------------------------------------------------------
//
// The destruction load returns a silent, error-free EMPTY state when the face is
// off. Read literally, zeros mean "window (0,0)", so every anchor claim becomes
// "above the upper bound" — i.e. tail deletion — and an ordinary config change
// (turn `--export-destruction-log` off after anchors were written) would be
// reported as a FORGERY. ADR-073 A8-② forbids exactly that: with no accounting,
// an absent prefix is `out_of_window` (unjudgeable, loud), never `unrealized`.
func TestP49T338DisabledDestructionLedgerIsUnjudgeable(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()

	// Fixture sanity: the destruction family really has anchor claims to check,
	// and they are judgeable while the face is on.
	if n := p49AnchorCount(t, destructionAnchorPath(f.dir)); n == 0 {
		t.Fatal("T338 fixture: want at least one destruction anchor entry")
	}
	if row := p49Row(t, f.realization(), witnessFamilyDestruction); row.Verdict == realizationFamilyNothingAssessed {
		t.Fatalf("T338 fixture: the destruction family must be judgeable with the face on, got %+v", row)
	}

	// A supported config change: the operator turns the destruction face off. The
	// anchor stream and the destruction log both SURVIVE on disk; only the switch
	// changed. (AcceptanceLog must be off too: P45's construction guard G4 refuses
	// an acceptance ledger without a destruction ledger.)
	cfg := f.cfg
	cfg.DestructionLog = false
	cfg.AcceptanceLog = false
	sched2, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatalf("T338: rebuilding the scheduler on the same dir must be legal: %v", err)
	}

	sum := sched2.AnchorRealizationStatus()
	row := sum.Destruction
	if row.Verdict == realizationFamilyUnrealized || row.Unrealized != 0 {
		t.Fatalf("T338/A8-2: a disabled destruction ledger must never turn its anchors into a deletion claim, got %+v", row)
	}
	if row.OutOfWindow == 0 {
		t.Fatalf("T338/A5: claims that cannot be judged must be counted as unjudgeable, got %+v", row)
	}
	if row.Compacted != 0 {
		t.Fatalf("T338: nothing was accounted for, so nothing may read compacted, got %+v", row)
	}
	if row.Reason == "" {
		t.Fatal("T338/A5: unjudgeable must be loud on the same face")
	}
	if row.Verdict != realizationFamilyNothingAssessed {
		t.Fatalf("T338: an unjudgeable family has checked nothing, so it is nothing_assessed, got %+v", row)
	}
	if sum.State == realizationStateUnrealized {
		t.Fatal("T338/A8-2: the global scalar must not read unrealized because a face is switched off")
	}
	if sum.AnchorRealized && sum.Claims <= 0 {
		t.Fatal("T338/I5: anchor_realized still implies claims > 0")
	}
}

// ---------------------------------------------------------------------------
// T339 — an EMPTY anchor window needs no ledger: a family with nothing to judge
// reads nothing_assessed, not indeterminate (review M3 / ADR-073 §3)
// ---------------------------------------------------------------------------
//
// The ledger load runs fail-closed, which is right for a family that HAS claims.
// But the load must not be reached when there is nothing to check: a deployment
// with no key authority cannot VERIFY the acceptance ledger (its load returns an
// error), so ordering the ledger load before the empty-window test made the whole
// realization face read `indeterminate` forever — in a deployment that never
// anchored a single acceptance claim. ADR-073 §3 says an empty anchor window is
// `anchor_nothing_assessed`; the empty case is silence, not failure.
func TestP49T339EmptyAnchorWindowNeedsNoLedger(t *testing.T) {
	f := newP48Fixture(t, nil) // anchoring on, no key authority at all
	f.emit(3)
	f.tick()

	sum := f.sched.AnchorRealizationStatus()

	// The acceptance ledger cannot be verified here (no KAK) — but nothing was
	// ever anchored for that family, so the family is not "unjudgeable", it is
	// simply empty.
	acc := sum.Acceptance
	if acc.Verdict == realizationFamilyIndeterminate {
		t.Fatalf("T339/M3: a family with no anchor claims must not be dragged to indeterminate by a ledger it never needed, got %+v", acc)
	}
	if acc.Verdict != realizationFamilyNothingAssessed {
		t.Fatalf("T339/M3: an empty anchor window is nothing_assessed (ADR-073 §3), got %+v", acc)
	}
	if acc.Reason == "" {
		t.Fatal("T339/A5: an empty window still owes a reason")
	}
	for _, name := range sum.IndeterminateFamilies {
		if name == witnessFamilyAcceptance {
			t.Fatal("T339/M3: an empty-window family must not enter the indeterminate list")
		}
	}
	if sum.State == realizationStateIndeterminate {
		t.Fatalf("T339/M3: a deployment with no key authority must not read indeterminate on this face, got %q (%v)", sum.State, sum.IndeterminateFamilies)
	}
	// Silence is not a green light either: nothing was checked for that family.
	if acc.Checked != 0 || acc.Realized != 0 || acc.Unrealized != 0 {
		t.Fatalf("T339: an empty window makes no claim at all, got %+v", acc)
	}
}
