package server

// Phase 40 — Publication Anchoring (ADR-052 scope, ADR-053 architecture).
//
// What Phase 35~39 cannot prove: every one of them roots its trust in ONE host
// plus ONE Ed25519 private key. Whoever wields that key can delete a snapshot
// and re-sign the remainder into a self-consistent chain that passes every
// local check. Anchoring pushes the per-publication commitment OUTSIDE that
// trust domain, so "a publication existed and was later removed" becomes
// observable by someone who never trusted the host.
//
// This file is deliberately narrow. It never touches the Phase 35 status, the
// Phase 37 signature verdict, the Phase 38 chain verdict, the Phase 39 ledger or
// the Phase 36 coverage surface (ADR-053 I8): the anchor dimension is ORTHOGONAL
// and can only add a fourth, independent verdict.
//
// Persistence reuses the shared append-only primitive (appendonly_log.go), so
// the Phase 39 discipline and this one cannot drift apart.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const chainAnchorFile = "chain-anchor.jsonl"

// keyLifecycleAnchorFile (Phase 41) is the SECOND anchor record stream.
// Lifecycle events are deliberately kept out of `chain-anchor.jsonl`: their
// sequence space is independent, and mixing the two families in one file would
// make the shared group classifier treat a publication seq and a lifecycle seq
// as the same group (ADR-055 §9).
const keyLifecycleAnchorFile = "key-lifecycle-anchor.jsonl"

// anchorKind values. The empty kind is the Phase 40 publication family, so
// every byte ever written by Phase 40 stays identical.
const (
	anchorKindPublication  = ""
	anchorKindKeyLifecycle = "key_lifecycle"
)

// Delivery states of one anchor_seq. Only `anchored` is a CONFIRMED state —
// an HTTP 2xx alone is never a confirmation (ADR-053 §5.2).
const (
	anchorStatePending    = "pending"
	anchorStateAnchored   = "anchored"
	anchorStateUnanchored = "unanchored"
)

// foreignStreamVerdict is the one signature verdict this Phase adds to the
// Phase 37 set. Like `key_unknown` it is UNVERIFIABLE, never tamper evidence:
// an entry written for a different stream (for example before the export
// directory was moved) simply is not ours (ADR-052 §5, R40-5).
const sigVerdictForeignStream = "foreign_stream"

// anchorEntry is one anchor record.
//
// The signed payload covers ONLY the identity and business fields; the delivery
// state is operationally mutable and is NEVER signed — binding it would make
// every state advance invalidate the proof (ADR-053 §1.3).
type anchorEntry struct {
	// ---- covered by the signature ----
	PublicationID      int64  `json:"publication_id"`
	AnchorSeq          int64  `json:"anchor_seq"`
	ManifestDigest     string `json:"manifest_digest"`
	PrevPublicationID  int64  `json:"prev_publication_id"`
	PrevManifestDigest string `json:"prev_manifest_digest,omitempty"`
	RecordedAt         string `json:"recorded_at"`
	KeyID              string `json:"key_id"`    // derived (P37), never configured
	StreamID           string `json:"stream_id"` // derived (R40-2), never configured

	// Phase 41 (the second anchor family). All omitempty, so a Phase 40
	// publication entry serializes byte-for-byte as it always has.
	Kind        string `json:"kind,omitempty"`         // "key_lifecycle"
	EventSeq    int64  `json:"event_seq,omitempty"`    // lifecycle event identity
	EventDigest string `json:"event_digest,omitempty"` // sha256(canonical lifecycle payload)
	EventType   string `json:"event_type,omitempty"`
	NotAfter    string `json:"not_after,omitempty"`

	// ---- delivery state: mutable, never signed ----
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	AckID      string `json:"ack_id,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	AnchoredAt string `json:"anchored_at,omitempty"`

	Sig string `json:"sig,omitempty"`
}

// anchorSigned is the exact structure canonicalAnchorPayload serializes.
type anchorSigned struct {
	PublicationID      int64  `json:"publication_id"`
	AnchorSeq          int64  `json:"anchor_seq"`
	ManifestDigest     string `json:"manifest_digest"`
	PrevPublicationID  int64  `json:"prev_publication_id"`
	PrevManifestDigest string `json:"prev_manifest_digest,omitempty"`
	RecordedAt         string `json:"recorded_at"`
	KeyID              string `json:"key_id"`
	StreamID           string `json:"stream_id"`
	// Phase 41 — omitempty keeps the Phase 40 payload byte-identical.
	Kind        string `json:"kind,omitempty"`
	EventSeq    int64  `json:"event_seq,omitempty"`
	EventDigest string `json:"event_digest,omitempty"`
	EventType   string `json:"event_type,omitempty"`
	NotAfter    string `json:"not_after,omitempty"`
}

func anchorSignedFields(e *anchorEntry) anchorSigned {
	return anchorSigned{
		PublicationID:      e.PublicationID,
		AnchorSeq:          e.AnchorSeq,
		ManifestDigest:     e.ManifestDigest,
		PrevPublicationID:  e.PrevPublicationID,
		PrevManifestDigest: e.PrevManifestDigest,
		RecordedAt:         e.RecordedAt,
		KeyID:              e.KeyID,
		StreamID:           e.StreamID,
		Kind:               e.Kind,
		EventSeq:           e.EventSeq,
		EventDigest:        e.EventDigest,
		EventType:          e.EventType,
		NotAfter:           e.NotAfter,
	}
}

// identityID is the id an anchor entry is addressed by: the publication id for
// the Phase 40 family, the lifecycle event_seq for the Phase 41 family.
func (e *anchorEntry) identityID() int64 {
	if e.Kind == anchorKindKeyLifecycle {
		return e.EventSeq
	}
	return e.PublicationID
}

func serializeAnchorEntryBytes(e *anchorEntry) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// canonicalAnchorPayload returns the exact bytes the signature covers: the
// identity and business fields only.
func canonicalAnchorPayload(e *anchorEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil anchor entry")
	}
	data, err := json.Marshal(anchorSignedFields(e))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// anchorPayloadEqual reports whether two entries make the SAME statement. It is
// the discriminator behind ADR-053 I5: same seq + same payload is a legal state
// advance, same seq + different payload is a conflict.
func anchorPayloadEqual(a, b anchorEntry) bool {
	return anchorSignedFields(&a) == anchorSignedFields(&b)
}

// anchorID returns the triple the witness and the local log must agree on.
type anchorID struct {
	PublicationID int64  `json:"publication_id"`
	AnchorSeq     int64  `json:"anchor_seq"`
	Digest        string `json:"digest"`
}

func (e *anchorEntry) id() anchorID {
	return anchorID{PublicationID: e.PublicationID, AnchorSeq: e.AnchorSeq, Digest: e.ManifestDigest}
}

// ---------------------------------------------------------------------------
// Identity — derived, never configured (R40-2)
// ---------------------------------------------------------------------------

// streamIDForPublicKey derives the non-configurable stream identity:
// SHA-256(raw public key ‖ 0x00 ‖ absolute export directory)[:8] hex.
//
// A configurable deployment id would only be a DECLARATION: whoever can rewrite
// the config can make two different streams claim the same identity, which is
// exactly the confusion R40-2 has to prevent. Binding the identity to the
// absolute export directory also means a moved or renamed directory is a
// different stream — see R40-5 for the (accepted) cost of that.
func streamIDForPublicKey(pub ed25519.PublicKey, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("stream id: cannot resolve %q: %w", dir, err)
	}
	h := sha256.New()
	h.Write(pub)
	h.Write([]byte{0x00})
	h.Write([]byte(filepath.ToSlash(abs)))
	return hex.EncodeToString(h.Sum(nil)[:8]), nil
}

// (s *exportSigner) streamIDFor is the signing-side derivation.
func (s *exportSigner) streamIDFor(dir string) (string, error) {
	if s == nil || len(s.priv) != ed25519.PrivateKeySize {
		return "", errors.New("stream id: no signing key configured")
	}
	pub, ok := s.priv.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("stream id: not an Ed25519 key")
	}
	return streamIDForPublicKey(pub, dir)
}

// anchorIdentity is the local identity used to decide whether an external
// witness sequence is even ABOUT this stream. accepted holds every
// "key_id|stream_id" pair this deployment can own (normally exactly one).
type anchorIdentity struct {
	KeyID    string
	StreamID string
	accepted map[string]bool
}

func (id *anchorIdentity) accepts(keyID, streamID string) bool {
	if id == nil {
		return false
	}
	return id.accepted[keyID+"|"+streamID]
}

// deriveAnchorIdentity builds the local identity. The signing key wins when it
// is configured; otherwise the trust anchor is used (a read-only deployment
// still knows which stream it is looking at).
func deriveAnchorIdentity(signer *exportSigner, trust *exportTrustStore, dir string) (*anchorIdentity, error) {
	id := &anchorIdentity{accepted: map[string]bool{}}
	if signer != nil {
		sid, err := signer.streamIDFor(dir)
		if err != nil {
			return nil, err
		}
		id.KeyID = signer.keyID
		id.StreamID = sid
		id.accepted[signer.keyID+"|"+sid] = true
		return id, nil
	}
	if trust == nil || len(trust.keys) == 0 {
		return id, nil
	}
	keyIDs := make([]string, 0, len(trust.keys))
	for keyID := range trust.keys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		sid, err := streamIDForPublicKey(trust.keys[keyID], dir)
		if err != nil {
			return nil, err
		}
		id.accepted[keyID+"|"+sid] = true
	}
	if len(keyIDs) == 1 {
		id.KeyID = keyIDs[0]
		if sid, err := streamIDForPublicKey(trust.keys[keyIDs[0]], dir); err == nil {
			id.StreamID = sid
		}
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Signing / verification — the frozen Phase 37 decision order, plus the
// stream binding of R40-2.
// ---------------------------------------------------------------------------

func (s *exportSigner) signAnchorEntry(e *anchorEntry, dir string, at time.Time) error {
	if s == nil {
		return errors.New("no signer configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("sign key: private key is not a valid Ed25519 key")
	}
	sid, err := s.streamIDFor(dir)
	if err != nil {
		return err
	}
	e.KeyID = s.keyID
	e.StreamID = sid
	e.Sig = ""
	payload, perr := canonicalAnchorPayload(e)
	if perr != nil {
		return perr
	}
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyAnchorEntrySignature applies the Phase 37 decision order to an anchor
// entry and adds the one new verdict this Phase needs (foreign_stream).
func verifyAnchorEntrySignature(e *anchorEntry, trust *exportTrustStore, dir string) SignatureVerdict {
	if e == nil || e.Sig == "" || e.KeyID == "" || e.StreamID == "" {
		return SignatureVerdict{Verdict: sigVerdictAbsent, Detail: "anchor entry carries no usable signature block"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(e.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: e.KeyID, Detail: "anchor signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: e.KeyID, Detail: "no trusted public keys are configured"}
	}
	pub, known := trust.keys[e.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: e.KeyID, Detail: "anchor key_id is not in the trusted key set"}
	}
	// The stream binding is checked BEFORE the signature: an entry that was
	// written for another stream is not ours, and saying so is not tamper
	// evidence (R40-5).
	wantStream, serr := streamIDForPublicKey(pub, dir)
	if serr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: e.KeyID, Detail: serr.Error()}
	}
	if e.StreamID != wantStream {
		return SignatureVerdict{Verdict: sigVerdictForeignStream, KeyID: e.KeyID, Detail: "anchor stream_id does not belong to this export directory (foreign stream, not tamper evidence)"}
	}
	payload, perr := canonicalAnchorPayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: e.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: e.KeyID, Detail: "anchor signature does not verify against the trusted key"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: e.KeyID}
}

// ---------------------------------------------------------------------------
// Persistence — the shared primitive, with the anchor's grouping rule injected.
// ---------------------------------------------------------------------------

// anchorGroupOf is the classifier the anchor log injects: one `anchor_seq` is
// ONE group. Compaction may therefore only ever drop a seq with all of its
// state lines — dropping a newer `anchored` line while keeping its older
// `pending` line would roll a confirmed record back to unconfirmed, i.e. wash
// evidence away (ADR-053 I3).
func anchorGroupOf(raw []byte) (int64, bool) {
	var e anchorEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return e.AnchorSeq, true
}

// anchorProblem is one anchor record that exists but cannot be trusted.
type anchorProblem struct {
	AnchorSeq     int64  `json:"anchor_seq"`
	PublicationID int64  `json:"publication_id,omitempty"`
	Verdict       string `json:"verdict"`
	Detail        string `json:"detail,omitempty"`
}

// anchorWindow is the local evidence window of R40-1: the contiguous run of
// surviving anchor_seq values. It is what separates a witness for a record we
// legally compacted away (outside, not assertable) from a witness for a record
// that should still be here (inside ⇒ broken).
type anchorWindow struct {
	MinSeq     int64 `json:"min_seq"`
	MaxSeq     int64 `json:"max_seq"`
	Entries    int   `json:"entries"`
	Continuous bool  `json:"continuous"`
}

// anchorState is the read-only view the read surface and the reconciler work
// from. Building it never writes, repairs, or compacts.
type anchorState struct {
	latest    map[int64]anchorEntry // anchor_seq -> the group's LAST line
	byID      map[int64]anchorEntry // publication_id -> the usable entry
	unusable  []anchorProblem
	conflicts []int64 // seq recorded with contradictory payloads
	window    anchorWindow
	total     int // groups on disk
}

func (a *anchorState) seqs() []int64 {
	out := make([]int64, 0, len(a.latest))
	for seq := range a.latest {
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// loadAnchorState reads the anchor log. It is STRICTLY fail-closed: an
// unclassifiable line makes the whole load fail (ADR-053 §6) — there is no
// skip-and-continue, because skipping is how a tampered prefix disappears.
func loadAnchorState(dir string, trust *exportTrustStore) (*anchorState, error) {
	return loadAnchorStatePath(anchorLogPath(dir), dir, trust)
}

// loadAnchorStatePath is the path-parameterised form introduced by Phase 41 so
// the second (lifecycle) anchor stream can reuse one implementation instead of
// growing a second copy of the discipline.
func loadAnchorStatePath(path, dir string, trust *exportTrustStore) (*anchorState, error) {
	st := &anchorState{latest: map[int64]anchorEntry{}, byID: map[int64]anchorEntry{}}
	lines, ok, err := readLogLines(path, anchorGroupOf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("anchor log holds %d unclassifiable line(s); refusing to evaluate it", countUnclassified(lines))
	}
	conflicted := map[int64]bool{}
	for i := range lines {
		var e anchorEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			// Unreachable: the classifier already parsed this line.
			return nil, fmt.Errorf("anchor line %d is not readable: %w", i+1, jerr)
		}
		if prev, seen := st.latest[e.AnchorSeq]; seen && !anchorPayloadEqual(prev, e) {
			// Same seq, different statement ⇒ conflict, never last-write-wins.
			conflicted[e.AnchorSeq] = true
		}
		st.latest[e.AnchorSeq] = e // last line of the group wins
	}
	for seq := range conflicted {
		st.conflicts = append(st.conflicts, seq)
		delete(st.latest, seq)
	}
	sort.Slice(st.conflicts, func(i, j int) bool { return st.conflicts[i] < st.conflicts[j] })

	seqs := st.seqs()
	st.total = len(seqs)
	if len(seqs) > 0 {
		st.window.MinSeq = seqs[0]
		st.window.MaxSeq = seqs[len(seqs)-1]
		st.window.Entries = len(seqs)
		st.window.Continuous = true
		for i := 1; i < len(seqs); i++ {
			if seqs[i] != seqs[i-1]+1 {
				st.window.Continuous = false
				break
			}
		}
	}

	// Usability is a separate question from existence: a record we cannot trust
	// still OCCUPIES its seq and therefore still shapes the window.
	for _, seq := range seqs {
		e := st.latest[seq]
		v := verifyAnchorEntrySignature(&e, trust, dir)
		if v.Verdict != sigVerdictOK {
			st.unusable = append(st.unusable, anchorProblem{AnchorSeq: seq, PublicationID: e.identityID(), Verdict: v.Verdict, Detail: v.Detail})
			continue
		}
		st.byID[e.identityID()] = e
	}
	return st, nil
}

// nextAnchorSeq allocates the next seq: max(surviving)+1, or 1 for an empty
// log. It is only ever consumed by a SUCCESSFUL append (ADR-053 §4.2), which is
// what makes the surviving seqs contiguous (I4). An unclassifiable line makes
// allocation fail-closed too — we cannot know which seqs are taken.
func nextAnchorSeq(dir string) (int64, error) { return nextAnchorSeqPath(anchorLogPath(dir)) }

func nextAnchorSeqPath(path string) (int64, error) {
	st, err := loadAnchorStatePath(path, filepath.Dir(path), nil)
	if err != nil {
		return 0, err
	}
	if len(st.latest) == 0 {
		return 1, nil
	}
	return st.window.MaxSeq + 1, nil
}

// appendAnchorEntry records one anchor event.
//
//	same seq + same payload     → state advance (legal: pending → anchored)
//	same seq + different payload→ conflict, nothing is written (ADR-053 I5)
//
// An unclassifiable line makes the append FAIL-CLOSED and leaves the file
// byte-identical (ADR-053 I2).
func appendAnchorEntry(dir string, capacity int, e anchorEntry) error {
	return appendAnchorEntryPath(anchorLogPath(dir), capacity, e)
}

func appendAnchorEntryPath(path string, capacity int, e anchorEntry) error {
	lines, _, err := readLogLines(path, anchorGroupOf)
	if err != nil {
		return err
	}
	unclassified := 0
	var prev *anchorEntry
	for i := range lines {
		if !lines[i].classified {
			unclassified++
			continue
		}
		if lines[i].group != e.AnchorSeq {
			continue
		}
		var p anchorEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			return fmt.Errorf("anchor line for seq %d is not readable: %w", e.AnchorSeq, jerr)
		}
		prev = &p // last line of the group wins
	}
	if unclassified > 0 {
		return fmt.Errorf("anchor log holds %d unclassifiable line(s); refusing to append — rewriting the file would silently discard that evidence", unclassified)
	}
	if prev != nil && !anchorPayloadEqual(*prev, e) {
		return fmt.Errorf("anchor conflict: anchor_seq %d already recorded with a different payload", e.AnchorSeq)
	}

	raw, merr := serializeAnchorEntryBytes(&e)
	if merr != nil {
		return merr
	}
	// TRUE append: nothing already on disk is rewritten.
	if aerr := appendLogLine(path, raw); aerr != nil {
		return aerr
	}
	if capacity > 0 {
		return compactAnchorPrefixPath(path, capacity)
	}
	return nil
}

func compactAnchorPrefix(dir string, capacity int) error {
	return compactAnchorPrefixPath(anchorLogPath(dir), capacity)
}

// compactAnchorPrefixPath bounds the log by whole seq groups — with one hard rule:
// it NEVER drops an UNCONFIRMED group. A `pending` group carries a delivery
// obligation that A5 forbids re-creating (no re-dispatch), so evicting it would
// silently erase the only thing that still remembers the delivery is owed
// (ADR-053 §4.4 / T128). When the oldest group is unconfirmed the log is
// deliberately left over capacity and the caller is told.
func compactAnchorPrefixPath(path string, capacity int) error {
	st, err := loadAnchorStatePath(path, filepath.Dir(path), nil)
	if err != nil {
		return err
	}
	if st.window.Entries <= capacity {
		return nil
	}
	if oldest, ok := st.latest[st.window.MinSeq]; ok && oldest.State != anchorStateAnchored {
		return fmt.Errorf("anchor capacity %d exceeded (%d groups) but seq %d is still %s — refusing to evict an unconfirmed group",
			capacity, st.window.Entries, st.window.MinSeq, oldest.State)
	}
	return compactLogPrefixGroups(path, capacity, anchorGroupOf)
}

// ---------------------------------------------------------------------------
// Witness transport (ADR-053 §5) — minimal and not vendor-specific.
// ---------------------------------------------------------------------------

// anchorRequest is the wire form handed to the witness.
type anchorRequest struct {
	V                  int    `json:"v"`
	KeyID              string `json:"key_id"`
	StreamID           string `json:"stream_id"`
	PublicationID      int64  `json:"publication_id"`
	AnchorSeq          int64  `json:"anchor_seq"`
	AnchorDigest       string `json:"anchor_digest"`
	ManifestDigest     string `json:"manifest_digest"`
	PrevPublicationID  int64  `json:"prev_publication_id"`
	PrevManifestDigest string `json:"prev_manifest_digest,omitempty"`
	RecordedAt         string `json:"recorded_at"`
	Sig                string `json:"sig"`
}

// anchorDigestOf is the digest the witness and the local log compare: the
// SHA-256 of the canonical anchor payload. It is DERIVED on both sides, never
// transmitted as a claim.
func anchorDigestOf(e *anchorEntry) (string, error) {
	payload, err := canonicalAnchorPayload(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// anchorTransport is the (single-method) delivery channel. Keeping it behind an
// interface is what makes the offline dev witness possible.
type anchorTransport interface {
	deliver(ctx context.Context, req anchorRequest) (status int, body []byte, err error)
}

type httpAnchorTransport struct {
	endpoint string
	client   *http.Client
}

func (t *httpAnchorTransport) deliver(ctx context.Context, req anchorRequest) (int, []byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(hreq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, data, rerr
}

// fileAnchorTransport is the OFFLINE dev witness: every request is appended
// verbatim to <dir>/witness.jsonl and acknowledged with a deterministic ack_id.
// It never touches the network, which is what lets the whole Phase be tested
// hermetically — and it doubles as the source of the witness sequence the
// reconcile surface is fed (ADR-053 §5.3).
type fileAnchorTransport struct {
	dir string
}

const witnessFile = "witness.jsonl"

func (t *fileAnchorTransport) deliver(ctx context.Context, req anchorRequest) (int, []byte, error) {
	if cerr := ctx.Err(); cerr != nil {
		return 0, nil, cerr
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return 0, nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}
	path := filepath.Join(t.dir, witnessFile)
	if err := appendLogLine(path, raw); err != nil {
		return 0, nil, err
	}
	n := 1
	lines, _, rerr := readLogLines(path, func(raw []byte) (int64, bool) {
		var w witnessItem
		if jerr := json.Unmarshal(raw, &w); jerr != nil {
			return 0, false
		}
		return w.AnchorSeq, true
	})
	if rerr == nil {
		for _, l := range lines {
			if l.group == req.AnchorSeq {
				n++
			}
		}
	}
	ack := fmt.Sprintf("%d-%d", req.AnchorSeq, n)
	return 200, []byte(`{"ack_id":"` + ack + `"}`), nil
}

// witnessItem is one record of a witness sequence (the reconcile input shape).
type witnessItem struct {
	PublicationID int64  `json:"publication_id"`
	AnchorSeq     int64  `json:"anchor_seq"`
	AnchorDigest  string `json:"anchor_digest"`
	KeyID         string `json:"key_id"`
	StreamID      string `json:"stream_id"`
	// Sig is stored verbatim (the witness keeps what it received) but is NOT an
	// input to reconciliation: the reconciler compares digest triples, and the
	// signature is verified independently on the local side.
	Sig string `json:"sig,omitempty"`
}

// newAnchorTransport builds the delivery channel. An empty endpoint means
// anchoring is OFF: no transport, no file, no behaviour change (ADR-053 I9).
func newAnchorTransport(endpoint string, timeout time.Duration) (anchorTransport, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, nil
	}
	if dir, isFile := parseFileEndpoint(endpoint); isFile {
		return &fileAnchorTransport{dir: dir}, nil
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("anchor endpoint %q is not http(s):// or file://", endpoint)
	}
	if timeout <= 0 {
		timeout = defaultAnchorTimeout
	}
	return &httpAnchorTransport{endpoint: endpoint, client: &http.Client{Timeout: timeout}}, nil
}

// parseFileEndpoint recognises the offline dev witness form file://<dir>,
// including the Windows drive form file:///C:/path.
func parseFileEndpoint(endpoint string) (string, bool) {
	if !strings.HasPrefix(endpoint, "file://") {
		return "", false
	}
	rest := strings.TrimPrefix(endpoint, "file://")
	if len(rest) >= 3 && rest[0] == '/' && rest[2] == ':' {
		rest = rest[1:]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return filepath.Clean(rest), true
}

// ---------------------------------------------------------------------------
// Delivery result classification (ADR-053 §5.2): a response CODE is never a
// confirmation — only a non-empty ack_id is.
// ---------------------------------------------------------------------------

type anchorDeliveryResult struct {
	State    string // anchored | pending | unanchored | "" (conflict: nothing to record)
	Conflict bool
	AckID    string
	Detail   string
}

func classifyAnchorResponse(status int, body []byte, derr error) anchorDeliveryResult {
	if derr != nil {
		return anchorDeliveryResult{State: anchorStatePending, Detail: "transport: " + derr.Error()}
	}
	var parsed struct {
		AckID  string `json:"ack_id"`
		Reason string `json:"reason"`
	}
	// An unparseable body is NOT an error on its own: only the code and the
	// fields we can read decide the outcome (§5.2).
	_ = json.Unmarshal(body, &parsed)

	switch {
	case status == http.StatusConflict:
		if parsed.Reason == "duplicate" {
			// Same id, same digest: the witness already holds it. Idempotent.
			return anchorDeliveryResult{State: anchorStateAnchored, AckID: parsed.AckID, Detail: "duplicate"}
		}
		if parsed.Reason == "conflict" {
			// Same id, different digest: refuse to record anything (A4/A5).
			return anchorDeliveryResult{Conflict: true, Detail: "witness reported a conflicting digest"}
		}
		return anchorDeliveryResult{State: anchorStateUnanchored, Detail: fmt.Sprintf("witness refused (409, reason=%q)", parsed.Reason)}
	case status >= 200 && status < 300:
		if strings.TrimSpace(parsed.AckID) == "" {
			return anchorDeliveryResult{State: anchorStatePending, Detail: fmt.Sprintf("%d without an ack_id — a response code is not a confirmation", status)}
		}
		return anchorDeliveryResult{State: anchorStateAnchored, AckID: parsed.AckID}
	case status >= 500:
		return anchorDeliveryResult{State: anchorStatePending, Detail: fmt.Sprintf("witness %d", status)}
	default:
		return anchorDeliveryResult{State: anchorStateUnanchored, Detail: fmt.Sprintf("witness %d", status)}
	}
}

// ---------------------------------------------------------------------------
// Scheduler-side wiring (ADR-053 §4)
// ---------------------------------------------------------------------------

const defaultAnchorTimeout = 5 * time.Second

func (s *HistoryExportScheduler) anchorEnabled() bool { return s != nil && s.anchorTransport != nil }

func (s *HistoryExportScheduler) anchorMaxAttempts() int {
	if s.cfg.AnchorMaxAttempts <= 0 {
		return 8
	}
	return s.cfg.AnchorMaxAttempts
}

func (s *HistoryExportScheduler) setAnchorError(msg string) {
	s.mu.Lock()
	s.anchorError = msg
	s.mu.Unlock()
}

// recordAnchorEntry is the 5th and LAST step of a publication: it runs strictly
// after the atomic manifest publish and the ledger append, so an anchor failure
// can never roll back a published fact (ADR-053 I6).
func (s *HistoryExportScheduler) recordAnchorEntry(m *snapshotManifest, at time.Time) error {
	if !s.anchorEnabled() || m == nil {
		return nil
	}
	if s.signer == nil {
		return errors.New("anchor: no signing key configured")
	}
	// Same guard as the ledger: a manifest whose signature does not verify must
	// never be washed into anchor evidence.
	if v := verifyManifestSignature(m, s.trust); v.Verdict != sigVerdictOK {
		return fmt.Errorf("anchor: refusing to record publication %d (%s)", m.PublicationID, v.Verdict)
	}
	seq, err := nextAnchorSeq(s.cfg.Dir)
	if err != nil {
		return err
	}
	e, err := entryForAnchor(m, seq, at)
	if err != nil {
		return err
	}
	e.State = anchorStatePending

	// Capacity gate (ADR-053 §4.4 / T128). The log is bounded by whole-group
	// prefix compaction, but an UNCONFIRMED group is never dropped to make room:
	// A5 forbids re-dispatch, so evicting it would silently erase a delivery
	// obligation. In that state the new entry is recorded `unanchored` (a loud,
	// terminal state) instead of evicting the oldest one.
	if s.cfg.AnchorCapacity > 0 {
		st, lerr := loadAnchorState(s.cfg.Dir, s.trust)
		if lerr != nil {
			return lerr
		}
		if anchorBacklogBlocks(st, s.cfg.AnchorCapacity) {
			e.State = anchorStateUnanchored
			e.LastError = fmt.Sprintf("anchor capacity %d reached while seq %d is still %s — refusing to evict an unconfirmed group",
				s.cfg.AnchorCapacity, st.window.MinSeq, st.latest[st.window.MinSeq].State)
			if serr := s.signer.signAnchorEntry(&e, s.cfg.Dir, at); serr != nil {
				return serr
			}
			if aerr := appendAnchorEntry(s.cfg.Dir, s.cfg.AnchorCapacity, e); aerr != nil {
				return aerr
			}
			return errors.New("anchor: " + e.LastError)
		}
	}

	if serr := s.signer.signAnchorEntry(&e, s.cfg.Dir, at); serr != nil {
		return serr
	}
	// [R40-6 / I10] The pending line is durable BEFORE we talk to the witness.
	// A crash in the other order would leave a confirmed witness whose local
	// record never existed, which the reconciler would have to read as
	// `witness_ahead_of_window` — i.e. as if something had been deleted.
	if aerr := appendAnchorEntry(s.cfg.Dir, s.cfg.AnchorCapacity, e); aerr != nil {
		return aerr
	}
	return s.dispatchAnchor(context.Background(), e)
}

// anchorBacklogBlocks reports whether capacity is exhausted AND the oldest
// surviving group is still unconfirmed.
func anchorBacklogBlocks(st *anchorState, capacity int) bool {
	if st == nil || capacity <= 0 || st.window.Entries < capacity {
		return false
	}
	oldest, ok := st.latest[st.window.MinSeq]
	return ok && oldest.State != anchorStateAnchored
}

// dispatchAnchor performs ONE delivery attempt and records its outcome as a new
// state line for the same seq (a state ADVANCE, never a rewrite — ADR-053 §1.2).
// It never rolls back anything that already happened.
func (s *HistoryExportScheduler) dispatchAnchor(ctx context.Context, e anchorEntry) error {
	return s.dispatchAnchorPath(ctx, anchorLogPath(s.cfg.Dir), e)
}

func (s *HistoryExportScheduler) dispatchAnchorPath(ctx context.Context, path string, e anchorEntry) error {
	dg, derr := anchorDigestOf(&e)
	if derr != nil {
		return derr
	}
	req := anchorRequest{
		V:                  1,
		KeyID:              e.KeyID,
		StreamID:           e.StreamID,
		PublicationID:      e.PublicationID,
		AnchorSeq:          e.AnchorSeq,
		AnchorDigest:       dg,
		ManifestDigest:     e.ManifestDigest,
		PrevPublicationID:  e.PrevPublicationID,
		PrevManifestDigest: e.PrevManifestDigest,
		RecordedAt:         e.RecordedAt,
		Sig:                e.Sig,
	}
	// TEST-ONLY hook (R40-6): fires at the moment of dispatch, so a test can
	// assert the pending line is already durable on disk.
	if s.beforeAnchorDispatch != nil {
		s.beforeAnchorDispatch(s.cfg.Dir, e.AnchorSeq)
	}
	status, body, terr := s.anchorTransport.deliver(ctx, req)
	res := classifyAnchorResponse(status, body, terr)

	if res.Conflict {
		// A4/A5: record NOTHING. The entry stays `pending` and the problem is
		// surfaced — a conflict is never resolved by overwriting.
		return fmt.Errorf("anchor: witness reported a conflict for publication %d (seq %d)", e.PublicationID, e.AnchorSeq)
	}

	next := e
	next.Attempts = e.Attempts + 1
	switch res.State {
	case anchorStateAnchored:
		next.State = anchorStateAnchored
		next.AckID = res.AckID
		next.AnchoredAt = s.clock().UTC().Format(time.RFC3339Nano)
	case anchorStateUnanchored:
		next.State = anchorStateUnanchored
		next.LastError = res.Detail
	default: // pending — retryable
		next.State = anchorStatePending
		next.LastError = res.Detail
		if next.Attempts >= s.anchorMaxAttempts() {
			next.State = anchorStateUnanchored
			next.LastError = fmt.Sprintf("attempts exhausted (%d): %s", next.Attempts, res.Detail)
		}
	}
	// The payload is unchanged, so the original signature still covers it: a
	// state advance needs no re-signing (ADR-053 §1.3).
	if aerr := appendAnchorEntryPath(path, s.cfg.AnchorCapacity, next); aerr != nil {
		return aerr
	}
	if next.State != anchorStateAnchored {
		return fmt.Errorf("anchor: seq %d is %s (attempt %d): %s", next.AnchorSeq, next.State, next.Attempts, next.LastError)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Phase 41 — the second anchor family (ADR-055 §9)
//
// A lifecycle event is anchored through the SAME dispatch and the SAME ack
// semantics as a publication (2xx + non-empty ack_id = anchored, 8 attempts ⇒
// terminal `unanchored`), but into its OWN file and its own sequence space: the
// two families must never share a group key, or the shared classifier would
// read a publication seq and a lifecycle seq as one group.
//
// Two prohibitions are inherited verbatim: the anchor result is never used to
// decide whether a key is valid, and the lifecycle ledger is never used to fill
// an anchor hole.
// ---------------------------------------------------------------------------

func keyLifecycleAnchorPath(dir string) string { return filepath.Join(dir, keyLifecycleAnchorFile) }

// anchorKeyLifecycleEvent records and dispatches the anchor of one lifecycle
// event. The pending line is durable BEFORE the witness is contacted (R40-6).
func (s *HistoryExportScheduler) anchorKeyLifecycleEvent(e keyLifecycleEntry) error {
	if !s.anchorEnabled() || s.signer == nil {
		return nil
	}
	path := keyLifecycleAnchorPath(s.cfg.Dir)
	seq, err := nextAnchorSeqPath(path)
	if err != nil {
		return err
	}
	ae := anchorEntry{
		AnchorSeq:   seq,
		Kind:        anchorKindKeyLifecycle,
		EventSeq:    e.EventSeq,
		EventDigest: e.EventDigest,
		EventType:   e.EventType,
		NotAfter:    e.NotAfter,
		RecordedAt:  s.clock().UTC().Format(time.RFC3339Nano),
		State:       anchorStatePending,
	}
	if serr := s.signer.signAnchorEntry(&ae, s.cfg.Dir, s.clock()); serr != nil {
		return serr
	}
	if aerr := appendAnchorEntryPath(path, s.cfg.AnchorCapacity, ae); aerr != nil {
		return aerr
	}
	return s.dispatchAnchorPath(context.Background(), path, ae)
}

// anchorHousekeeping re-delivers entries that are still `pending` and have
// attempts left. It runs BEFORE publishing, so a freshly created entry is never
// dispatched twice in the same tick. `unanchored` is terminal: ADR-052 §6-12
// freezes re-dispatch out of existence, so nothing here ever revives it.
func (s *HistoryExportScheduler) anchorHousekeeping(ctx context.Context) {
	if !s.anchorEnabled() {
		return
	}
	st, err := loadAnchorState(s.cfg.Dir, s.trust)
	if err != nil {
		s.setAnchorError(err.Error())
		return
	}
	for _, seq := range st.seqs() {
		e := st.latest[seq]
		if e.State != anchorStatePending {
			continue
		}
		if e.Attempts >= s.anchorMaxAttempts() {
			final := e
			final.State = anchorStateUnanchored
			final.LastError = fmt.Sprintf("attempts exhausted (%d)", e.Attempts)
			if aerr := appendAnchorEntry(s.cfg.Dir, s.cfg.AnchorCapacity, final); aerr != nil {
				s.setAnchorError(aerr.Error())
			}
			continue
		}
		if derr := s.dispatchAnchor(ctx, e); derr != nil {
			s.setAnchorError(derr.Error())
		}
	}
}

// refreshAnchorStatus recomputes the cached roll-up the read surface exposes.
func (s *HistoryExportScheduler) refreshAnchorStatus() {
	if !s.anchorEnabled() {
		return
	}
	st, err := loadAnchorState(s.cfg.Dir, s.trust)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.anchorError = err.Error()
		return
	}
	a, p, u := summarizeAnchor(st)
	s.anchoredCount, s.pendingCount, s.unanchoredCount = a, p, u
	w := st.window
	s.anchorWindow = &w
}

// ---------------------------------------------------------------------------
// Reconciliation (ADR-053 §7 / ADR-052 A7-A10)
//
// This is the payoff of the whole Phase: an actor who holds the witness
// sequence and NOT the host can now see that a publication existed locally and
// is gone, because the witness still carries it and the local window says it
// should be here.
//
// The five assertable sources of `anchor_broken` are exactly:
// orphan_witness (inside the window), witness_ahead_of_window, digest_mismatch,
// divergent, window_discontinuous. `missing_witness` and
// `witness_outside_window` are NEVER broken — both are indistinguishable from a
// legal compaction / a not-yet-delivered record, and asserting on the
// indistinguishable is how evidence honesty dies (ADR-052 §7.8).
//
// ZERO SIDE EFFECTS (ADR-052 §6-13 / I7): this function only READS the local
// log. It writes nothing, audits nothing, and never opens a network connection.
// ---------------------------------------------------------------------------

const (
	anchorVerdictAbsent       = "anchor_absent"
	anchorVerdictOK           = "anchor_ok"
	anchorVerdictIncomplete   = "anchor_incomplete"
	anchorVerdictBroken       = "anchor_broken"
	anchorVerdictUnverifiable = "anchor_unverifiable"

	anchorTrustNotProvided = "not_provided"
	anchorTrustUntrusted   = "untrusted"
	anchorTrustDivergent   = "divergent"
	anchorTrustAligned     = "aligned"

	trustReasonForeignKey  = "foreign_key_id"
	trustReasonForeignStrm = "foreign_stream"
	trustReasonNoOverlap   = "no_overlap"
	trustReasonMalformed   = "malformed_sequence"
	trustReasonNoWindow    = "no_local_window"
)

// anchorItemResult is the per-publication row of the reconcile output.
type anchorItemResult struct {
	PublicationID int64  `json:"publication_id"`
	AnchorState   string `json:"anchor_state"`
	AnchorSeq     int64  `json:"anchor_seq"`
	AnchorDigest  string `json:"anchor_digest,omitempty"`
	Witness       string `json:"witness"`
}

const (
	witnessStateWitnessed      = "witnessed"
	witnessStateDigestMismatch = "digest_mismatch"
	witnessStateMissing        = "missing_witness"
	witnessStateOrphan         = "orphan_witness"
	witnessStateOutsideWindow  = "witness_outside_window"
	witnessStateAheadOfWindow  = "witness_ahead_of_window"
	witnessStateUnconfirmed    = "witnessed_unconfirmed"
	witnessStateNotEvaluated   = "not_evaluated"
)

// anchorReconcileResult is the aggregate row (ADR-052 §5).
type anchorReconcileResult struct {
	Enabled           bool               `json:"enabled"`
	WitnessTrust      string             `json:"witness_trust"`
	TrustReason       string             `json:"trust_reason,omitempty"`
	Verdict           string             `json:"verdict"`
	Window            *anchorWindow      `json:"anchor_window,omitempty"`
	OrphanWitnessIDs  []int64            `json:"orphan_witness_ids"`
	OutsideWindowIDs  []int64            `json:"outside_window_ids"`
	AheadOfWindowIDs  []int64            `json:"ahead_of_window_ids"`
	DivergentIDs      []int64            `json:"divergent_ids"`
	MissingWitnessIDs []int64            `json:"missing_witness_ids"`
	MismatchIDs       []int64            `json:"digest_mismatch_ids"`
	PendingIDs        []int64            `json:"pending_ids"`
	UnanchoredIDs     []int64            `json:"unanchored_ids"`
	UnusableIDs       []int64            `json:"unusable_ids,omitempty"`
	LocalEntries      int                `json:"local_anchor_entries"`
	AnchorError       string             `json:"anchor_error,omitempty"`
	Items             []anchorItemResult `json:"items"`
}

// ReconcileAnchor compares the local anchor log against a witness sequence.
func (s *HistoryExportScheduler) ReconcileAnchor(seq []witnessItem) (anchorReconcileResult, error) {
	res := anchorReconcileResult{
		Verdict:           anchorVerdictAbsent,
		WitnessTrust:      anchorTrustNotProvided,
		OrphanWitnessIDs:  []int64{},
		OutsideWindowIDs:  []int64{},
		AheadOfWindowIDs:  []int64{},
		DivergentIDs:      []int64{},
		MissingWitnessIDs: []int64{},
		MismatchIDs:       []int64{},
		PendingIDs:        []int64{},
		UnanchoredIDs:     []int64{},
		Items:             []anchorItemResult{},
	}
	if s == nil || !s.anchorEnabled() {
		return res, nil
	}
	res.Enabled = true

	// A disabled trust anchor means local records cannot be verified at all:
	// there is no window to reason about, and nothing may be asserted.
	st, err := loadAnchorState(s.cfg.Dir, s.trust)
	if err != nil {
		res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonNoWindow
		res.Verdict = anchorVerdictUnverifiable
		res.AnchorError = err.Error()
		return res, nil
	}
	w := st.window
	res.Window = &w
	res.LocalEntries = st.window.Entries
	for _, p := range st.unusable {
		res.UnusableIDs = append(res.UnusableIDs, p.PublicationID)
	}

	// Local roll-up is reported even without a witness: it is local state, not
	// a conclusion about the witness.
	// st.byID holds only entries whose signature verifies, so nothing that
	// cannot be trusted takes part in any conclusion.
	localByID := map[int64]anchorEntry{}
	for id, e := range st.byID {
		localByID[id] = e
		switch e.State {
		case anchorStatePending:
			res.PendingIDs = append(res.PendingIDs, id)
		case anchorStateUnanchored:
			res.UnanchoredIDs = append(res.UnanchoredIDs, id)
		}
	}
	sortInt64s(res.PendingIDs)
	sortInt64s(res.UnanchoredIDs)
	sortInt64s(res.UnusableIDs)

	if len(seq) == 0 {
		// T133: no witness sequence ⇒ no reconciliation conclusion at all.
		res.WitnessTrust = anchorTrustNotProvided
		if st.window.Entries == 0 {
			res.Verdict = anchorVerdictAbsent
		} else {
			res.Verdict = anchorVerdictIncomplete // local records exist but nothing proved them
		}
		res.Items = localItemsOnly(localByID)
		return res, nil
	}

	// Malformed input is a refusal to conclude, never a broken verdict.
	for _, w := range seq {
		if w.PublicationID <= 0 || w.AnchorSeq <= 0 || strings.TrimSpace(w.AnchorDigest) == "" {
			res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonMalformed
			res.Verdict = anchorVerdictUnverifiable
			return res, nil
		}
	}

	identity, ierr := deriveAnchorIdentity(s.signer, s.trust, s.cfg.Dir)
	if ierr != nil {
		res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonNoWindow
		res.Verdict = anchorVerdictUnverifiable
		res.AnchorError = ierr.Error()
		return res, nil
	}
	for _, w := range seq {
		if !identity.knowsKey(w.KeyID) {
			res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonForeignKey
			res.Verdict = anchorVerdictUnverifiable
			return res, nil
		}
		if !identity.accepts(w.KeyID, w.StreamID) {
			res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonForeignStrm
			res.Verdict = anchorVerdictUnverifiable
			return res, nil
		}
	}

	witnessByID := map[int64]witnessItem{}
	for _, w := range seq {
		witnessByID[w.PublicationID] = w
	}

	// Alignment lower bound: at least one shared id must agree on BOTH the seq
	// and the digest. Zero agreement over a non-empty overlap is `divergent`
	// (R40-2) — an assertable disagreement, not a shrug.
	shared := []int64{}
	for id, e := range localByID {
		if e.State != anchorStateAnchored {
			continue
		}
		if _, ok := witnessByID[id]; ok {
			shared = append(shared, id)
		}
	}
	sortInt64s(shared)
	if len(shared) == 0 {
		res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonNoOverlap
		res.Verdict = anchorVerdictUnverifiable
		return res, nil
	}
	alignedAny := false
	for _, id := range shared {
		e := localByID[id]
		dg, derr := anchorDigestOf(&e)
		if derr != nil {
			res.WitnessTrust, res.TrustReason = anchorTrustUntrusted, trustReasonNoWindow
			res.Verdict = anchorVerdictUnverifiable
			res.AnchorError = derr.Error()
			return res, nil
		}
		w := witnessByID[id]
		if w.AnchorSeq == e.AnchorSeq && w.AnchorDigest == dg {
			alignedAny = true
			break
		}
	}
	if !alignedAny {
		res.WitnessTrust = anchorTrustDivergent
		res.DivergentIDs = shared
		res.Verdict = anchorVerdictBroken
		res.Items = walkAnchorUnion(localByID, witnessByID, st, &res)
		return res, nil
	}
	res.WitnessTrust = anchorTrustAligned
	res.Items = walkAnchorUnion(localByID, witnessByID, st, &res)

	switch {
	case len(res.MismatchIDs) > 0 || len(res.OrphanWitnessIDs) > 0 || len(res.AheadOfWindowIDs) > 0 || !st.window.Continuous:
		res.Verdict = anchorVerdictBroken
	case len(res.PendingIDs) > 0 || len(res.UnanchoredIDs) > 0 || len(res.MissingWitnessIDs) > 0 || len(res.OutsideWindowIDs) > 0:
		res.Verdict = anchorVerdictIncomplete
	default:
		res.Verdict = anchorVerdictOK
	}
	return res, nil
}

// walkAnchorUnion classifies every id in (local ∪ witness).
func walkAnchorUnion(local map[int64]anchorEntry, witness map[int64]witnessItem, st *anchorState, res *anchorReconcileResult) []anchorItemResult {
	ids := map[int64]bool{}
	for id := range local {
		ids[id] = true
	}
	for id := range witness {
		ids[id] = true
	}
	union := make([]int64, 0, len(ids))
	for id := range ids {
		union = append(union, id)
	}
	sortInt64s(union)

	out := make([]anchorItemResult, 0, len(union))
	for _, id := range union {
		loc, hasLoc := local[id]
		wit, hasWit := witness[id]
		state := witnessStateNotEvaluated
		switch {
		case hasLoc && loc.State == anchorStateAnchored && hasWit:
			dg, derr := anchorDigestOf(&loc)
			if derr != nil {
				state = witnessStateNotEvaluated
				break
			}
			if wit.AnchorDigest == dg && wit.AnchorSeq == loc.AnchorSeq {
				state = witnessStateWitnessed
			} else {
				state = witnessStateDigestMismatch
				res.MismatchIDs = append(res.MismatchIDs, id)
			}
		case hasLoc && loc.State == anchorStateAnchored && !hasWit:
			state = witnessStateMissing
			res.MissingWitnessIDs = append(res.MissingWitnessIDs, id)
		case !hasLoc && hasWit && wit.AnchorSeq >= st.window.MinSeq && wit.AnchorSeq <= st.window.MaxSeq:
			// The core payoff: the witness remembers something the local window
			// says should still be here.
			state = witnessStateOrphan
			res.OrphanWitnessIDs = append(res.OrphanWitnessIDs, id)
		case !hasLoc && hasWit && wit.AnchorSeq < st.window.MinSeq:
			// A legal prefix compaction moved the lower bound past it: NOT
			// assertable (R40-1).
			state = witnessStateOutsideWindow
			res.OutsideWindowIDs = append(res.OutsideWindowIDs, id)
		case !hasLoc && hasWit:
			// Prefix compaction only ever removes the OLDEST groups, so a seq
			// beyond the upper bound cannot be produced legally.
			state = witnessStateAheadOfWindow
			res.AheadOfWindowIDs = append(res.AheadOfWindowIDs, id)
		case hasLoc && hasWit:
			// Locally pending/unanchored: the witness has it, but we never
			// confirmed it — it takes part in no assertion (T139).
			state = witnessStateUnconfirmed
		}
		digest := ""
		if hasLoc {
			if dg, derr := anchorDigestOf(&loc); derr == nil {
				digest = dg
			}
		}
		out = append(out, anchorItemResult{
			PublicationID: id,
			AnchorState:   loc.State,
			AnchorSeq:     loc.AnchorSeq,
			AnchorDigest:  digest,
			Witness:       state,
		})
	}
	sortInt64s(res.MismatchIDs)
	sortInt64s(res.MissingWitnessIDs)
	sortInt64s(res.OrphanWitnessIDs)
	sortInt64s(res.OutsideWindowIDs)
	sortInt64s(res.AheadOfWindowIDs)
	return out
}

// localItemsOnly renders the local roll-up when no witness sequence was given.
func localItemsOnly(local map[int64]anchorEntry) []anchorItemResult {
	ids := make([]int64, 0, len(local))
	for id := range local {
		ids = append(ids, id)
	}
	sortInt64s(ids)
	out := make([]anchorItemResult, 0, len(ids))
	for _, id := range ids {
		e := local[id]
		dg, _ := anchorDigestOf(&e)
		out = append(out, anchorItemResult{
			PublicationID: id, AnchorState: e.State, AnchorSeq: e.AnchorSeq,
			AnchorDigest: dg, Witness: witnessStateNotEvaluated,
		})
	}
	return out
}

func (id *anchorIdentity) knowsKey(keyID string) bool {
	if id == nil {
		return false
	}
	if id.KeyID != "" {
		return keyID == id.KeyID
	}
	for pair := range id.accepted {
		if strings.HasPrefix(pair, keyID+"|") {
			return true
		}
	}
	return false
}

func sortInt64s(v []int64) { sort.Slice(v, func(i, j int) bool { return v[i] < v[j] }) }

// anchorLogPath exposes the anchor log's location to the read surface and tests.
func anchorLogPath(dir string) string { return filepath.Join(dir, chainAnchorFile) }

// anchorLogAbsent reports whether anchoring has never produced a file.
func anchorLogAbsent(dir string) bool {
	_, err := os.Stat(anchorLogPath(dir))
	return os.IsNotExist(err)
}

// entryForAnchor builds the (unsigned) anchor statement for a published
// manifest. It reuses the Phase 39 digest helper, so there is still exactly one
// canonical form behind every commitment.
func entryForAnchor(m *snapshotManifest, seq int64, at time.Time) (anchorEntry, error) {
	dg, err := ledgerDigestOf(m)
	if err != nil {
		return anchorEntry{}, err
	}
	e := anchorEntry{
		PublicationID:     m.PublicationID,
		AnchorSeq:         seq,
		ManifestDigest:    dg,
		PrevPublicationID: 0,
		RecordedAt:        at.UTC().Format(time.RFC3339Nano),
	}
	if m.Chain != nil {
		e.PrevPublicationID = m.Chain.PrevPublicationID
		e.PrevManifestDigest = m.Chain.PrevManifestDigest
	}
	return e, nil
}

// anchorStatusSummary is the scheduler-side roll-up surfaced on the read-only
// face (ADR-053 §8).
type anchorStatusSummary struct {
	Enabled       bool   `json:"anchor_enabled,omitempty"`
	EndpointSet   bool   `json:"anchor_endpoint_set,omitempty"`
	AnchoredCount int    `json:"anchored_count,omitempty"`
	PendingCount  int    `json:"pending_count,omitempty"`
	UnanchoredCnt int    `json:"unanchored_count,omitempty"`
	AnchorError   string `json:"anchor_error,omitempty"`
}

func summarizeAnchor(st *anchorState) (anchored, pending, unanchored int) {
	if st == nil {
		return 0, 0, 0
	}
	for _, e := range st.latest {
		switch e.State {
		case anchorStateAnchored:
			anchored++
		case anchorStatePending:
			pending++
		case anchorStateUnanchored:
			unanchored++
		}
	}
	return anchored, pending, unanchored
}

// describeAnchorStates is a diagnostic helper: it renders the local state
// distribution, used by the read surface and by tests.
func describeAnchorStates(st *anchorState) string {
	if st == nil {
		return "absent"
	}
	a, p, u := summarizeAnchor(st)
	return fmt.Sprintf("anchored=%d pending=%d unanchored=%d window=[%d,%d] entries=%d continuous=%t",
		a, p, u, st.window.MinSeq, st.window.MaxSeq, st.window.Entries, st.window.Continuous)
}

// validateAnchorState is the invariant self-check used by tests (I4/I5): a legal
// log never contains a gap, and never contains a duplicated seq with a
// contradictory payload.
func validateAnchorState(st *anchorState) []string {
	if st == nil {
		return nil
	}
	var out []string
	if !st.window.Continuous && st.window.Entries > 0 {
		out = append(out, fmt.Sprintf("anchor_seq gap inside [%d,%d]: a legal log is contiguous (I4)", st.window.MinSeq, st.window.MaxSeq))
	}
	for _, seq := range st.conflicts {
		out = append(out, fmt.Sprintf("anchor_seq %d carries contradictory payloads (I5)", seq))
	}
	for _, p := range st.unusable {
		if strings.TrimSpace(p.Verdict) != "" {
			out = append(out, fmt.Sprintf("anchor_seq %d is unusable (%s)", p.AnchorSeq, p.Verdict))
		}
	}
	return out
}
