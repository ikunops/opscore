package server

// Phase 51 — Lifecycle Subject Domain tests (T363~T383; ADR-077 §5, ADR-078 §7).
//
// The discriminating cases all follow one shape: ONE ledger that a pre-Phase-51
// reader could only answer by consulting the deployment's LIVE trust file is now
// answered from the evidence itself, and the two consumers of that ledger (P41's
// status roll-up, P50's authority face) must never contradict each other on the
// same input. Three properties are pinned inside the cases rather than asserted
// in prose: the neighbouring faces stay byte-identical while this one says
// something new (I5), a ledger whose rows predate the carrier keeps EXACTLY its
// old reading, per consumer (I13 — never a unified rule), and "undecidable"
// (undeclared / migrated / conflict) never becomes "declared" (I6/I7/I12).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture: the trust anchors are MUTABLE across runs (that is the whole point of
// this Phase), so the fixture is parameterised where the P50 one is fixed.
// ---------------------------------------------------------------------------

type p51Env struct {
	t                 *testing.T
	dir               string
	keyDir            string
	store             *fakeExportStore
	signPriv, signPub string
	kakPriv, kakPub   string
	now               time.Time
}

func newP51Env(t *testing.T) *p51Env {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{dir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signPriv, signPub, _, _ := genKeyPair(t, keyDir, "signer")
	kakPriv, kakPub, _, _ := genKeyPair(t, keyDir, "kak")
	return &p51Env{
		t: t, dir: dir, keyDir: keyDir,
		store:    &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: p50T1, MinSeq: 1, MaxSeq: 3}},
		signPriv: signPriv, signPub: signPub,
		kakPriv: kakPriv, kakPub: kakPub,
		now: p50T1,
	}
}

// key generates an extra keypair; returns its private path, public path and the
// P37-derived key id (never configured — ADR-055 §1).
func (e *p51Env) key(base string) (string, string, string) {
	e.t.Helper()
	priv, pub, _, pubKey := genKeyPair(e.t, e.keyDir, base)
	return priv, pub, keyIDForPublicKey(pubKey)
}

func (e *p51Env) config(signTrust []string, vpriv string, vtrust []string, anchoring bool) HistoryExportConfig {
	cfg := HistoryExportConfig{
		Store:                  e.store,
		Dir:                    e.dir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		SignKeyPath:            e.signPriv,
		TrustKeyPaths:          signTrust,
		KeyAuthorityPath:       e.kakPriv,
		KeyAuthorityTrustPaths: []string{e.kakPub},
		VerifierKeyPath:        vpriv,
		VerifierTrustPaths:     vtrust,
		VerifyAttest:           true,
		Clock:                  func() time.Time { return e.now },
	}
	if anchoring {
		wd := filepath.Join(filepath.Dir(e.dir), "witness")
		if err := os.MkdirAll(wd, 0o755); err != nil {
			e.t.Fatal(err)
		}
		cfg.AnchorEndpoint = "file://" + filepath.ToSlash(wd)
		cfg.AnchorMaxAttempts = 8
	}
	return cfg
}

// build assembles a scheduler over THIS directory with an explicit pair of
// trust anchors. Same dir + same ledger across calls is exactly the
// "configuration changed under the directory" input this Phase is about.
func (e *p51Env) build(signTrust []string, vpriv string, vtrust []string) *HistoryExportScheduler {
	e.t.Helper()
	s, err := NewHistoryExportScheduler(e.config(signTrust, vpriv, vtrust, false))
	if err != nil {
		e.t.Fatalf("scheduler: %v", err)
	}
	return s
}

// buildAnchored is build() with the anchor stream turned on (T381).
func (e *p51Env) buildAnchored(signTrust []string, vpriv string, vtrust []string) *HistoryExportScheduler {
	e.t.Helper()
	s, err := NewHistoryExportScheduler(e.config(signTrust, vpriv, vtrust, true))
	if err != nil {
		e.t.Fatalf("scheduler: %v", err)
	}
	return s
}

// buildNoLedger has NO key authority at all: the lifecycle ledger is disabled.
func (e *p51Env) buildNoLedger(signTrust []string) *HistoryExportScheduler {
	e.t.Helper()
	cfg := e.config(signTrust, "", nil, false)
	cfg.KeyAuthorityPath = ""
	cfg.KeyAuthorityTrustPaths = nil
	s, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		e.t.Fatalf("scheduler: %v", err)
	}
	return s
}

func (e *p51Env) activate(s *HistoryExportScheduler, keyID string, notBefore time.Time) keyLifecycleEntry {
	e.t.Helper()
	ent, err := s.AppendKeyLifecycleEvent(keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: keyID, NotBefore: klTS(notBefore)})
	if err != nil {
		e.t.Fatalf("activate %s: %v", keyID, err)
	}
	return ent
}

func (e *p51Env) terminal(s *HistoryExportScheduler, keyID, event string, notAfter time.Time) keyLifecycleEntry {
	e.t.Helper()
	ent, err := s.AppendKeyLifecycleEvent(keyLifecycleRequest{EventType: event, KeyID: keyID, NotAfter: klTS(notAfter)})
	if err != nil {
		e.t.Fatalf("%s %s: %v", event, keyID, err)
	}
	return ent
}

// rawRowAt appends ONE hand-built lifecycle row with an EXPLICIT `role` (the
// empty string and values outside the enum are both reachable this way) and an
// explicit seq/prev pointer. The row is fully self-consistent and KAK-signed, so
// the ONLY thing that can give it away is what the Phase reads.
func (e *p51Env) rawRowAt(s *HistoryExportScheduler, seq int64, prevDigest, keyID, role, eventType, notBefore, notAfter string) keyLifecycleEntry {
	e.t.Helper()
	kak, err := newExportSigner(e.kakPriv, "")
	if err != nil {
		e.t.Fatal(err)
	}
	c := s.keyLifecycleConfig()
	ent := keyLifecycleEntry{
		V:               1,
		EventSeq:        seq,
		EventType:       eventType,
		KeyID:           keyID,
		NotBefore:       notBefore,
		NotAfter:        notAfter,
		RecordedAt:      e.now.UTC().Format(time.RFC3339Nano),
		AuthorityKeyID:  kak.keyID,
		StreamID:        c.streamID,
		PrevEventDigest: prevDigest,
		Role:            role,
	}
	dg, derr := lifecycleEventDigest(&ent)
	if derr != nil {
		e.t.Fatal(derr)
	}
	ent.EventDigest = dg
	if serr := kak.signKeyLifecycleEntry(&ent, e.now); serr != nil {
		e.t.Fatal(serr)
	}
	raw, merr := serializeKeyLifecycleEntryBytes(&ent)
	if merr != nil {
		e.t.Fatal(merr)
	}
	if aerr := appendLogLine(keyLifecycleLogPath(c.dir), raw); aerr != nil {
		e.t.Fatal(aerr)
	}
	return ent
}

// rawRow appends a hand-built row as the ledger's NEXT event.
func (e *p51Env) rawRow(s *HistoryExportScheduler, keyID, role, eventType, notBefore, notAfter string) keyLifecycleEntry {
	e.t.Helper()
	lines, ok, err := readLogLines(keyLifecycleLogPath(e.dir), keyLifecycleGroupOf)
	if err != nil || !ok {
		e.t.Fatalf("read ledger: err=%v ok=%v", err, ok)
	}
	var maxSeq int64
	prev := ""
	for i := range lines {
		var p keyLifecycleEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			e.t.Fatal(jerr)
		}
		if p.EventSeq > maxSeq {
			maxSeq, prev = p.EventSeq, p.EventDigest
		}
	}
	return e.rawRowAt(s, maxSeq+1, prev, keyID, role, eventType, notBefore, notAfter)
}

// p51Attest appends ONE hand-built verification report signed by `signer`, which
// CLAIMS `claimedKeyID`. It mirrors the P50 fixture's helper, parameterised by
// scheduler so a case can put several different signers into one log.
func p51Attest(t *testing.T, s *HistoryExportScheduler, at time.Time, claimedKeyID string, signer *exportSigner, signedAt string) verificationLogEntry {
	t.Helper()
	c := s.verificationConfig()
	lines, ok, err := readLogLines(verificationLogPath(c.dir), verificationGroupOf)
	if err != nil || !ok {
		t.Fatalf("read verification log: err=%v ok=%v", err, ok)
	}
	prev := ""
	maxSeq := int64(0)
	for i := range lines {
		var p verificationLogEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			t.Fatal(jerr)
		}
		prev = p.Digest
		if p.ReportSeq > maxSeq {
			maxSeq = p.ReportSeq
		}
	}
	items := []VerificationItem{{
		PublicationID: 1,
		Status:        verifyStatusOK,
		Chain:         chainPosPredecessorVerified,
		ChainSource:   "disk+ledger",
		Overall:       verificationAttested,
	}}
	ev := DimensionAvailability{Status: true, Chain: true, ChainSource: true}
	e := verificationLogEntry{
		V:          1,
		ReportSeq:  maxSeq + 1,
		Subject:    VerificationSubject{Publications: []int64{1}, MinID: 1, MaxID: 1, Limit: verificationSubjectLimit},
		Evaluable:  ev,
		Items:      items,
		Overall:    verificationAttested,
		Reasons:    buildVerificationReasons(items, ev, ""),
		VerifiedAt: at.UTC().Format(time.RFC3339Nano),
		StreamID:   c.streamID,
		KeyID:      claimedKeyID,
		PrevDigest: prev,
	}
	dg, derr := verificationEntryDigest(&e)
	if derr != nil {
		t.Fatal(derr)
	}
	e.Digest = dg
	if serr := signer.signVerificationEntry(&e, at); serr != nil {
		t.Fatal(serr)
	}
	if signedAt != "" {
		e.Signature.SignedAt = signedAt
	}
	raw, merr := serializeVerificationEntryBytes(&e)
	if merr != nil {
		t.Fatal(merr)
	}
	if aerr := appendLogLine(verificationLogPath(c.dir), raw); aerr != nil {
		t.Fatal(aerr)
	}
	return e
}

func p51SignerFor(t *testing.T, privPath string) *exportSigner {
	t.Helper()
	s, err := newExportSigner(privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// p51SignerID is the P37-derived id of a private key file (what a fixture needs
// to name a subject it did not otherwise keep).
func p51SignerID(t *testing.T, privPath string) string { return p51SignerFor(t, privPath).keyID }

func p51Pub(t *testing.T, privPath string) ed25519.PublicKey {
	t.Helper()
	return p51SignerFor(t, privPath).priv.Public().(ed25519.PublicKey)
}

func p51DomainFace(s *HistoryExportScheduler) lifecycleDomainStatusSummary {
	return keyLifecycleDomainSummary(s.keyLifecycleConfig())
}

func p51Subject(t *testing.T, sum lifecycleDomainStatusSummary, keyID string) lifecycleDomainSubjectStatus {
	t.Helper()
	row, ok := sum.Subjects[keyID]
	if !ok {
		t.Fatalf("no domain subject %s (subjects: %v)", keyID, sum.Subjects)
	}
	return row
}

func p51Authorizations(sum keyLifecycleStatusSummary) []string {
	out := make([]string, 0, len(sum.Authorities))
	for _, a := range sum.Authorities {
		out = append(out, a.KeyID)
	}
	return out
}

// p51ProtectionDigest pins the whole `internal/protection` tree (P51 promises it
// zero diff): sorted relative path + sha256 of each file, folded into one digest.
const p51ProtectionDigest = "b0895fc48dbff771f93d628921c6b3ab94d3aac60fb31a56081619e887e23589"

// p51FrozenFiles is the zero-diff surface P51 promises (ADR-077 §4 A3 / I1): the
// five frozen files, the further faces P41~P50 own, and the module manifests.
func p51FrozenFiles() map[string]string {
	return map[string]string{
		"internal/controlplane/server/appendonly_log.go":              "f4ae703323c3c9c49f5209f2b70f287dacc8ad61cb8ff84c1c1c9aa93fcb81ee",
		"internal/controlplane/server/snapshot_anchor.go":             "eca582354359fde49c3e2b1f6e664551dbcd31ff300a310938ea2429acdebc63",
		"internal/controlplane/server/snapshot_witness_reconcile.go":  "4313822e3bdfb3b2bace3eed1643469be23611097ea728e17412c4b39200f9b2",
		"internal/controlplane/server/snapshot_signature.go":          "17d9ced5dff8b31be576a9473e5175083ce03e60f21b3cfbd12f78960d406d23",
		"internal/controlplane/server/snapshot_chain.go":              "736c29b9b6ce114ca0efeb4068586b42c32af832dad20b699aadea8c7c9a47b2",
		"internal/controlplane/server/snapshot_ledger.go":             "e9efe6fd1a7013639f0539cfb2a2940957edb9201173df005b39f2a80f50d325",
		"internal/controlplane/server/history_export_coverage.go":     "478f0ad9bd9f6189f04f198b90f2493c138ad1678a0c2aaed6364d89fe133999",
		"internal/controlplane/server/history_export_manifest.go":     "86d56214e4e69a8a33b3f4293fa72e2a3c69148e300ee1f1f8f980b335892588",
		"internal/controlplane/server/snapshot_verification.go":       "2d35d9d3d267c1cfd025653b83a68e34cb4f9aeb8d4e3daa65931d9714202b27",
		"internal/controlplane/server/snapshot_anchor_realization.go": "6a59a5f1d3b7b32681eca8573fa80d765a5e94d39d3479388a2870a20d8a1e5a",
		"go.mod": "6dfc9eea3dcba0f32b4ef8229b1a9a912dddf6db1891ed3b6623538f987e6b32",
		"go.sum": "48a94c452c1c0b794722b90652f70a8bd6ea4a3a82f86b50b31bb0683bd9cd27",
	}
}

// p51DirDigest folds every file under a directory (sorted relative path +
// content hash) into one digest, so a whole tree can be pinned.
func p51DirDigest(t *testing.T, rel string) string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	var files []string
	err := filepath.Walk(filepath.Join(root, rel), func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if info.IsDir() {
			return nil
		}
		sub, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(sub))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		data, rerr := os.ReadFile(filepath.Join(root, f))
		if rerr != nil {
			t.Fatal(rerr)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s %s\n", f, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// T363 — frozen face by content + P41 ledger BYTE equivalence (I1).
// ---------------------------------------------------------------------------

func TestP51T363FrozenFaceAndLedgerBytes(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for rel, want := range p51FrozenFiles() {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("frozen file %s unreadable: %v", rel, err)
		}
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("T363/I1 VIOLATION: frozen file %s changed:\n got %s\nwant %s", rel, got, want)
		}
	}
	// `internal/protection` is zero-diff too (whole tree, one digest).
	if got, want := p51DirDigest(t, "internal/protection"), p51ProtectionDigest; got != want {
		t.Fatalf("T363/I1: internal/protection must be zero-diff:\n got %s\nwant %s", got, want)
	}

	// The trailing `omitempty` carrier is an IDENTITY on a pre-Phase-51 row: the
	// canonical payload — and therefore event_digest, and therefore the KAK
	// signature over it — is the byte string the old eleven fields produced, in
	// the old order, with no `role` at all (ADR-077 §2 probe 4).
	legacy := keyLifecycleEntry{
		V: 1, EventSeq: 2, EventType: lifecycleEventActivated, KeyID: "k",
		PubkeyFingerprint: "f",
		NotBefore:         "2026-11-01T00:00:00Z",
		RecordedAt:        "2026-11-01T00:00:00Z",
		AuthorityKeyID:    "a", StreamID: "s", PrevEventDigest: "p",
	}
	payload, err := canonicalLifecyclePayload(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	wantLegacy := `{"v":1,"event_seq":2,"event_type":"activated","key_id":"k","pubkey_fingerprint":"f","not_before":"2026-11-01T00:00:00Z","recorded_at":"2026-11-01T00:00:00Z","authority_key_id":"a","stream_id":"s","prev_event_digest":"p"}`
	if string(payload) != wantLegacy {
		t.Fatalf("T363: a row written before Phase 51 must hash to the SAME bytes:\n got %s\nwant %s", payload, wantLegacy)
	}
	// …and a NEW row carries the domain, LAST, exactly as the probe measured.
	modern := legacy
	modern.Role = lifecycleDomainVerifier
	payload2, err := canonicalLifecyclePayload(&modern)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(wantLegacy, "}") + `,"role":"verifier"}`; string(payload2) != want {
		t.Fatalf("T363: a declared row must append exactly one trailing field:\n got %s\nwant %s", payload2, want)
	}

	// The identity must hold end to end on disk: a hand-built PRE-Phase-51 row
	// (no `role`) is written, KAK-signed and read back as verifiable.
	e := newP51Env(t)
	s := e.build([]string{e.signPub}, "", nil)
	row := e.rawRow(s, "legacy-key-id", "", lifecycleEventActivated, klTS(p50T0), "")
	if row.Role != "" {
		t.Fatalf("fixture: a legacy row carries no role, got %q", row.Role)
	}
	st, lerr := loadKeyLifecycleState(s.keyLifecycleConfig())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if !st.verifiable {
		t.Fatalf("T363: a pre-Phase-51 row must still verify against the KAK: %v", st.errs)
	}
	raw, rerr := os.ReadFile(keyLifecycleLogPath(e.dir))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(raw), `"role"`) {
		t.Fatalf("T363: an old row must not gain a role byte: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// T364 — the enable gate IS the lifecycle gate, and the baseline is exhaustive
// (I3).
// ---------------------------------------------------------------------------

func TestP51T364GroupSharesTheLifecycleGateExactly(t *testing.T) {
	// Ledger DISABLED: neither group may appear, and the document must not even
	// mention them.
	e := newP51Env(t)
	s := e.buildNoLedger([]string{e.signPub})
	s.Tick(context.Background())
	st := s.Status()
	if st.KeyLifecycle != nil || st.KeyLifecycleDomains != nil {
		t.Fatalf("with the KAK absent BOTH groups must be absent: kl=%v domains=%v", st.KeyLifecycle, st.KeyLifecycleDomains)
	}
	doc, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "key_lifecycle") {
		t.Fatalf("a disabled ledger must not emit either lifecycle group: %s", doc)
	}
	keys := p50Keys(t, st)
	if got, want := strings.Join(keys, ","), strings.Join(p51NoLedgerStatusKeys(), ","); got != want {
		t.Fatalf("the disabled ledger's document changed shape:\n got %v\nwant %v", keys, p51NoLedgerStatusKeys())
	}

	// Ledger ENABLED: the two groups are present together, and the ledger's own
	// domain group is the ONLY new top-level key (the exhaustive declaration).
	g := newP51Env(t)
	s2 := g.build([]string{g.signPub}, "", nil)
	s2.Tick(context.Background())
	st2 := s2.Status()
	if st2.KeyLifecycle == nil || st2.KeyLifecycleDomains == nil {
		t.Fatalf("an enabled ledger must carry BOTH groups: kl=%v domains=%v", st2.KeyLifecycle, st2.KeyLifecycleDomains)
	}
	if got, want := strings.Join(p50Keys(t, st2), ","), strings.Join(p50BaselineStatusKeys(), ","); got != want {
		t.Fatalf("the enabled baseline must be exhaustive:\n got %v\nwant %v", p50Keys(t, st2), p50BaselineStatusKeys())
	}
	// An empty ledger is not a declaration (I6) — and the group says so loudly.
	if st2.KeyLifecycleDomains.State != lifecycleDomainUndeclared || st2.KeyLifecycleDomains.Declared {
		t.Fatalf("an empty ledger must read undeclared: %+v", st2.KeyLifecycleDomains)
	}
}

func p51NoLedgerStatusKeys() []string {
	var out []string
	for _, k := range p50BaselineStatusKeys() {
		if k == "key_lifecycle" || k == "key_lifecycle_domains" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// T365 — the write face records the domain; the caller cannot declare it (A1/A2)
// ---------------------------------------------------------------------------

func TestP51T365WriteFaceRecordsTheDerivedDomain(t *testing.T) {
	// The request type carries NO domain field: the domain is derived from anchor
	// membership, so a caller has no way to assert one (I9 — type-level).
	rt := reflect.TypeOf(keyLifecycleRequest{})
	if rt.NumField() != 4 {
		t.Fatalf("keyLifecycleRequest must keep exactly its four P41 fields, got %d", rt.NumField())
	}
	var tags []string
	for i := 0; i < rt.NumField(); i++ {
		tags = append(tags, string(rt.Field(i).Tag))
	}
	wantTags := `json:"event_type",json:"key_id",json:"not_before,omitempty",json:"not_after,omitempty"`
	if got := strings.Join(tags, ","); got != wantTags {
		t.Fatalf("keyLifecycleRequest changed:\n got %v\nwant %v", got, wantTags)
	}

	// A manifest signing subject gets `signing`; a verifier subject `verifier`.
	f := newP51Env(t)
	vpriv, vpub, vid := f.key("vak")
	s := f.build([]string{f.signPub}, vpriv, []string{vpub})
	signerID := p51SignerID(t, f.signPriv)
	if got := f.activate(s, signerID, p50T0).Role; got != lifecycleDomainSigning {
		t.Fatalf("a signing-anchor subject must be recorded as signing, got %q", got)
	}
	if got := f.activate(s, vid, p50T0).Role; got != lifecycleDomainVerifier {
		t.Fatalf("a verifier-anchor subject must be recorded as verifier, got %q", got)
	}
	raw, err := os.ReadFile(keyLifecycleLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"role":"signing"`) || !strings.Contains(string(raw), `"role":"verifier"`) {
		t.Fatalf("both domains must be durable: %s", raw)
	}

	// The admission decisions are UNCHANGED (I10): neither anchor and both
	// anchors are still refused, with their wording, and a refusal adds no byte.
	before, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if _, aerr := s.AppendKeyLifecycleEvent(keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: "deadbeefdeadbeef", NotBefore: klTS(p50T0)}); aerr == nil {
		t.Fatal("a key in neither anchor must still be refused")
	} else if !strings.Contains(aerr.Error(), "neither") {
		t.Fatalf("the neither-anchor refusal wording must not change: %v", aerr)
	}
	s.verifierTrust.keys[signerID] = p51Pub(t, f.signPriv)
	if _, aerr := s.AppendKeyLifecycleEvent(keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: signerID, NotBefore: klTS(p50T0)}); aerr == nil {
		t.Fatal("a key in both anchors must still be refused")
	} else if !strings.Contains(aerr.Error(), "domain is undecidable") {
		t.Fatalf("the both-anchor refusal wording must not change: %v", aerr)
	}
	after, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after) != string(before) {
		t.Fatal("a refused event must leave the ledger byte-identical")
	}
}

// ---------------------------------------------------------------------------
// T366 — the core new verdict: three subjects, three domains, one ledger (A1/A9)
// ---------------------------------------------------------------------------

func TestP51T366DomainsAreAssertedPerSubject(t *testing.T) {
	e := newP51Env(t)
	_, extraPub, extraID := e.key("extra")
	vpriv, vpub, vid := e.key("vak")
	s := e.build([]string{e.signPub, extraPub}, vpriv, []string{vpub})
	signerID := p51SignerID(t, e.signPriv)

	// ONE pre-Phase-51 row (no `role`) + two declared subjects, same ledger.
	e.rawRow(s, extraID, "", lifecycleEventActivated, klTS(p50T0), "")
	e.activate(s, signerID, p50T0)
	e.activate(s, vid, p50T0)

	sum := p51DomainFace(s)
	if got := p51Subject(t, sum, signerID).Domain; got != lifecycleDomainSigning {
		t.Fatalf("the signing subject must read signing, got %q", got)
	}
	if got := p51Subject(t, sum, vid).Domain; got != lifecycleDomainVerifier {
		t.Fatalf("the verifier subject must read verifier, got %q", got)
	}
	if got := p51Subject(t, sum, extraID).Domain; got != lifecycleDomainUndeclared {
		t.Fatalf("a pre-Phase-51 row must read undeclared, got %q", got)
	}
	if sum.State != lifecycleDomainStateDeclared || !sum.Declared {
		t.Fatalf("one declared subject makes the face declared: %+v", sum)
	}
	if sum.UndeclaredEvents != 1 || sum.DeclaredEvents != 2 || sum.Events != 3 || sum.SubjectCount != 3 {
		t.Fatalf("witness counts: %+v", sum)
	}
}

// ---------------------------------------------------------------------------
// T367 — red example A: a RETIRED verifier never leaks back into P41's roll-up
// ---------------------------------------------------------------------------

func TestP51T367RetiredVerifierStaysOutOfTheSigningRollup(t *testing.T) {
	e := newP51Env(t)
	v1Priv, v1Pub, v1ID := e.key("vak1")
	v2Priv, v2Pub, _ := e.key("vak2")
	signerID := p51SignerID(t, e.signPriv)

	// Run 1: VAK1 is configured AND trusted; its window is written by the
	// production write face, so its row declares `verifier`.
	s1 := e.build([]string{e.signPub}, v1Priv, []string{v1Pub})
	e.activate(s1, signerID, p50T0)
	e.activate(s1, v1ID, p50T0)

	// Run 2: the verifier is ROTATED — a new VAK and a new trust FILE, SAME
	// directory, SAME ledger, SAME signer, SAME KAK.
	s2 := e.build([]string{e.signPub}, v2Priv, []string{v2Pub})
	sum := keyLifecycleSummary(s2.keyLifecycleConfig())
	for _, id := range p51Authorizations(sum) {
		if id == v1ID {
			t.Fatalf("① the RETIRED verifier %s must not appear in authorizations: %+v", v1ID, sum.Authorities)
		}
	}
	if sum.ActiveKeyID != signerID {
		t.Fatalf("② active_key_id must stay with the signing subject, got %q", sum.ActiveKeyID)
	}
	// ③ …while the domain face still states, from the evidence, what that window
	// was for. The faces are simultaneously visible and never mask each other.
	dsum := p51DomainFace(s2)
	if got := p51Subject(t, dsum, v1ID).Domain; got != lifecycleDomainVerifier {
		t.Fatalf("③ the retired subject's domain must still be readable: %q", got)
	}
	if dsum.State != lifecycleDomainStateDeclared {
		t.Fatalf("the face must not be dragged down by a legal rotation: %+v", dsum)
	}
}

// ---------------------------------------------------------------------------
// T368 — red example B: a SEQUENTIAL migration is the domain face's job, and A6
// is silent on it (A8-③)
// ---------------------------------------------------------------------------

func TestP51T368SequentialMigrationIsReportedAndA6IsSilent(t *testing.T) {
	e := newP51Env(t)
	mPriv, mPub, mID := e.key("moving")
	v2Priv, v2Pub, _ := e.key("vak2")

	// Run 1: M is a VERIFIER and its window is opened for real.
	s1 := e.build([]string{e.signPub}, mPriv, []string{mPub})
	e.activate(s1, mID, p50T0)

	// Run 2: the SAME key_id is now a MANIFEST SIGNING subject. It is never in
	// both anchors at once, so the construction guard has nothing to see, and a
	// NEW event declares the new domain.
	s2 := e.build([]string{e.signPub, mPub}, v2Priv, []string{v2Pub})
	if ov := verifierTrustOverlap(s2.trust, s2.verifierTrust); len(ov) != 0 {
		t.Fatalf("A6 must be silent on a sequential migration, got overlap %v", ov)
	}
	e.terminal(s2, mID, lifecycleEventRotatedOut, p50T5)

	sum := p51DomainFace(s2)
	if sum.State != lifecycleDomainStateMigrated {
		t.Fatalf("a two-domain subject must make the face migrated, got %s (%s)", sum.State, sum.Reason)
	}
	if len(sum.MigratedKeys) != 1 || sum.MigratedKeys[0] != mID {
		t.Fatalf("the migrated subject must be named: %v", sum.MigratedKeys)
	}
	sub := p51Subject(t, sum, mID)
	if sub.Domain != lifecycleDomainSigning {
		t.Fatalf("the CURRENT domain is the LAST declared one (signing), got %q", sub.Domain)
	}
	if !sub.Migrated {
		t.Fatalf("migrated is an independent boolean: %+v", sub)
	}
	// A migration is NOT a conflict: both facts are individually assertable.
	if len(sum.ConflictKeys) != 0 {
		t.Fatalf("a legal migration must never fail closed: %v", sum.ConflictKeys)
	}
	// And it is not "declared" either (I5 — no folding upwards).
	if sum.Declared {
		t.Fatal("migrated must not be presented as declared")
	}
}

// ---------------------------------------------------------------------------
// T369 — red example C (load-bearing): a SIGNING-domain window never satisfies
// the verification authority (A7/I7)
// ---------------------------------------------------------------------------

func TestP51T369SigningDomainWindowIsNeverVerifierAuthority(t *testing.T) {
	e := newP51Env(t)
	mPriv, mPub, mID := e.key("moving")

	// Run 1: M is a MANIFEST SIGNING subject; its window is opened for real.
	s1 := e.build([]string{e.signPub, mPub}, "", nil)
	e.activate(s1, mID, p50T0)

	// Run 2: M migrated to the VERIFIER anchor and signs a report.
	s2 := e.build([]string{e.signPub}, mPriv, []string{mPub})
	e.now = p50T1
	rep, err := s2.AttestVerification(100)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	sum := s2.VerifierAuthorityStatus()
	row := p50Row(t, sum, mID)
	if row.Verdict != verifierAuthorityDomainMismatch {
		t.Fatalf("① the row must be domain_mismatch, got %s (%s)", row.Verdict, row.Reason)
	}
	if row.DomainMismatch != 1 || row.Checked != 0 || row.Authorized != 0 {
		t.Fatalf("① a mismatched interval is neither checked nor an authority: %+v", row)
	}
	if sum.State == verifierAuthorityAuthorized || sum.Authorized {
		t.Fatalf("② verifier_authorized must be false, got %s", sum.State)
	}
	if sum.Claims != 0 || sum.DomainMismatches != 1 {
		t.Fatalf("② claims must be zero and the mismatch counted once: %+v", sum)
	}
	// ③ There is no path that judges the SIGNING interval as an authority.
	for k, r := range sum.Keys {
		if r.Verdict == verifierAuthorityAuthorized || r.Authorized > 0 {
			t.Fatalf("③ signer %s reached authorized on a foreign domain's window: %+v", k, r)
		}
	}
	// ④ P42's report and P44's verdict are UNCHANGED: a new dimension, not a
	// re-reading of an existing face.
	rawReport, rerr := json.Marshal(rep)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(rawReport), "domain_mismatch") || strings.Contains(string(rawReport), "verifier_authority") {
		t.Fatalf("④ P42 must carry no Phase 51 vocabulary: %s", rawReport)
	}
	for _, it := range rep.Items {
		if it.Lifecycle != "unbounded" {
			t.Fatalf("④ P42's lifecycle ruler still names the MANIFEST signer: %q", it.Lifecycle)
		}
	}
	vs, lerr := loadVerificationState(s2.verificationConfig())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(vs.entries) != 1 || !vs.verifiable {
		t.Fatalf("④ P44 must still accept the record: verifiable=%v entries=%d errs=%v", vs.verifiable, len(vs.entries), vs.errs)
	}
	if v := verifyVerificationEntrySignatureIn(&vs.entries[0], s2.verifierTrust, s2.verifierForeignKeys()); v.Verdict != sigVerdictOK {
		t.Fatalf("④ P44 must still read signature_ok: %s", v.Verdict)
	}
	// ⑤ The domain face does not pretend the window never existed.
	if got := p51Subject(t, p51DomainFace(s2), mID).Domain; got != lifecycleDomainSigning {
		t.Fatalf("⑤ the domain face must still report signing, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// T370 — a conflict fails closed and WINS the total order (I8/I12)
// ---------------------------------------------------------------------------

func TestP51T370ConflictFailsClosedAndOutranksDeclared(t *testing.T) {
	// (a) A role outside the value domain.
	e := newP51Env(t)
	s := e.build([]string{e.signPub}, "", nil)
	signerID := p51SignerID(t, e.signPriv)
	e.rawRow(s, "bad-role-key", "rotator", lifecycleEventActivated, klTS(p50T0), "")
	e.activate(s, signerID, p50T0)

	sum := p51DomainFace(s)
	if sum.State != lifecycleDomainConflict || sum.Declared {
		t.Fatalf("(a) an invalid role must fail closed at the face, got %s", sum.State)
	}
	if got := p51Subject(t, sum, "bad-role-key").Domain; got != lifecycleDomainConflict {
		t.Fatalf("(a) the offending subject must read conflict, got %q", got)
	}
	if got := p51Subject(t, sum, signerID).Domain; got != lifecycleDomainSigning {
		t.Fatalf("(a) a healthy subject keeps its own verdict: %q", got)
	}
	if len(sum.ConflictKeys) != 1 || sum.ConflictKeys[0] != "bad-role-key" {
		t.Fatalf("(a) conflict keys: %v", sum.ConflictKeys)
	}
	if len(sum.MigratedKeys) != 0 {
		t.Fatalf("(a) a conflict is not a migration: %v", sum.MigratedKeys)
	}

	// (b) ONE event group declaring two domains. `domainOf` is a pure function, so
	// it is pinned directly; the face must fail closed on the same input.
	g := newP51Env(t)
	s2 := g.build([]string{g.signPub}, "", nil)
	first := g.rawRowAt(s2, 1, "", "torn-key", lifecycleDomainSigning, lifecycleEventActivated, klTS(p50T0), "")
	g.rawRowAt(s2, 1, first.EventDigest, "torn-key", lifecycleDomainVerifier, lifecycleEventActivated, klTS(p50T0), "")
	st, lerr := loadKeyLifecycleState(s2.keyLifecycleConfig())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if d := domainOf(st, "torn-key"); d.Domain != lifecycleDomainConflict {
		t.Fatalf("(b) one event group with two domains must be a conflict, got %q", d.Domain)
	}
	sum2 := p51DomainFace(s2)
	if sum2.State != lifecycleDomainConflict || sum2.Declared {
		t.Fatalf("(b) the face must fail closed, got %s", sum2.State)
	}
	if got := p51Subject(t, sum2, "torn-key").Domain; got != lifecycleDomainConflict {
		t.Fatalf("(b) subject domain: %q", got)
	}

	// (c) FIRST HIT of the total order: a declared subject elsewhere must not wash
	// the conflict out (I12 — never by sequential assignment).
	h := newP51Env(t)
	vpriv, vpub, vid := h.key("vak")
	s3 := h.build([]string{h.signPub}, vpriv, []string{vpub})
	h.rawRow(s3, "bad-role-key", "nonsense", lifecycleEventActivated, klTS(p50T0), "")
	h.activate(s3, p51SignerID(t, h.signPriv), p50T0)
	h.activate(s3, vid, p50T0)
	sum3 := p51DomainFace(s3)
	if sum3.State != lifecycleDomainConflict || sum3.Declared {
		t.Fatalf("(c) conflict must outrank declared, got %s (declared=%v)", sum3.State, sum3.Declared)
	}
}

// ---------------------------------------------------------------------------
// T371 — A4-2: the fallback is PER CONSUMER and is the identity (I11/I13)
// ---------------------------------------------------------------------------

func TestP51T371UndeclaredRowsKeepTheirPerConsumerRule(t *testing.T) {
	// (a) the T362 shape: a manifest key decommissioned by a TRUST-FILE edit must
	// keep reading exactly as before, because its row has no domain and the P41
	// rule for such a row IS the pre-Phase-51 rule.
	e := newP51Env(t)
	_, extraPub, extraID := e.key("extra")
	s1 := e.build([]string{e.signPub, extraPub}, "", nil)
	e.rawRow(s1, extraID, "", lifecycleEventActivated, klTS(p50T0), "")
	sum1 := keyLifecycleSummary(s1.keyLifecycleConfig())
	if len(sum1.Authorities) != 1 || sum1.Authorities[0].KeyID != extraID || sum1.ActiveKeyID != extraID {
		t.Fatalf("(a) precondition: run 1 lists the decommissioned key: %+v", sum1)
	}
	s2 := e.build([]string{e.signPub}, "", nil)
	sum2 := keyLifecycleSummary(s2.keyLifecycleConfig())
	if strings.Join(p51Authorizations(sum2), ",") != strings.Join(p51Authorizations(sum1), ",") || sum2.ActiveKeyID != sum1.ActiveKeyID {
		t.Fatalf("(a) a trust-file edit must not change the reading of a no-role row: %+v vs %+v", sum1, sum2)
	}

	// (b) a PRE-PHASE-51 VERIFIER row whose key is STILL in the verifier anchor
	// must stay excluded — the rule the P50 roll-up already used. This is the
	// case a unified "no role ⇒ signing subject" rule would break (MU10).
	g := newP51Env(t)
	vpriv, vpub, vid := g.key("vak")
	s3 := g.build([]string{g.signPub}, vpriv, []string{vpub})
	g.rawRow(s3, vid, "", lifecycleEventActivated, klTS(p50T0), "")
	sum3 := keyLifecycleSummary(s3.keyLifecycleConfig())
	for _, id := range p51Authorizations(sum3) {
		if id == vid {
			t.Fatalf("(b) a no-role verifier row of a STILL-trusted verifier must stay out of authorizations: %+v", sum3.Authorities)
		}
	}
	if sum3.ActiveKeyID == vid {
		t.Fatalf("(b) active_key_id must not become a verifier subject: %q", sum3.ActiveKeyID)
	}

	// (c) a verifier IS configured but the ledger holds only signing-subject rows:
	// the verifier side has no row, and nothing is invented for it.
	h := newP51Env(t)
	vpriv2, vpub2, vid2 := h.key("vak")
	s4 := h.build([]string{h.signPub}, vpriv2, []string{vpub2})
	signerID := p51SignerID(t, h.signPriv)
	h.activate(s4, signerID, p50T0)
	sum4 := keyLifecycleSummary(s4.keyLifecycleConfig())
	if len(sum4.Authorities) != 1 || sum4.Authorities[0].KeyID != signerID {
		t.Fatalf("(c) only the signing subject may be listed: %+v", sum4.Authorities)
	}
	dsum := p51DomainFace(s4)
	if _, ok := dsum.Subjects[vid2]; ok {
		t.Fatalf("(c) a key with no row must not appear as a subject: %v", dsum.Subjects)
	}
	if dsum.SubjectCount != 1 || dsum.State != lifecycleDomainStateDeclared || dsum.UndeclaredEvents != 0 {
		t.Fatalf("(c) the single window IS a declaration and nothing else is claimed: %+v", dsum)
	}
	// A ledger whose rows ALL predate the carrier is the opposite extreme: the
	// empty set of declarations is not a declaration (I6).
	k := newP51Env(t)
	s5 := k.build([]string{k.signPub}, "", nil)
	k.rawRow(s5, "old-key", "", lifecycleEventActivated, klTS(p50T0), "")
	if all := p51DomainFace(s5); all.State != lifecycleDomainUndeclared || all.Declared {
		t.Fatalf("(c) an all-undeclared ledger must read undeclared: %+v", all)
	}
}

// ---------------------------------------------------------------------------
// T372 — no folding: three verdicts, all visible at once (A7/I5)
// ---------------------------------------------------------------------------

func TestP51T372MismatchViolatedAndUnboundedAreThreeValues(t *testing.T) {
	if verifierAuthorityDomainMismatch == verifierAuthorityViolated || verifierAuthorityDomainMismatch == verifierAuthorityUnbounded ||
		verifierAuthorityViolated == verifierAuthorityUnbounded {
		t.Fatal("the three verdicts collapsed into one name")
	}
	if lifecycleDomainUndeclared == lifecycleDomainStateDeclared || lifecycleDomainStateMigrated == lifecycleDomainConflict {
		t.Fatal("the undecidable vocabulary collapsed into the declared one")
	}

	e := newP51Env(t)
	aPriv, aPub, aID := e.key("migA")   // signing-era rows, verifier anchor later
	bPriv, bPub, bID := e.key("verifB") // verifier rows, revoked
	cPriv, cPub, cID := e.key("verifC") // in the anchor, but no ledger row at all

	// Run 1: A's window is opened while A is a SIGNING subject; B's is opened (and
	// terminated) as a VERIFIER.
	t1 := e.build([]string{e.signPub, aPub}, "", nil)
	e.activate(t1, aID, p50T0)
	t2 := e.build([]string{e.signPub}, bPriv, []string{bPub})
	e.activate(t2, bID, p50T0)
	e.terminal(t2, bID, lifecycleEventRevoked, p50T5)

	// Run 2: all three keys are verification signers.
	s := e.build([]string{e.signPub}, cPriv, []string{aPub, bPub, cPub})
	p51Attest(t, s, p50T1, aID, p51SignerFor(t, aPriv), "")
	p51Attest(t, s, p50T6, bID, p51SignerFor(t, bPriv), "")
	p51Attest(t, s, p50T1, cID, p51SignerFor(t, cPriv), "")

	sum := s.VerifierAuthorityStatus()
	rowA, rowB, rowC := p50Row(t, sum, aID), p50Row(t, sum, bID), p50Row(t, sum, cID)
	if rowA.Verdict != verifierAuthorityDomainMismatch {
		t.Fatalf("A: want domain_mismatch, got %s (%s)", rowA.Verdict, rowA.Reason)
	}
	if rowB.Verdict != verifierAuthorityViolated {
		t.Fatalf("B: want violated, got %s (%s)", rowB.Verdict, rowB.Reason)
	}
	if rowC.Verdict != verifierAuthorityUnbounded {
		t.Fatalf("C: want unbounded, got %s (%s)", rowC.Verdict, rowC.Reason)
	}
	if rowA.Verdict == rowB.Verdict || rowB.Verdict == rowC.Verdict || rowA.Verdict == rowC.Verdict {
		t.Fatal("the three verdicts must be distinguishable on one input")
	}
	if sum.DomainMismatches != 1 || sum.Violations != 1 {
		t.Fatalf("each counter must count only its own event: %+v", sum)
	}
	if sum.Claims != 1 || sum.Authorized {
		t.Fatalf("only the violated record was actually evaluated: %+v", sum)
	}
	if sum.State != verifierAuthorityViolated {
		t.Fatalf("the global order puts violated above domain_mismatch: %s", sum.State)
	}
}

// ---------------------------------------------------------------------------
// T373 — non-vacuity, and the empty set is never true (I6)
// ---------------------------------------------------------------------------

func TestP51T373DeclaredIsEarnedAndTheEmptySetIsNot(t *testing.T) {
	e := newP51Env(t)
	vpriv, vpub, vid := e.key("vak")
	_, extraPub, extraID := e.key("extra")
	s := e.build([]string{e.signPub, extraPub}, vpriv, []string{vpub})
	signerID := p51SignerID(t, e.signPriv)
	e.activate(s, signerID, p50T0)
	e.activate(s, vid, p50T0)

	// (a) at least one declared subject, no conflict ⇒ declared ∧ declared_events
	// > 0 (the non-vacuity anchor).
	sum := p51DomainFace(s)
	if sum.State != lifecycleDomainStateDeclared || !sum.Declared {
		t.Fatalf("(a) declared expected: %+v", sum)
	}
	if sum.DeclaredEvents <= 0 {
		t.Fatal("(a) declared ⇒ declared_events > 0")
	}
	// …and a PRE-PHASE-51 row may coexist with it: `declared` does NOT imply an
	// empty `undeclared_events` (review M3).
	e.rawRow(s, extraID, "", lifecycleEventActivated, klTS(p50T0), "")
	sum2 := p51DomainFace(s)
	if sum2.State != lifecycleDomainStateDeclared || !sum2.Declared {
		t.Fatalf("(a) history must not demote a declaration: %+v", sum2)
	}
	if sum2.UndeclaredEvents != 1 {
		t.Fatalf("(a) the undeclared witness must be loud: %+v", sum2)
	}

	// (b) an empty ledger, and an all-undeclared ledger.
	g := newP51Env(t)
	s2 := g.build([]string{g.signPub}, "", nil)
	empty := p51DomainFace(s2)
	if empty.State != lifecycleDomainUndeclared || empty.Declared {
		t.Fatalf("(b) an empty ledger is not a declaration: %+v", empty)
	}
	if empty.SubjectCount != 0 || empty.DeclaredEvents != 0 {
		t.Fatalf("(b) nothing may be counted: %+v", empty)
	}
	h := newP51Env(t)
	s3 := h.build([]string{h.signPub}, "", nil)
	h.rawRow(s3, "old-one", "", lifecycleEventActivated, klTS(p50T0), "")
	h.rawRow(s3, "old-two", "", lifecycleEventActivated, klTS(p50T0), "")
	all := p51DomainFace(s3)
	if all.State != lifecycleDomainUndeclared || all.Declared {
		t.Fatalf("(b) all-undeclared must read undeclared: %+v", all)
	}
	if all.UndeclaredEvents != 2 {
		t.Fatalf("(b) both historical rows must be witnessed: %+v", all)
	}
}

// ---------------------------------------------------------------------------
// T374 — undecidable is LOUD (A7/A8-②)
// ---------------------------------------------------------------------------

func TestP51T374UndecidableIsLoud(t *testing.T) {
	// undeclared
	e := newP51Env(t)
	s := e.build([]string{e.signPub}, "", nil)
	e.rawRow(s, "old-key", "", lifecycleEventActivated, klTS(p50T0), "")
	und := p51DomainFace(s)
	if und.Reason == "" || p51Subject(t, und, "old-key").Reason == "" || und.UndeclaredEvents == 0 {
		t.Fatalf("undeclared must be loud at both levels: %+v", und)
	}

	// migrated
	g := newP51Env(t)
	mPriv, mPub, mID := g.key("moving")
	s1 := g.build([]string{g.signPub}, mPriv, []string{mPub})
	g.activate(s1, mID, p50T0)
	v2Priv, v2Pub, _ := g.key("vak2")
	s2 := g.build([]string{g.signPub, mPub}, v2Priv, []string{v2Pub})
	g.terminal(s2, mID, lifecycleEventRotatedOut, p50T5)
	mig := p51DomainFace(s2)
	if mig.Reason == "" || p51Subject(t, mig, mID).Reason == "" {
		t.Fatalf("migrated must be loud at both levels: %+v", mig)
	}
	if len(mig.MigratedKeys) == 0 {
		t.Fatal("migrated must name its subjects")
	}

	// conflict
	h := newP51Env(t)
	s3 := h.build([]string{h.signPub}, "", nil)
	h.rawRow(s3, "bad-role-key", "rotator", lifecycleEventActivated, klTS(p50T0), "")
	con := p51DomainFace(s3)
	if con.Reason == "" || p51Subject(t, con, "bad-role-key").Reason == "" {
		t.Fatalf("conflict must be loud at both levels: %+v", con)
	}
	if len(con.ConflictKeys) == 0 {
		t.Fatal("conflict must name its subjects")
	}
	// "cannot tell" is never rendered as "no problem".
	if und.Declared || mig.Declared || con.Declared {
		t.Fatal("no undecidable state may be rendered as declared")
	}
}

// ---------------------------------------------------------------------------
// T375 — the read face has zero side effects (A6)
// ---------------------------------------------------------------------------

func TestP51T375TheDomainFaceHasNoSideEffects(t *testing.T) {
	e := newP51Env(t)
	vpriv, vpub, vid := e.key("vak")
	s := e.build([]string{e.signPub}, vpriv, []string{vpub})
	e.activate(s, p51SignerID(t, e.signPriv), p50T0)
	e.activate(s, vid, p50T0)

	paths := []string{keyLifecycleLogPath(e.dir), verificationLogPath(e.dir), anchorLogPath(e.dir)}
	before := map[string][]byte{}
	beforeInfo := map[string]os.FileInfo{}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			before[p] = b
		}
		if fi, err := os.Stat(p); err == nil {
			beforeInfo[p] = fi
		}
	}
	listBefore := p50DirListing(t, e.dir)

	for i := 0; i < 3; i++ {
		if sum := p51DomainFace(s); sum.State == "" {
			t.Fatal("the face must always carry a state")
		}
		_ = s.Status()
	}

	for p := range before {
		after, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s disappeared: %v", p, err)
		}
		if string(after) != string(before[p]) {
			t.Fatalf("%s was modified by a read face", p)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(beforeInfo[p].ModTime()) {
			t.Fatalf("%s mtime moved: %v → %v", p, beforeInfo[p].ModTime(), fi.ModTime())
		}
	}
	if got := p50DirListing(t, e.dir); got != listBefore {
		t.Fatalf("the face created or removed a file:\n before %s\n after  %s", listBefore, got)
	}
}

// ---------------------------------------------------------------------------
// T376 — no new family, stream, route or API parameter (A3/A7-⑨/I9)
// ---------------------------------------------------------------------------

func TestP51T376NoNewFamilyStreamRouteOrParameter(t *testing.T) {
	if got := len(witnessFamilyRegistry()); got != 6 {
		t.Fatalf("the registry must still hold 6 families, got %d", got)
	}
	if got := len(anchorDeliveryStreams()); got != 6 {
		t.Fatalf("the delivery partition must still hold 6 streams, got %d", got)
	}
	if got := len(deliverySweepStreams()); got != 4 {
		t.Fatalf("the sweep set must still hold 4 streams, got %d", got)
	}
	// The domain carrier is NOT an API parameter: the request type is pinned by
	// T365, and the new file registers nothing and routes nothing.
	src := strings.ToLower(p50ReadSource(t, "snapshot_lifecycle_domain.go"))
	for _, banned := range []string{"witnessfamilyregistry(", "anchordeliverystreams(", "deliverysweepstreams(", "handlefunc", "newservemux", "mux.handle", "type keylifecyclerequest"} {
		if strings.Contains(src, banned) {
			t.Fatalf("the new face must register no family, route or parameter, found %q", banned)
		}
	}
	// No new ledger and no new key type: the anchor stream stays its own file.
	if keyLifecycleLogPath("/x") == keyLifecycleAnchorPath("/x") {
		t.Fatal("the anchor stream must stay a separate file from the ledger")
	}
}

// ---------------------------------------------------------------------------
// T377 — P41's verdict face is untouched (I1/I2)
// ---------------------------------------------------------------------------

func TestP51T377Phase41VerdictFaceIsUnchanged(t *testing.T) {
	if sigVerdictBeforeActivation != "signature_before_activation" ||
		sigVerdictAfterRotation != "signature_after_rotation" ||
		sigVerdictAfterRevocation != "signature_after_revocation" ||
		sigVerdictTimeUnparseable != "signature_time_unparseable" {
		t.Fatal("the frozen P41 verdict names changed")
	}
	// The four verdicts still come out of the SAME function on the SAME input.
	e := newP51Env(t)
	s := e.build([]string{e.signPub}, "", nil)
	signerID := p51SignerID(t, e.signPriv)
	e.activate(s, signerID, p50T0)
	e.terminal(s, signerID, lifecycleEventRevoked, p50T5)
	st, err := loadKeyLifecycleState(s.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(signerID)
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, klTS(p50T0.Add(-time.Hour)), a); got.Verdict != sigVerdictBeforeActivation {
		t.Fatalf("before activation: %s", got.Verdict)
	}
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, klTS(p50T6), a); got.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("after revocation: %s", got.Verdict)
	}
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, "not-a-time", a); got.Verdict != sigVerdictTimeUnparseable {
		t.Fatalf("unparseable: %s", got.Verdict)
	}
	// The comparison semantics are still P41's on a DOMAIN-FOLDED interval:
	// `t == not_before` is inside, `t == not_after` is outside, rotation is its
	// own word.
	g := newP51Env(t)
	vpriv, vpub, vid := g.key("vak")
	s2 := g.build([]string{g.signPub}, vpriv, []string{vpub})
	g.activate(s2, vid, p50T0)
	g.terminal(s2, vid, lifecycleEventRotatedOut, p50T5)
	st2, err := loadKeyLifecycleState(s2.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a2, n := authorizationForDomain(s2.keyLifecycleConfig(), st2, vid, lifecycleDomainVerifier)
	if n != 2 {
		t.Fatalf("both verifier-domain rows must fold: %d", n)
	}
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: vid}, klTS(p50T0), a2); got.Verdict != sigVerdictOK {
		t.Fatalf("t == not_before is inside: %s", got.Verdict)
	}
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: vid}, klTS(p50T5), a2); got.Verdict != sigVerdictAfterRotation {
		t.Fatalf("t == not_after is outside, and rotation is its own word: %s", got.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T378 — malformed input never panics (I8's defensive edge)
// ---------------------------------------------------------------------------

func TestP51T378MalformedInputNeverPanics(t *testing.T) {
	legal := map[string]bool{
		lifecycleDomainSigning: true, lifecycleDomainVerifier: true,
		lifecycleDomainUndeclared: true, lifecycleDomainConflict: true,
		lifecycleDomainStateMigrated: true, lifecycleDomainStateDeclared: true,
	}
	for i, tc := range []struct {
		name string
		role string
	}{
		{"empty role", ""},
		{"non-enum role", "SIGNING"},
		{"very long role", strings.Repeat("r", 5000)},
		{"illegal byte role", "sign\x00ing"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d (%s) panicked: %v", i, tc.name, r)
				}
			}()
			e := newP51Env(t)
			s := e.build([]string{e.signPub}, "", nil)
			e.rawRow(s, "malformed", tc.role, lifecycleEventActivated, klTS(p50T0), "")
			sum := p51DomainFace(s)
			if !legal[sum.State] {
				t.Fatalf("case %d (%s): illegal state %q", i, tc.name, sum.State)
			}
			if sum.Declared {
				t.Fatalf("case %d (%s): malformed input must never read declared", i, tc.name)
			}
		}()
	}

	// Invalid JSON on the ledger: fail-closed and loud, no panic.
	g := newP51Env(t)
	s2 := g.build([]string{g.signPub}, "", nil)
	if err := os.WriteFile(keyLifecycleLogPath(g.dir), []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := p51DomainFace(s2)
	if bad.State != lifecycleDomainConflict || bad.Reason == "" || bad.Declared {
		t.Fatalf("invalid JSON must be a loud conflict: %+v", bad)
	}

	// A zero-value entry (every field empty) poisons the ledger (its signature is
	// absent) — a conflict, never a panic and never a declaration.
	h := newP51Env(t)
	s3 := h.build([]string{h.signPub}, "", nil)
	if err := os.WriteFile(keyLifecycleLogPath(h.dir), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zero := p51DomainFace(s3)
	if zero.State == "" || zero.Declared {
		t.Fatalf("a zero-value row must not assert a declaration: %+v", zero)
	}

	// An empty ledger, a nil state and a zero config never panic either.
	e2 := newP51Env(t)
	s4 := e2.build([]string{e2.signPub}, "", nil)
	if got := p51DomainFace(s4); got.State != lifecycleDomainUndeclared {
		t.Fatalf("empty ledger: %s", got.State)
	}
	if got := domainOf(nil, "anything"); got.Domain != lifecycleDomainUndeclared {
		t.Fatalf("nil state: %+v", got)
	}
	if a, n := authorizationForDomain(keyLifecycleConfig{}, nil, "k", lifecycleDomainVerifier); n != 0 || a.Evaluable {
		t.Fatalf("nil state must resolve to an unevaluable interval: %+v %d", a, n)
	}
}

// ---------------------------------------------------------------------------
// T379 — P41's STATUS face: zero regression on no-role input (A4-2 / I11)
// ---------------------------------------------------------------------------

func TestP51T379Phase41StatusFaceIsIdenticalOnNoRoleInput(t *testing.T) {
	// (a) the manifest decommission (T362): same dir, same ledger, trust-file edit.
	e := newP51Env(t)
	_, extraPub, extraID := e.key("extra")
	s1 := e.build([]string{e.signPub, extraPub}, "", nil)
	e.rawRow(s1, extraID, "", lifecycleEventActivated, klTS(p50T0), "")
	sumA1 := keyLifecycleSummary(s1.keyLifecycleConfig())
	s2 := e.build([]string{e.signPub}, "", nil)
	sumA2 := keyLifecycleSummary(s2.keyLifecycleConfig())
	if strings.Join(p51Authorizations(sumA1), ",") != strings.Join(p51Authorizations(sumA2), ",") {
		t.Fatalf("(a) decommission changed the roll-up: %v vs %v", p51Authorizations(sumA1), p51Authorizations(sumA2))
	}
	if sumA2.ActiveKeyID != extraID {
		t.Fatalf("(a) active_key_id changed: %q", sumA2.ActiveKeyID)
	}

	// (b) a still-trusted verifier whose row carries no domain stays excluded.
	g := newP51Env(t)
	vpriv, vpub, vid := g.key("vak")
	signerID := p51SignerID(t, g.signPriv)
	s3 := g.build([]string{g.signPub}, vpriv, []string{vpub})
	g.activate(s3, signerID, p50T0)
	g.rawRow(s3, vid, "", lifecycleEventActivated, klTS(p50T0), "")
	sumB := keyLifecycleSummary(s3.keyLifecycleConfig())
	for _, id := range p51Authorizations(sumB) {
		if id == vid {
			t.Fatalf("(b) a no-role verifier subject must stay excluded: %+v", sumB.Authorities)
		}
	}
	if sumB.ActiveKeyID != signerID {
		t.Fatalf("(b) active_key_id must not move: %q", sumB.ActiveKeyID)
	}
	// (c) the ledger's own counts are the ledger's, not this view's.
	if sumB.EventCount != 2 {
		t.Fatalf("(c) event count: %d", sumB.EventCount)
	}
}

// ---------------------------------------------------------------------------
// T380 — P50's authority face: zero regression on no-role input (A4-2 / I11)
// ---------------------------------------------------------------------------

func TestP51T380VerifierAuthorityIsUnchangedOnNoRoleInput(t *testing.T) {
	// A PRE-PHASE-51 verifier window (no role) judged exactly as Phase 50 judged
	// it: `authorized`, and NEVER `domain_mismatch`.
	e := newP51Env(t)
	vpriv, vpub, vid := e.key("vak")
	s := e.build([]string{e.signPub}, vpriv, []string{vpub})
	e.rawRow(s, vid, "", lifecycleEventActivated, klTS(p50T0), "")
	e.rawRow(s, vid, "", lifecycleEventRevoked, "", klTS(p50T5))
	e.now = p50T1
	if _, err := s.AttestVerification(100); err != nil {
		t.Fatalf("attest: %v", err)
	}
	sum := s.VerifierAuthorityStatus()
	row := p50Row(t, sum, vid)
	if row.Verdict != verifierAuthorityAuthorized || row.Authorized != 1 || row.Checked != 1 {
		t.Fatalf("a no-role verifier window must be judged exactly as before: %+v", row)
	}
	if row.DomainMismatch != 0 || sum.DomainMismatches != 0 {
		t.Fatalf("domain_mismatch must not fire on an in-ledger verification row: %+v", sum)
	}
	if sum.State != verifierAuthorityAuthorized || !sum.Authorized {
		t.Fatalf("the face must still read authorized: %+v", sum)
	}
	// The new counter is present and zero: the vocabulary grew, the existing
	// values did not move.
	doc, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"verifier_authority_domain_mismatches":0`) {
		t.Fatalf("the new counter must be reported (zero on old input): %s", doc)
	}

	// Only a window DECLARED to another domain produces a mismatch.
	g := newP51Env(t)
	mPriv, mPub, mID := g.key("moving")
	t1 := g.build([]string{g.signPub, mPub}, "", nil)
	g.activate(t1, mID, p50T0)
	t2 := g.build([]string{g.signPub}, mPriv, []string{mPub})
	g.now = p50T1
	if _, err := t2.AttestVerification(100); err != nil {
		t.Fatalf("attest: %v", err)
	}
	if sum2 := t2.VerifierAuthorityStatus(); sum2.DomainMismatches != 1 {
		t.Fatalf("a declared signing-domain window must produce exactly one mismatch: %+v", sum2)
	}
}

// ---------------------------------------------------------------------------
// T381 — tail deletion is caught by P49 and NOT re-implemented here (A8-⑤)
// ---------------------------------------------------------------------------

func TestP51T381DeletionProtectionReusesP49(t *testing.T) {
	e := newP51Env(t)
	vpriv, vpub, vid := e.key("vak")
	s := e.buildAnchored([]string{e.signPub}, vpriv, []string{vpub})
	e.activate(s, vid, p50T0)

	if p51DomainFace(s).State != lifecycleDomainStateDeclared {
		t.Fatal("precondition: the domain face must read declared before the deletion")
	}
	if rz := s.AnchorRealizationStatus(); rz.KeyLifecycle.Verdict != realizationFamilyRealized {
		t.Fatalf("precondition: P49 must read realized: %+v (%s)", rz.KeyLifecycle, rz.KeyLifecycle.Reason)
	}

	// Remove the rows that carry the domain; the anchor entry written after them
	// stays. No legal producer of that state exists.
	p49KeepLines(t, keyLifecycleLogPath(e.dir), 0, keyLifecycleGroupOf)

	// This face can only report what the surviving evidence supports — and it
	// reports it HONESTLY: no domain, loudly, never a declaration.
	after := p51DomainFace(s)
	if after.State == lifecycleDomainStateDeclared || after.Declared {
		t.Fatalf("after the deletion the face must not claim a declaration: %+v", after)
	}
	if after.State != lifecycleDomainUndeclared && after.State != lifecycleDomainConflict {
		t.Fatalf("the degraded state must be one of the honest ones: %+v", after)
	}
	if after.State == lifecycleDomainUndeclared && after.Reason == "" {
		t.Fatalf("the degradation must be loud: %+v", after)
	}
	// P49 catches it — in the same case, with both faces true at once.
	rz := s.AnchorRealizationStatus()
	if rz.KeyLifecycle.Verdict != realizationFamilyUnrealized {
		t.Fatalf("P49 must report anchor_unrealized for key_lifecycle, got %s (%s)", rz.KeyLifecycle.Verdict, rz.KeyLifecycle.Reason)
	}
	if rz.State != realizationStateUnrealized {
		t.Fatalf("the global realization scalar must reflect it: %s", rz.State)
	}
}

// ---------------------------------------------------------------------------
// T382 — mutation sensitivity, pinned at the DATA level. Each baseline below is
// exactly the dependency its mutant removes.
// ---------------------------------------------------------------------------

func TestP51T382MutationSensitivity(t *testing.T) {
	// MU1 (drop the domain write) — a fresh row must carry it.
	e := newP51Env(t)
	vpriv, vpub, vid := e.key("vak")
	s := e.build([]string{e.signPub}, vpriv, []string{vpub})
	signerID := p51SignerID(t, e.signPriv)
	if a := e.activate(s, signerID, p50T0); a.Role != lifecycleDomainSigning {
		t.Fatalf("MU1 baseline: signing row role=%q", a.Role)
	}
	if b := e.activate(s, vid, p50T0); b.Role != lifecycleDomainVerifier {
		t.Fatalf("MU1 baseline: verifier row role=%q", b.Role)
	}

	// MU2 (resolve a DECLARED row by the live trust file = the HEAD shape) — the
	// retirement baseline: VAK1's row declares verifier while run 2's trust file
	// no longer holds VAK1, so a live-trust lookup would re-list it.
	e2 := newP51Env(t)
	v1Priv, v1Pub, v1ID := e2.key("vak1")
	v2Priv, v2Pub, _ := e2.key("vak2")
	s1 := e2.build([]string{e2.signPub}, v1Priv, []string{v1Pub})
	e2.activate(s1, v1ID, p50T0)
	s2 := e2.build([]string{e2.signPub}, v2Priv, []string{v2Pub})
	if cfg := s2.keyLifecycleConfig(); cfg.verifierTrust != nil {
		if _, still := cfg.verifierTrust.keys[v1ID]; still {
			t.Fatal("MU2 baseline: run 2's trust file must not contain the retired VAK")
		}
	}
	st, err := loadKeyLifecycleState(s2.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if rowsForDomain(s2.keyLifecycleConfig(), st, v1ID, lifecycleDomainSigning) != 0 {
		t.Fatal("MU2 baseline: a declared verifier row must fold into ZERO signing rows")
	}
	for _, id := range p51Authorizations(keyLifecycleSummary(s2.keyLifecycleConfig())) {
		if id == v1ID {
			t.Fatalf("MU2 baseline: the retired verifier must stay out: %s", id)
		}
	}

	// MU3 (fold undeclared into declared) — an all-undeclared ledger.
	e3 := newP51Env(t)
	s3 := e3.build([]string{e3.signPub}, "", nil)
	e3.rawRow(s3, "old-key", "", lifecycleEventActivated, klTS(p50T0), "")
	if u := p51DomainFace(s3); u.State != lifecycleDomainUndeclared || u.Declared || u.DeclaredEvents != 0 {
		t.Fatalf("MU3 baseline: %+v", u)
	}

	// MU4 (fold migrated into conflict) — a two-domain subject.
	e4 := newP51Env(t)
	mPriv, mPub, mID := e4.key("moving")
	t1 := e4.build([]string{e4.signPub}, mPriv, []string{mPub})
	e4.activate(t1, mID, p50T0)
	v4Priv, v4Pub, _ := e4.key("vak2")
	t2 := e4.build([]string{e4.signPub, mPub}, v4Priv, []string{v4Pub})
	e4.terminal(t2, mID, lifecycleEventRotatedOut, p50T5)
	if mig := p51DomainFace(t2); mig.State != lifecycleDomainStateMigrated || len(mig.ConflictKeys) != 0 {
		t.Fatalf("MU4 baseline: %+v", mig)
	}

	// MU5/MU6 (fold domain_mismatch into unbounded/violated, or let it stay
	// authorized) — one signing-domain window in front of the verifier face.
	e5 := newP51Env(t)
	m5Priv, m5Pub, m5ID := e5.key("moving5")
	t3 := e5.build([]string{e5.signPub, m5Pub}, "", nil)
	e5.activate(t3, m5ID, p50T0)
	t4 := e5.build([]string{e5.signPub}, m5Priv, []string{m5Pub})
	e5.now = p50T1
	if _, err := t4.AttestVerification(100); err != nil {
		t.Fatalf("attest: %v", err)
	}
	f5 := t4.VerifierAuthorityStatus()
	r5 := p50Row(t, f5, m5ID)
	if r5.Verdict != verifierAuthorityDomainMismatch || r5.Authorized != 0 || r5.Checked != 0 {
		t.Fatalf("MU5/MU6 baseline: %+v", r5)
	}
	if f5.Authorized || f5.Claims != 0 {
		t.Fatalf("MU5/MU6 baseline (face): %+v", f5)
	}

	// MU7 (let the caller declare the domain) — the request type has no such
	// field, and the new file defines no request type of its own.
	if reflect.TypeOf(keyLifecycleRequest{}).NumField() != 4 {
		t.Fatal("MU7 baseline: keyLifecycleRequest gained a field")
	}

	// MU8 (sequential assignment of the face state) — a conflict must survive a
	// declared subject being processed after it.
	e6 := newP51Env(t)
	s6 := e6.build([]string{e6.signPub}, "", nil)
	e6.rawRow(s6, "aaa-bad", "rotator", lifecycleEventActivated, klTS(p50T0), "")
	e6.activate(s6, p51SignerID(t, e6.signPriv), p50T0)
	f6 := p51DomainFace(s6)
	if f6.State != lifecycleDomainConflict || f6.Declared {
		t.Fatalf("MU8 baseline: %+v", f6)
	}

	// MU9 (side effects on read) — pinned in full by T375; the same dependency is
	// asserted compactly here so the mutant has a named baseline in this case too.
	before, _ := os.ReadFile(keyLifecycleLogPath(e6.dir))
	_ = p51DomainFace(s6)
	after, _ := os.ReadFile(keyLifecycleLogPath(e6.dir))
	if string(before) != string(after) {
		t.Fatal("MU9 baseline: the read face must not write")
	}

	// MU10 (unify A4-2 to "no role ⇒ signing subject") — a no-role VERIFIER row
	// whose key is STILL trusted must stay in the VERIFICATION fold only.
	e7 := newP51Env(t)
	v7Priv, v7Pub, v7ID := e7.key("vak")
	s7 := e7.build([]string{e7.signPub}, v7Priv, []string{v7Pub})
	e7.rawRow(s7, v7ID, "", lifecycleEventActivated, klTS(p50T0), "")
	st7, err := loadKeyLifecycleState(s7.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if rowsForDomain(s7.keyLifecycleConfig(), st7, v7ID, lifecycleDomainSigning) != 0 {
		t.Fatal("MU10 baseline: a still-trusted no-role verifier row must NOT fold into the signing domain")
	}
	if rowsForDomain(s7.keyLifecycleConfig(), st7, v7ID, lifecycleDomainVerifier) != 1 {
		t.Fatal("MU10 baseline: the same row DOES fold into the verification domain")
	}
	for _, id := range p51Authorizations(keyLifecycleSummary(s7.keyLifecycleConfig())) {
		if id == v7ID {
			t.Fatal("MU10 baseline: the no-role verifier subject must stay out of authorizations")
		}
	}
}

// ---------------------------------------------------------------------------
// T383 — the faces are simultaneously visible and never mask each other (I5)
// ---------------------------------------------------------------------------

func TestP51T383FacesAreSimultaneouslyVisible(t *testing.T) {
	e := newP51Env(t)
	mPriv, mPub, mID := e.key("moving")
	signerID := p51SignerID(t, e.signPriv)

	// World 1 (red example C): the P50 face refuses the window's authority while
	// the domain face says exactly what it was for, and P41 still reads its own
	// signing subject.
	s1 := e.build([]string{e.signPub, mPub}, "", nil)
	e.activate(s1, mID, p50T0)
	e.activate(s1, signerID, p50T0)
	s2 := e.build([]string{e.signPub}, mPriv, []string{mPub})
	e.now = p50T1
	if _, err := s2.AttestVerification(100); err != nil {
		t.Fatalf("attest: %v", err)
	}
	va := s2.VerifierAuthorityStatus()
	if p50Row(t, va, mID).Verdict != verifierAuthorityDomainMismatch {
		t.Fatalf("P50 must refuse the foreign window's authority: %+v", va.Keys)
	}
	if got := p51Subject(t, p51DomainFace(s2), mID).Domain; got != lifecycleDomainSigning {
		t.Fatalf("the domain face must still name the real domain: %q", got)
	}
	found := false
	for _, id := range p51Authorizations(keyLifecycleSummary(s2.keyLifecycleConfig())) {
		if id == signerID {
			found = true
		}
	}
	if !found {
		t.Fatal("P41's roll-up must be unaffected by the P50 refusal")
	}

	// World 2 (red example A): the retirement is visible in all three places at
	// once, and the ledger itself stays verifiable across the rotation.
	e2 := newP51Env(t)
	v1Priv, v1Pub, v1ID := e2.key("vak1")
	v2Priv, v2Pub, _ := e2.key("vak2")
	t1 := e2.build([]string{e2.signPub}, v1Priv, []string{v1Pub})
	e2.activate(t1, v1ID, p50T0)
	t2 := e2.build([]string{e2.signPub}, v2Priv, []string{v2Pub})
	if id := keyLifecycleSummary(t2.keyLifecycleConfig()).ActiveKeyID; id == v1ID {
		t.Fatalf("active_key_id must not become the retired verifier: %q", id)
	}
	if got := p51Subject(t, p51DomainFace(t2), v1ID).Domain; got != lifecycleDomainVerifier {
		t.Fatalf("the domain face must still name the retired subject's domain: %q", got)
	}
	st, err := loadKeyLifecycleState(t2.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !st.verifiable {
		t.Fatalf("the ledger stays verifiable across the rotation: %v", st.errs)
	}
}
