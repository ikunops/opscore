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
// T97 — deleting the P38 anchor is NOT a boundary, it is a break
// ---------------------------------------------------------------------------
func TestChainAnchorDeletionBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10) // anchor
	f.tick(t, at(1), 11, 20)

	f.drop(t, at(0)) // delete the anchor

	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("deleting the P38 anchor must break the chain (boundary is bound to the real chain start): %+v", verdict)
	}
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
// T93 — a present-but-unverifiable predecessor cannot be confirmed
// ---------------------------------------------------------------------------
func TestChainUnverifiablePredecessorBreaks(t *testing.T) {
	f := newChainFixture(t)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	// Corrupt the FIRST manifest's JSON so it cannot be parsed at all: the
	// successor remains intact and still commits to it.
	if err := os.WriteFile(filepath.Join(f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, verdict := mustVerifyDetailed(t, f.sched)
	if verdict.Verdict != "chain_broken" {
		t.Fatalf("an unverifiable predecessor must break the chain: %+v", verdict)
	}
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
