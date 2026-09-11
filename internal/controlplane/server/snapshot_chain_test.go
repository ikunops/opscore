package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 38 helpers
// ---------------------------------------------------------------------------

type chainFixture struct {
	snapDir  string
	sched    *HistoryExportScheduler
	store    *fakeExportStore
	privPath string
	pubPath  string
}

func newChainFixture(t *testing.T) *chainFixture {
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
		SignKeyPath: privPath, TrustKeyPaths: []string{pubPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &chainFixture{snapDir: snapDir, sched: s, store: st, privPath: privPath, pubPath: pubPath}
}

// v4Manifest builds a chain-bearing manifest skeleton for hand-crafted cases.
func v4Manifest(id int64, identity string, chain *manifestChain) *snapshotManifest {
	return &snapshotManifest{
		SchemaVersion: manifestSchemaVersionV4,
		PublicationID: id,
		Snapshot:      identity,
		ExportedAt:    "2026-09-10T00:00:00Z",
		GeneratedAt:   "2026-09-10T00:00:00Z",
		Source:        "durable",
		SeqContinuity: seqContinuityValue,
		MinSeq:        1,
		MaxSeq:        10,
		Records:       10,
		Chain:         chain,
	}
}

// writeSignedManifest signs a manifest with the given signer and writes it to
// the snapshot directory (used to craft hop shapes a real publisher never emits).
func writeSignedManifest(t *testing.T, dir string, signer *exportSigner, m *snapshotManifest) {
	t.Helper()
	if err := signer.signManifest(m, chainT0); err != nil {
		t.Fatal(err)
	}
	data, err := serializeSnapshotManifestBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	name := "alert-transitions-" + m.Snapshot + ".manifest.json"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// tick publishes ONE signed snapshot with the given store provenance.
func (f *chainFixture) tick(t *testing.T, exp time.Time, minSeq, maxSeq int64) {
	t.Helper()
	f.store.mu.Lock()
	f.store.res = protection.TransitionReadResult{
		Transitions: sampleTransitions(), ExportedAt: exp, MinSeq: minSeq, MaxSeq: maxSeq,
	}
	f.store.mu.Unlock()
	f.sched.Tick(context.Background())
}

// burn advances the durable publication watermark, simulating a crashed tick
// that allocated an id without publishing (a LEGAL gap, M7).
func (f *chainFixture) burn(t *testing.T, id int64) {
	t.Helper()
	path := filepath.Join(f.snapDir, publicationStateFile)
	if err := os.WriteFile(path, []byte(strconv.FormatInt(id, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dropLedger removes the Phase 39 ledger, reproducing a pre-P39 deployment.
func (f *chainFixture) dropLedger(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.snapDir, chainLedgerFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func (f *chainFixture) drop(t *testing.T, exp time.Time) {
	t.Helper()
	base := filepath.Join(f.snapDir, "alert-transitions-"+safeTS(exp))
	for _, suffix := range []string{".manifest.json", ".json"} {
		if err := os.Remove(base + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func chainOf(t *testing.T, res []VerifyResult, exp time.Time) string {
	t.Helper()
	return chainOfID(t, res, safeTS(exp))
}

func chainOfID(t *testing.T, res []VerifyResult, identity string) string {
	t.Helper()
	for _, r := range res {
		if r.Snapshot == identity {
			return r.Chain
		}
	}
	t.Fatalf("result for %s missing: %+v", identity, res)
	return ""
}

func mustVerifyDetailed(t *testing.T, s *HistoryExportScheduler) ([]VerifyResult, ChainVerdict) {
	t.Helper()
	res, chain, err := s.VerifySnapshotsDetailed(10)
	if err != nil {
		t.Fatal(err)
	}
	return res, chain
}

var chainT0 = time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)

func at(n int) time.Time { return chainT0.Add(time.Duration(n) * time.Hour) }

// ---------------------------------------------------------------------------
// T81 — a complete chain verifies
// ---------------------------------------------------------------------------
func TestChainCompleteVerifies(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	res, chain := mustVerifyDetailed(t, f.sched)
	if chain.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", chain)
	}
	if chain.AnchorPublicationID != 1 {
		t.Fatalf("anchor = %d, want 1 (the P38 genesis)", chain.AnchorPublicationID)
	}
	if len(chain.BrokenAt) != 0 {
		t.Fatalf("broken_at = %v, want none", chain.BrokenAt)
	}
	if got := chainOf(t, res, at(0)); got != chainPosRetentionBoundary {
		t.Fatalf("first chain position = %q, want %q", got, chainPosRetentionBoundary)
	}
	for _, exp := range []time.Time{at(1), at(2)} {
		if got := chainOf(t, res, exp); got != chainPosPredecessorVerified {
			t.Fatalf("position for %v = %q, want %q", exp, got, chainPosPredecessorVerified)
		}
	}
}

// ---------------------------------------------------------------------------
// T91 — the first chain-bearing manifest declares genesis
// ---------------------------------------------------------------------------
func TestChainGenesis(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)

	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	chain, ok := m["chain"].(map[string]any)
	if !ok {
		t.Fatalf("manifest carries no chain block: %v", m)
	}
	if v, _ := chain["prev_publication_id"].(float64); int64(v) != 0 {
		t.Fatalf("genesis prev_publication_id = %v, want 0", chain["prev_publication_id"])
	}
	if _, present := chain["prev_manifest_digest"]; present {
		t.Fatal("genesis must not carry a predecessor digest")
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" || verdict.AnchorPublicationID != 1 {
		t.Fatalf("chain = %+v, want chain_ok anchored at 1", verdict)
	}
}

// ---------------------------------------------------------------------------
// T92-a — a LEGAL crash gap is not a chain gap (core discriminator)
// ---------------------------------------------------------------------------
func TestChainLegalCrashGapStaysOK(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10) // pub 1
	f.tick(t, at(1), 11, 20) // pub 2
	f.burn(t, 3)             // pub 3 allocated, never published (M7 legal gap)
	f.tick(t, at(2), 21, 30) // pub 4

	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
	chain := m["chain"].(map[string]any)
	if v, _ := chain["prev_publication_id"].(float64); int64(v) != 2 {
		t.Fatalf("pub 4 must commit to the LAST PUBLISHED manifest (2), not id-1: got %v", chain["prev_publication_id"])
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("a legal crash gap must NOT be reported as a chain break: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T92-b — post-publication deletion IS a chain break (same on-disk shape as
// T92-a, different signed commitment)
// ---------------------------------------------------------------------------
func TestChainPostPublicationDeletionBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)  // pub 1
	f.tick(t, at(1), 11, 20) // pub 2
	f.tick(t, at(2), 21, 30) // pub 3
	f.tick(t, at(3), 31, 40) // pub 4 (prev = 3)

	f.drop(t, at(2)) // delete pub 3's files

	res, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("deleting a published manifest must break the chain: %+v", verdict)
	}
	if len(verdict.BrokenAt) != 1 || verdict.BrokenAt[0] != 4 {
		t.Fatalf("broken_at = %v, want [4]", verdict.BrokenAt)
	}
	// Orthogonality: the remaining snapshots keep their own P35/P37 verdicts.
	for _, r := range res {
		if r.Snapshot == safeTS(at(3)) {
			if r.Status != "ok" {
				t.Fatalf("status = %q, want ok (chain never rewrites P35)", r.Status)
			}
			if r.Signature == nil || r.Signature.Verdict != "signature_ok" {
				t.Fatalf("signature = %+v, want signature_ok (chain never rewrites P37)", r.Signature)
			}
			if r.Chain != chainPosBroken {
				t.Fatalf("chain position = %q, want %q", r.Chain, chainPosBroken)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T83 — replacing a snapshot is caught by the digest commitment
// ---------------------------------------------------------------------------
func TestChainReplacementBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	// Replace pub 2 with a differently-valued manifest. Its signature no longer
	// verifies, so it drops out of the chain evidence entirely.
	editManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(1))+".manifest.json", func(m map[string]any) {
		m["max_seq"] = float64(9999)
	})
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("a replaced snapshot must break the chain: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T84 — the chain fields are bound by the P37 signature
// ---------------------------------------------------------------------------
func TestChainFieldsAreSigned(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Rewriting the commitment to a different-but-plausible predecessor must
	// invalidate the signature (the chain block is part of the payload).
	editManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(1))+".manifest.json", func(m map[string]any) {
		m["chain"].(map[string]any)["prev_publication_id"] = float64(0)
	})
	res, _ := mustVerifyDetailed(t, f.sched)
	for _, r := range res {
		if r.Snapshot == safeTS(at(1)) {
			if r.Signature == nil || r.Signature.Verdict != "signature_invalid" {
				t.Fatalf("signature = %+v, want signature_invalid", r.Signature)
			}
			if r.Status != "mismatch" {
				t.Fatalf("status = %q, want mismatch", r.Status)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T85 — a v3 manifest (signature, no chain) is pre-chain history
// ---------------------------------------------------------------------------
func TestChainAbsentForV3Manifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "20260910T000001Z", 101, nil) // v2 writer ⇒ no chain
	res, verdict := mustVerifyDetailed(t, covScheduler(t, dir))
	if verdict.Verdict != "chain_absent" {
		t.Fatalf("chain = %+v, want chain_absent (no chain-bearing manifests)", verdict)
	}
	if len(res) == 0 {
		t.Fatal("expected a verify result")
	}
}

// ---------------------------------------------------------------------------
// T90 — pruning PRE-CHAIN history does not break the chain
// ---------------------------------------------------------------------------
func TestChainPruneOfPreChainHistoryStaysOK(t *testing.T) {
	f := newChainFixture(t)
	// Pre-chain (v3-shaped) history that P38 never adopted.
	writeTestManifest(t, f.snapDir, "20260909T000001Z", 900, nil)
	writeTestManifest(t, f.snapDir, "20260909T000002Z", 901, nil)

	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Simulate retention removing the old pre-chain files.
	for _, n := range []string{"alert-transitions-20260909T000001Z.manifest.json", "alert-transitions-20260909T000002Z.manifest.json"} {
		if err := os.Remove(filepath.Join(f.snapDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("removing pre-chain history must not break the P38 chain: %+v", verdict)
	}
}

// ---------------------------------------------------------------------------
// T96 — the P37 → P38 migration boundary
// ---------------------------------------------------------------------------
func TestChainMigrationBoundary(t *testing.T) {
	f := newChainFixture(t)
	// Existing signed history from the previous phase (no chain block).
	writeTestManifest(t, f.snapDir, "20260909T000001Z", 101, nil)
	writeTestManifest(t, f.snapDir, "20260909T000002Z", 102, nil)
	writeTestManifest(t, f.snapDir, "20260909T000003Z", 103, nil)

	f.tick(t, at(0), 1, 10) // first v4: the P38 anchor
	f.tick(t, at(1), 11, 20)

	first := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	chain := first["chain"].(map[string]any)
	if v, _ := chain["prev_publication_id"].(float64); int64(v) != 0 {
		t.Fatalf("the first v4 must declare genesis (0), not adopt a pre-chain file: got %v", chain["prev_publication_id"])
	}
	res, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
	if got := chainOf(t, res, at(0)); got != chainPosRetentionBoundary {
		t.Fatalf("first v4 position = %q, want %q (P38 chain anchor)", got, chainPosRetentionBoundary)
	}
	if got := chainOf(t, res, at(1)); got != chainPosPredecessorVerified {
		t.Fatalf("second v4 position = %q, want %q", got, chainPosPredecessorVerified)
	}
}

// ---------------------------------------------------------------------------
// T97 — deleting the P38 anchor. Phase 39 splits this into two semantics:
// without a ledger it is a break (Phase 38 rule); with a ledger the anchor
// commitment survives and the verifiable range simply starts at the anchor.
// ---------------------------------------------------------------------------
func TestChainAnchorDeletion(t *testing.T) {
	t.Run("no_ledger_breaks", func(t *testing.T) {
		f := newChainFixture(t)
		f.tick(t, at(0), 1, 10) // anchor
		f.tick(t, at(1), 11, 20)
		f.drop(t, at(0))
		f.dropLedger(t)

		_, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_broken" {
			t.Fatalf("without a ledger, deleting the anchor must break the chain: %+v", verdict)
		}
	})

	t.Run("with_ledger_stays_verifiable", func(t *testing.T) {
		f := newChainFixture(t)
		f.tick(t, at(0), 1, 10) // anchor
		f.tick(t, at(1), 11, 20)
		f.drop(t, at(0))

		_, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_ok" {
			t.Fatalf("the ledger still holds the anchor commitment: %+v", verdict)
		}
		if verdict.VerifiableFromPublicationID != 1 {
			t.Fatalf("verifiable_from = %d, want 1 (the anchor)", verdict.VerifiableFromPublicationID)
		}
		if verdict.LedgerEntriesUsed == 0 {
			t.Fatal("the backtrack must have consumed the anchor ledger entry")
		}
	})
}

// ---------------------------------------------------------------------------
// T87 — chain and coverage are independent dimensions
// ---------------------------------------------------------------------------
func TestChainAndCoverageAreIndependent(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.burn(t, 3)
	f.tick(t, at(2), 21, 30)

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
	cov, err := f.sched.Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cov.Indeterminate.PublicationGaps) != 1 ||
		cov.Indeterminate.PublicationGaps[0] != (CoverageInterval{Min: 3, Max: 3}) {
		t.Fatalf("publication_gaps = %+v, want [[3,3]]", cov.Indeterminate.PublicationGaps)
	}
}

// ---------------------------------------------------------------------------
// T93 — a present-but-unverifiable predecessor. Phase 39 splits this too: the
// on-disk manifest is unusable, but the ledger still carries its commitment, so
// the chain remains verifiable (and the corrupt file is still reported by the
// P35 status / P37 signature dimensions).
// ---------------------------------------------------------------------------
func TestChainUnverifiablePredecessor(t *testing.T) {
	corruptFirst := func(t *testing.T, f *chainFixture) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json"), []byte("{broken"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no_ledger_breaks", func(t *testing.T) {
		f := newChainFixture(t)
		f.tick(t, at(0), 1, 10)
		f.tick(t, at(1), 11, 20)
		corruptFirst(t, f)
		f.dropLedger(t)

		_, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_broken" {
			t.Fatalf("without a ledger an unverifiable predecessor must break the chain: %+v", verdict)
		}
	})

	t.Run("with_ledger_stays_verifiable", func(t *testing.T) {
		f := newChainFixture(t)
		f.tick(t, at(0), 1, 10)
		f.tick(t, at(1), 11, 20)
		corruptFirst(t, f)

		res, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_ok" {
			t.Fatalf("the ledger still holds the predecessor commitment: %+v", verdict)
		}
		if verdict.VerifiableFromPublicationID != 1 {
			t.Fatalf("verifiable_from = %d, want 1", verdict.VerifiableFromPublicationID)
		}
		// The corrupt file is still honestly reported on its own dimension: the
		// P35 status degrades to unknown, and NO signature verdict is fabricated
		// for a manifest that cannot be parsed (R191).
		for _, r := range res {
			if r.Snapshot != safeTS(at(0)) {
				continue
			}
			if r.Status != "unknown" {
				t.Fatalf("corrupt manifest status = %q, want unknown", r.Status)
			}
			if r.Signature != nil {
				t.Fatalf("no signature verdict may be fabricated for an unparseable manifest: %+v", r.Signature)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// T94 — no double-headed chain across consecutive publications
// ---------------------------------------------------------------------------
func TestChainHasNoForks(t *testing.T) {
	f := newChainFixture(t)
	for i := 0; i < 4; i++ {
		f.tick(t, at(i), int64(i*10+1), int64(i*10+10))
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("consecutive publications must form a single chain: %+v", verdict)
	}
	// Every commit target must be distinct (no two manifests sharing a prev).
	seen := map[int64]bool{}
	for i := 0; i < 4; i++ {
		m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(i))+".manifest.json")
		prev := int64(m["chain"].(map[string]any)["prev_publication_id"].(float64))
		if prev == 0 {
			continue
		}
		if seen[prev] {
			t.Fatalf("forked chain: predecessor %d is committed to by two manifests", prev)
		}
		seen[prev] = true
	}
}

// ---------------------------------------------------------------------------
// T88 — chain verification is strictly read-only
// ---------------------------------------------------------------------------
func TestChainVerifyIsReadOnly(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	before := dirFingerprint(t, f.snapDir)
	mustVerifyDetailed(t, f.sched)
	if after := dirFingerprint(t, f.snapDir); after != before {
		t.Fatal("chain verification mutated the snapshot directory")
	}
}

// ---------------------------------------------------------------------------
// T98 — chain evidence that EXISTS but cannot be trusted is never chain_absent
// (R196). "No chain evidence" and "chain evidence we cannot verify" are
// different states and must not collapse into one.
// ---------------------------------------------------------------------------
func TestChainUnverifiableIsNotAbsent(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, snapDir, manifestName string)
		reTrust bool
	}{
		{
			name: "signature_invalid",
			corrupt: func(t *testing.T, dir, name string) {
				editManifest(t, dir, name, func(m map[string]any) { m["max_seq"] = float64(4242) })
			},
		},
		{
			name: "signature_malformed",
			corrupt: func(t *testing.T, dir, name string) {
				editManifest(t, dir, name, func(m map[string]any) { delete(m["signature"].(map[string]any), "sig") })
			},
		},
		{
			name:    "key_unknown",
			corrupt: func(t *testing.T, dir, name string) {},
			reTrust: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChainFixture(t)
			f.tick(t, at(0), 1, 10)
			name := "alert-transitions-" + safeTS(at(0)) + ".manifest.json"
			tc.corrupt(t, f.snapDir, name)

			sched := f.sched
			if tc.reTrust {
				// Same directory, but the verifier only trusts an unrelated key.
				keyDir := t.TempDir()
				_, otherPub, _, _ := genKeyPair(t, keyDir, "other")
				s2, err := NewHistoryExportScheduler(HistoryExportConfig{
					Store: f.store, Dir: f.snapDir, Interval: time.Hour, Formats: []string{"json"},
					TrustKeyPaths: []string{otherPub},
				})
				if err != nil {
					t.Fatal(err)
				}
				sched = s2
			}

			res, verdict := mustVerifyDetailed(t, sched)
			if verdict.Verdict == "chain_absent" {
				t.Fatalf("chain-bearing manifest exists but cannot be verified — must NOT be chain_absent: %+v", verdict)
			}
			if verdict.Verdict != "chain_broken" {
				t.Fatalf("chain = %+v, want chain_broken", verdict)
			}
			if verdict.Detail == "" {
				t.Fatal("detail must explain why the chain cannot be established")
			}
			if got := chainOf(t, res, at(0)); got != chainPosBroken {
				t.Fatalf("per-snapshot chain = %q, want %q", got, chainPosBroken)
			}
		})
	}
}

// T98b — a v4 manifest with a valid signature but an unverifiable PARTNER still
// breaks instead of being silently reduced to the verifiable subset.
func TestChainPartialUnverifiableBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Invalidate the SECOND node: the surviving first node alone would look
	// like a clean single-node chain, which would hide the tampering.
	editManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(1))+".manifest.json", func(m map[string]any) {
		m["max_seq"] = float64(7777)
	})
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("chain = %+v, want chain_broken (a dropped chain-bearing node is not a smaller clean chain)", verdict)
	}
}

// ---------------------------------------------------------------------------
// T99 — a non-genesis hop without a predecessor digest is NOT verified (R197).
// BOTH commitments (id AND canonical digest) are mandatory for every hop,
// otherwise chain_ok would not mean what it claims.
// ---------------------------------------------------------------------------
func TestChainNonGenesisWithoutDigestBreaks(t *testing.T) {
	signerOf := func(t *testing.T, f *chainFixture) *exportSigner {
		t.Helper()
		signer, err := newExportSigner(f.privPath, "")
		if err != nil {
			t.Fatal(err)
		}
		return signer
	}

	// Control: with the digest present and exact, the same shape verifies —
	// proving the check was tightened rather than removed.
	t.Run("digest_present_verifies", func(t *testing.T) {
		f := newChainFixture(t)
		signer := signerOf(t, f)
		first := v4Manifest(1, "20260910T010000Z", &manifestChain{PrevPublicationID: 0})
		writeSignedManifest(t, f.snapDir, signer, first)
		dg, err := manifestDigest(first)
		if err != nil {
			t.Fatal(err)
		}
		writeSignedManifest(t, f.snapDir, signer, v4Manifest(2, "20260910T020000Z", &manifestChain{PrevPublicationID: 1, PrevManifestDigest: dg}))

		res, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_ok" {
			t.Fatalf("chain = %+v, want chain_ok", verdict)
		}
		if got := chainOfID(t, res, "20260910T020000Z"); got != chainPosPredecessorVerified {
			t.Fatalf("position = %q, want %q", got, chainPosPredecessorVerified)
		}
	})

	t.Run("missing_digest", func(t *testing.T) {
		f := newChainFixture(t)
		signer := signerOf(t, f)
		writeSignedManifest(t, f.snapDir, signer, v4Manifest(1, "20260910T010000Z", &manifestChain{PrevPublicationID: 0}))
		// Validly signed, id commitment present, digest commitment ABSENT.
		writeSignedManifest(t, f.snapDir, signer, v4Manifest(2, "20260910T020000Z", &manifestChain{PrevPublicationID: 1}))

		res, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_broken" {
			t.Fatalf("a hop without a predecessor digest must break the chain: %+v", verdict)
		}
		if len(verdict.BrokenAt) != 1 || verdict.BrokenAt[0] != 2 {
			t.Fatalf("broken_at = %v, want [2]", verdict.BrokenAt)
		}
		if got := chainOfID(t, res, "20260910T020000Z"); got != chainPosBroken {
			t.Fatalf("position = %q, want %q", got, chainPosBroken)
		}
	})

	t.Run("wrong_digest_breaks", func(t *testing.T) {
		f := newChainFixture(t)
		signer := signerOf(t, f)
		writeSignedManifest(t, f.snapDir, signer, v4Manifest(1, "20260910T010000Z", &manifestChain{PrevPublicationID: 0}))
		writeSignedManifest(t, f.snapDir, signer, v4Manifest(2, "20260910T020000Z", &manifestChain{
			PrevPublicationID: 1, PrevManifestDigest: strings.Repeat("ab", 32),
		}))

		_, verdict := mustVerifyDetailed(t, f.sched)
		if verdict.Verdict != "chain_broken" {
			t.Fatalf("a mismatching predecessor digest must break the chain: %+v", verdict)
		}
	})
}

// ---------------------------------------------------------------------------
// T100 — an untrusted latest predecessor BLOCKS the next publication (R198).
// Extending a chain from a node whose P37 provenance no longer holds would
// launder it into a trusted link; skipping back to an older node would disguise
// the break. Both are refused.
// ---------------------------------------------------------------------------
func TestChainTamperedPredecessorBlocksPublication(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	// Tamper the LATEST chain-bearing manifest (its signature no longer holds).
	editManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(1))+".manifest.json", func(m map[string]any) {
		m["max_seq"] = float64(4242)
	})

	f.tick(t, at(2), 21, 30) // attempt the next publication

	next := filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatalf("publication must be REFUSED when the latest predecessor is untrusted (stat err=%v)", err)
	}
	st := f.sched.Status()
	if !strings.Contains(st.ManifestError, "manifest: chain:") {
		t.Fatalf("manifest_error = %q, want a chain refusal", st.ManifestError)
	}
	if st.ChainError == "" {
		t.Fatal("chain_error must surface the refused extension")
	}
}

// T100 control — with a trusted latest predecessor the chain simply extends.
func TestChainTrustedPredecessorAllowsPublication(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
	if v, _ := m["chain"].(map[string]any)["prev_publication_id"].(float64); int64(v) != 2 {
		t.Fatalf("pub 3 must extend pub 2, got %v", m["chain"])
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
}

// T100b — genesis carries no digest (SHOULD); a genesis that does is malformed
// rather than silently accepted.
func TestChainGenesisDigestIsEmpty(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	chain := m["chain"].(map[string]any)
	if _, present := chain["prev_manifest_digest"]; present {
		t.Fatal("genesis must not carry a predecessor digest")
	}

	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	bad := v4Manifest(1, "20260910T010000Z", &manifestChain{PrevPublicationID: 0, PrevManifestDigest: strings.Repeat("cd", 32)})
	writeSignedManifest(t, dir2, signer, bad)
	s2, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: f.store, Dir: dir2, Interval: time.Hour, Formats: []string{"json"},
		TrustKeyPaths: []string{f.pubPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, verdict := mustVerifyDetailed(t, s2)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("a genesis digest without a predecessor id must break: %+v", verdict)
	}
	if got := chainOfID(t, res, "20260910T010000Z"); got != chainPosBroken {
		t.Fatalf("position = %q, want %q", got, chainPosBroken)
	}
}

// ---------------------------------------------------------------------------
// T101 — an unreadable / unparseable newest candidate is NOT skipped (R199).
// "I cannot read it" is not "it does not exist": skipping it would roll the
// chain back to an older node and hide the break.
// ---------------------------------------------------------------------------
func TestChainUnreadablePredecessorBlocksPublication(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, path string)
	}{
		{
			name: "unparseable_json",
			corrupt: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{truncated"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "empty_file",
			corrupt: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChainFixture(t)
			f.tick(t, at(0), 1, 10)
			f.tick(t, at(1), 11, 20)
			tc.corrupt(t, filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(1))+".manifest.json"))

			f.tick(t, at(2), 21, 30) // attempt the next publication

			next := filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
			if _, err := os.Stat(next); !os.IsNotExist(err) {
				t.Fatalf("publication must be REFUSED: an unreadable newest candidate cannot be skipped (stat err=%v)", err)
			}
			st := f.sched.Status()
			if !strings.Contains(st.ManifestError, "manifest: chain:") {
				t.Fatalf("manifest_error = %q, want a chain refusal", st.ManifestError)
			}
			if st.ChainError == "" {
				t.Fatal("chain_error must surface the refused extension")
			}
		})
	}
}

// T101 control — a readable, trusted newest candidate still extends normally,
// proving the publisher was not simply disabled.
func TestChainReadablePredecessorStillPublishes(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
	if v, _ := m["chain"].(map[string]any)["prev_publication_id"].(float64); int64(v) != 2 {
		t.Fatalf("pub 3 must extend pub 2, got %v", m["chain"])
	}
	if _, verdict := mustVerifyDetailed(t, f.sched); verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
}

// ---------------------------------------------------------------------------
// T102 — the predecessor is chosen on the PUBLICATION-ID axis, never the
// snapshot-identity (directory) order (R200). When an exported snapshot's
// timestamp moves backwards the two orders disagree, and a predecessor picked
// by directory order would be written (and signed) into the chain wrongly.
// ---------------------------------------------------------------------------
func TestChainPredecessorFollowsPublicationOrder(t *testing.T) {
	f := newChainFixture(t)
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	// Publication order 101 → 102 → 103, but identity order T1 < T2 < T3 maps to
	// 101 → 103 → 102: the MAXIMUM identity is NOT the maximum publication id.
	a := v4Manifest(101, "20260910T010000Z", &manifestChain{PrevPublicationID: 0})
	writeSignedManifest(t, f.snapDir, signer, a)
	dgA, err := manifestDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	c := v4Manifest(102, "20260910T030000Z", &manifestChain{PrevPublicationID: 101, PrevManifestDigest: dgA})
	writeSignedManifest(t, f.snapDir, signer, c)
	dgC, err := manifestDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	b := v4Manifest(103, "20260910T020000Z", &manifestChain{PrevPublicationID: 102, PrevManifestDigest: dgC})
	writeSignedManifest(t, f.snapDir, signer, b)

	f.burn(t, 103)          // watermark past the existing ids
	f.tick(t, at(5), 1, 10) // publish the next snapshot (pub 104)

	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(5))+".manifest.json")
	got := int64(m["chain"].(map[string]any)["prev_publication_id"].(float64))
	if got != 103 {
		t.Fatalf("predecessor = %d, want 103 (maximum publication_id, NOT the newest identity)", got)
	}
	if _, verdict := mustVerifyDetailed(t, f.sched); verdict.Verdict != "chain_ok" {
		t.Fatalf("chain = %+v, want chain_ok", verdict)
	}
}

// ---------------------------------------------------------------------------
// T103 — an unorderable candidate is never waved through on its identity
// (R201). A damaged manifest has NO knowable publication_id, so its directory
// identity cannot prove it is "old news" — that would be the cross-axis
// inference R200 forbade. The publication is refused.
// ---------------------------------------------------------------------------
func TestChainUnorderableCandidateAlwaysRefuses(t *testing.T) {
	f := newChainFixture(t)
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	// A readable chain-bearing manifest with a LATE identity.
	writeSignedManifest(t, f.snapDir, signer, v4Manifest(101, "20260910T090000Z", &manifestChain{PrevPublicationID: 0}))
	// A damaged manifest with an EARLIER identity: it *looks* older, but its
	// publication position is unknowable, so it must still refuse.
	broken := filepath.Join(f.snapDir, "alert-transitions-20260910T010000Z.manifest.json")
	if err := os.WriteFile(broken, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.burn(t, 101)
	f.tick(t, at(5), 1, 10)

	next := filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(5))+".manifest.json")
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatalf("an unorderable candidate must refuse the publication (stat err=%v)", err)
	}
	if st := f.sched.Status(); !strings.Contains(st.ManifestError, "manifest: chain:") || st.ChainError == "" {
		t.Fatalf("chain refusal must be surfaced: %q / %q", st.ManifestError, st.ChainError)
	}

	// Readable control: remove the damaged file and the same publication works.
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	f.tick(t, at(6), 1, 10)
	m := readManifestFile(t, f.snapDir, "alert-transitions-"+safeTS(at(6))+".manifest.json")
	if got := int64(m["chain"].(map[string]any)["prev_publication_id"].(float64)); got != 101 {
		t.Fatalf("predecessor = %d, want 101", got)
	}
}

// T103b — the publication axis still wins when identities disagree AND a
// damaged file is present: T102 behaviour is preserved, not regressed.
func TestChainPublicationAxisWithUnorderablePresent(t *testing.T) {
	f := newChainFixture(t)
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	writeSignedManifest(t, f.snapDir, signer, v4Manifest(101, "20260910T010000Z", &manifestChain{PrevPublicationID: 0}))
	writeSignedManifest(t, f.snapDir, signer, v4Manifest(103, "20260910T020000Z", &manifestChain{PrevPublicationID: 0}))
	if err := os.WriteFile(filepath.Join(f.snapDir, "alert-transitions-20260910T030000Z.manifest.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.burn(t, 103)
	f.tick(t, at(5), 1, 10)
	if _, err := os.Stat(filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(5))+".manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("damaged file must refuse regardless of the publication axis (stat err=%v)", err)
	}
}

func TestChainDefaultIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp, MinSeq: 1, MaxSeq: 3}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	raw, err := os.ReadFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v, _ := m["schema_version"].(float64); int(v) != manifestSchemaVersionV2 {
		t.Fatalf("unsigned manifest must stay v2, got %v", m["schema_version"])
	}
	if _, ok := m["chain"]; ok {
		t.Fatal("unsigned manifest must carry no chain block")
	}
	if strings.Contains(string(raw), "chain") {
		t.Fatalf("v2 document must not mention chain at all: %s", raw)
	}
	_, verdict := mustVerifyDetailed(t, s)
	if verdict.Verdict != "chain_absent" {
		t.Fatalf("chain = %+v, want chain_absent", verdict)
	}
}
