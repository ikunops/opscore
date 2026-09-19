package server

// Phase 43 — Evidence Destruction Accountability (ADR-060 scope, ADR-061
// architecture).
//
// What Phases 30~42 cannot say: five code paths in this system DESTROY evidence
// (retention prune plus four prefix compactions) and not one of them writes down
// what it removed. A legal destruction and an attacker's deletion therefore
// produce the same byte state — the file is not there — and every earlier Phase
// was forced to say "indeterminate" about it (P39 eviction vs deletion, P40
// R40-1 window, P36 publication_id holes, P42 gap_indeterminate).
//
// This Phase turns that collective silence into ONE assertable statement:
//
//	unaccounted_disappearance — evidence is known to have existed, is gone now,
//	                            and no authorized destruction record accounts
//	                            for it.
//
// Division of labour across Phases, stated once:
//
//	P37 decides WHO, P40 decides WHERE, P41 decides WHEN, P42 decides WHETHER,
//	P43 decides WHY ABSENT.
//
// Frozen boundaries (ADR-061 §12, honoured here):
//   - P35~P42 read surfaces and verdicts are READ, never modified; the
//     destruction status never participates in any of their merges (I6);
//   - `appendonly_log.go` is reused and only EXTENDED (an observed variant);
//   - the signature is the Phase 37 block, signed by the Phase 41 key authority
//     (KAK) — never by the export signing key (R43-1 / G3);
//   - with --export-destruction-log off nothing is written, no file is created
//     and every earlier response stays byte-identical.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// destructionLogFile lives in the export directory, next to the manifest, the
// chain ledger, the anchor logs, the lifecycle log and the verification log
// (R41-3: same domain, never fanned out).
const destructionLogFile = "destruction-log.jsonl"

// destructionSubjectLimit bounds how many publications the reconciliation scans.
const destructionSubjectLimit = 100

// The six destruction kinds — one per destroyed surface, plus the
// log-accounting-for-itself case (I5). Phase 45 adds the seventh: the input
// integrity acceptance ledger's own prefix compaction (ADR-066 §3 — it is
// evidence too, so trimming it is a destruction that must be accounted for).
const (
	destructionKindSnapshotRetention      = "snapshot_retention"
	destructionKindLedgerCompaction       = "ledger_compaction"
	destructionKindAnchorCompaction       = "anchor_compaction"
	destructionKindKeyLifecycleCompaction = "key_lifecycle_compaction"
	destructionKindVerificationCompaction = "verification_compaction"
	destructionKindSelfCompaction         = "self_compaction"
	destructionKindAcceptanceCompaction   = "acceptance_compaction"
)

// The state of one destruction group. `state` is the ONLY field in the STATE
// region of an entry: it is never signed and never part of the payload (I7).
const (
	destructionStateIntended  = "intended"
	destructionStateCompleted = "completed"
	destructionStateAborted   = "aborted"
)

// The verdicts this Phase adds. `unaccounted_disappearance` is the payoff; the
// rest exist so that every "we cannot say" is said out loud instead of being
// folded into "accounted".
const (
	destructionVerdictAccounted           = "accounted"
	destructionVerdictUnaccounted         = "unaccounted_disappearance"
	destructionVerdictUnconfirmed         = "destruction_unconfirmed"
	destructionVerdictUnauthorized        = "destruction_unauthorized"
	destructionVerdictConflict            = "destruction_conflict"
	destructionVerdictChainBroken         = "destruction_chain_broken"
	destructionVerdictWindowDiscontinuous = "destruction_window_discontinuous"
	destructionVerdictAbsent              = "destruction_absent"
)

// ---------------------------------------------------------------------------
// The entry and the two regions it is split into (R43-2)
// ---------------------------------------------------------------------------

// destructionTarget is one destroyed object, in one of two shapes:
//
//	publication face: {publication_id, manifest_digest}
//	log face:         {from_seq, to_seq, prefix_digest}
//
// An EMPTY digest is never a wildcard. It means "the digest was not available
// when the destruction was recorded", and matching then degrades to an exact id
// match — weaker, and reported as such (ADR-061 §4.2).
type destructionTarget struct {
	PublicationID  int64  `json:"publication_id,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	FromSeq        int64  `json:"from_seq,omitempty"`
	ToSeq          int64  `json:"to_seq,omitempty"`
	PrefixDigest   string `json:"prefix_digest,omitempty"`
}

// destructionEntry is one line of the destruction log.
//
// It is a COMPOSITE: an immutable signed payload (what was destroyed) plus ONE
// legal state advance (intended -> completed|aborted). The split is what makes
// both halves work at once (ADR-061 §2):
//
//   - `state` is in the STATE region and is NEVER signed, so the advance line
//     reuses the intended line's signature bytes VERBATIM and needs no KAK
//     online (I8 — a KAK that had to be online would freeze the log the first
//     time it was unreachable);
//   - because `state` is not in the payload, the CHAIN value must not be the
//     payload digest either, or "the completed line was deleted" and "it was
//     never completed" would be indistinguishable on the chain. Hence two
//     digests (I9).
type destructionEntry struct {
	// ---- payload: immutable, signed by the KAK ----
	V                       int                 `json:"v"` // 1
	DestructionSeq          int64               `json:"destruction_seq"`
	Kind                    string              `json:"kind"`
	Targets                 []destructionTarget `json:"targets"`
	DestroyedCount          int                 `json:"destroyed_count"`
	Policy                  string              `json:"policy"` // declarative only, never an authorization (Q2)
	RecordedAt              string              `json:"recorded_at"`
	AuthorityKeyID          string              `json:"authority_key_id"`
	StreamID                string              `json:"stream_id"`
	PrevDigest              string              `json:"prev_digest,omitempty"`
	TargetDigestUnavailable []string            `json:"target_digest_unavailable,omitempty"`

	// ---- derived ----
	EntryDigest string          `json:"entry_digest"`        // sha256(canonical payload) — the two lines of a group share it
	Signature   *signatureBlock `json:"signature,omitempty"` // KAK signature over the payload

	// ---- STATE region: mutable, never signed ----
	State string `json:"state"`
}

// destructionSigned is the exact structure canonicalDestructionPayload
// serializes: the payload region, in a fixed field order.
type destructionSigned struct {
	V                       int                 `json:"v"`
	DestructionSeq          int64               `json:"destruction_seq"`
	Kind                    string              `json:"kind"`
	Targets                 []destructionTarget `json:"targets"`
	DestroyedCount          int                 `json:"destroyed_count"`
	Policy                  string              `json:"policy"`
	RecordedAt              string              `json:"recorded_at"`
	AuthorityKeyID          string              `json:"authority_key_id"`
	StreamID                string              `json:"stream_id"`
	PrevDigest              string              `json:"prev_digest,omitempty"`
	TargetDigestUnavailable []string            `json:"target_digest_unavailable,omitempty"`
}

func destructionSignedFields(e *destructionEntry) destructionSigned {
	return destructionSigned{
		V:                       e.V,
		DestructionSeq:          e.DestructionSeq,
		Kind:                    e.Kind,
		Targets:                 e.Targets,
		DestroyedCount:          e.DestroyedCount,
		Policy:                  e.Policy,
		RecordedAt:              e.RecordedAt,
		AuthorityKeyID:          e.AuthorityKeyID,
		StreamID:                e.StreamID,
		PrevDigest:              e.PrevDigest,
		TargetDigestUnavailable: e.TargetDigestUnavailable,
	}
}

// destructionPayloadEqual is the I1' discriminator: two lines of one group make
// the same STATEMENT iff their payloads are equal. Same seq + same payload +
// legal advance is the one legal second line; anything else is a conflict.
func destructionPayloadEqual(a, b destructionEntry) bool {
	pa, ea := canonicalDestructionPayload(&a)
	pb, eb := canonicalDestructionPayload(&b)
	if ea != nil || eb != nil {
		return false
	}
	return string(pa) == string(pb)
}

// canonicalDestructionPayload returns the exact bytes the KAK signature covers.
// Same canonical serializer family as P37/P40/P41/P42 — a second one would drift.
func canonicalDestructionPayload(e *destructionEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil destruction entry")
	}
	return json.Marshal(destructionSignedFields(e))
}

// destructionEntryDigest is the content commitment: sha256(canonical payload).
// The intended and the completed line of one group share it (that is the point).
func destructionEntryDigest(e *destructionEntry) (string, error) {
	payload, err := canonicalDestructionPayload(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// destructionLineDigest is the CHAIN value (I9):
//
//	line_digest = sha256(prev_digest + ":" + entry_digest + ":" + state)
//
// It is computed on the fly and never stored. Because it carries `state`, the
// two lines of one group are distinguishable on the chain, so deleting the
// `completed` line is detectable and a repeated append is detectable — neither
// of which the content commitment alone can do.
func destructionLineDigest(e *destructionEntry) (string, error) {
	if e == nil {
		return "", errors.New("nil destruction entry")
	}
	sum := sha256.Sum256([]byte(e.PrevDigest + ":" + e.EntryDigest + ":" + e.State))
	return hex.EncodeToString(sum[:]), nil
}

func serializeDestructionEntryBytes(e *destructionEntry) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func destructionLogPath(dir string) string { return filepath.Join(dir, destructionLogFile) }

// destructionGroupOf is the classifier this log injects into the shared
// append-only primitive: one `destruction_seq` is ONE group.
func destructionGroupOf(raw []byte) (int64, bool) {
	var e destructionEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return e.DestructionSeq, true
}

// destructionLedgerAbsent reports whether the Phase has never produced a file.
// "No file" is the ONE thing that must never be dressed up as "nothing was ever
// destroyed" (T216 — deleting the log is escape B).
func destructionLedgerAbsent(dir string) bool {
	_, err := os.Stat(destructionLogPath(dir))
	return os.IsNotExist(err)
}

// ---------------------------------------------------------------------------
// KAK signing / verification (R43-1 — never the export signing key)
// ---------------------------------------------------------------------------

func (s *exportSigner) signDestructionEntry(e *destructionEntry, at time.Time) error {
	if s == nil {
		return errors.New("destruction: no key-authority (KAK) private key configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("destruction: private key is not a valid Ed25519 key")
	}
	if e.Signature == nil {
		e.Signature = &signatureBlock{}
	}
	e.Signature.Alg = signatureAlgEd25519
	e.Signature.KeyID = s.keyID
	e.Signature.SignedAt = at.UTC().Format(time.RFC3339Nano)
	e.Signature.Sig = ""
	payload, err := canonicalDestructionPayload(e)
	if err != nil {
		return err
	}
	e.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyDestructionEntrySignature applies the frozen Phase 37 decision order to
// a destruction entry, against the KAK trust anchor.
//
// Anything other than `signature_ok` means the entry is `destruction_unauthorized`
// and is therefore NOT accounted for (R43-1): whoever holds the export signing
// key — the adversary model P40 was built around — cannot manufacture an
// `accounted` verdict for evidence they deleted.
func verifyDestructionEntrySignature(e *destructionEntry, trust *exportTrustStore) SignatureVerdict {
	if e == nil || e.Signature == nil {
		id := ""
		if e != nil {
			id = e.AuthorityKeyID
		}
		return SignatureVerdict{Verdict: sigVerdictAbsent, KeyID: id, Detail: "destruction record carries no signature"}
	}
	sb := e.Signature
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "destruction signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "destruction signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted key-authority keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "destruction authority_key_id is not in the trusted KAK set"}
	}
	if e.AuthorityKeyID != "" && e.AuthorityKeyID != sb.KeyID {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "destruction authority_key_id disagrees with the signing key id"}
	}
	payload, perr := canonicalDestructionPayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "destruction signature does not verify against the trusted KAK"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// ---------------------------------------------------------------------------
// Configuration bundle
// ---------------------------------------------------------------------------

type destructionConfig struct {
	dir      string
	capacity int
	ka       *keyAuthority
	streamID string
	// on is the deployment switch (--export-destruction-log). Without it the
	// Phase is inert even when a KAK happens to be configured, so a default
	// deployment stays byte-identical to Phase 42 (ADR-061 §1.5).
	on bool
	// anchorDispatch, when non-nil, is invoked once a group reaches a terminal
	// state (I6: the anchor is an EXIT — a dispatch failure is logged by the
	// hook itself and never blocks or rolls back the accounting).
	anchorDispatch func(destructionEntry) error
}

func (c destructionConfig) enabled() bool  { return c.on && c.ka != nil && c.ka.verifiable() }
func (c destructionConfig) writable() bool { return c.on && c.ka != nil && c.ka.writable() }

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// destructionWindow is the surviving contiguous run of destruction_seq values —
// the R40-1 discipline instantiated for this log. Everything below `min` may
// legally have been compacted away and must never be read as "never recorded".
type destructionWindow struct {
	MinSeq     int64 `json:"min_seq"`
	MaxSeq     int64 `json:"max_seq"`
	Entries    int   `json:"entries"`
	Continuous bool  `json:"continuous"`
}

type destructionProblem struct {
	DestructionSeq int64  `json:"destruction_seq"`
	Verdict        string `json:"verdict"`
	Detail         string `json:"detail,omitempty"`
}

// destructionGroup is one destruction_seq with all of its lines collapsed.
type destructionGroup struct {
	seq      int64
	state    string           // effective (last legal) state
	entry    destructionEntry // the payload commitment (first line)
	lines    int
	conflict bool
	usable   bool   // every line of the group passed every check
	verdict  string // per-group problem verdict (review finding 3 — never discarded)
	detail   string // per-group problem detail
}

// destructionState is the read-only view the reconciliation works from.
// Building it never writes, repairs or compacts.
type destructionState struct {
	groups     []*destructionGroup
	bySeq      map[int64]*destructionGroup
	window     destructionWindow
	verifiable bool
	errs       []string
	total      int // physical lines
}

func (st *destructionState) seqs() []int64 {
	out := make([]int64, 0, len(st.groups))
	for _, g := range st.groups {
		out = append(out, g.seq)
	}
	return out
}

// loadDestructionState reads and validates the log. Strictly fail-closed along
// the same four axes as P41/P42 (I2):
//
//   - an unclassifiable line fails the whole load (skipping is how a tampered
//     prefix disappears);
//   - an entry whose KAK signature does not verify is `destruction_unauthorized`
//     and is NEVER accounted for — it poisons the log's verifiability too,
//     because a log that carries records we cannot trust is not a log we can
//     append to;
//   - a broken line_digest chain or a foreign stream poisons it as well;
//   - the FIRST surviving line is exempt from the prev check (I3): after a legal
//     prefix compaction it points at a line that is legitimately gone.
func loadDestructionState(c destructionConfig) (*destructionState, error) {
	st := &destructionState{bySeq: map[int64]*destructionGroup{}, verifiable: true}
	if !c.enabled() {
		return st, nil
	}
	path := destructionLogPath(c.dir)
	lines, ok, err := readLogLines(path, destructionGroupOf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("destruction log holds %d unclassifiable line(s); refusing to evaluate it", countUnclassified(lines))
	}
	if len(lines) == 0 {
		return st, nil
	}

	// prevLine is the digest of the last physical line; groupPrev is the value
	// the CURRENT group's lines must all carry. Both lines of a group share the
	// group's incoming pointer — that is what lets them share a payload (I1')
	// while still being distinguishable on the chain (I9).
	prevLine := ""
	groupPrev := ""
	groupExempt := true // I3: the FIRST surviving group's incoming pointer is exempt
	prevSeq := int64(0)
	haveGroup := false

	// Authority and state-machine problems are PER-GROUP (quarantined: the
	// group's own lines stay on disk, the on-disk chain keeps running through
	// them, and the group simply never counts as accounting — review findings
	// 2 and 3). Only ORDER damage (a prev mismatch) and a discontinuous window
	// are LOG-FATAL: they make the tail's order untrustworthy, so no
	// disappearance may be asserted at all (st.verifiable = false).
	for i := range lines {
		var e destructionEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			return nil, fmt.Errorf("destruction line %d is not readable: %w", i+1, jerr)
		}
		st.total++
		if !haveGroup || e.DestructionSeq != prevSeq {
			groupPrev = prevLine
			// The exemption belongs to the GROUP, not to the line: after a legal
			// prefix compaction the whole first group points at evidence that is
			// legitimately gone (I3 — otherwise every compaction self-inflicts).
			groupExempt = i == 0
			prevSeq = e.DestructionSeq
			haveGroup = true
		}
		g := st.bySeq[e.DestructionSeq]
		if g == nil {
			g = &destructionGroup{seq: e.DestructionSeq, state: e.State, entry: e, lines: 1, usable: true}
			st.bySeq[e.DestructionSeq] = g
			st.groups = append(st.groups, g)
		} else {
			g.lines++
		}

		// I3: the exemption belongs to the whole first surviving group — both of
		// its lines point at evidence a legal compaction legitimately removed.
		lineExempt := groupExempt

		// Authority (quarantine, chain continues over the on-disk bytes).
		if v := verifyDestructionEntrySignature(&e, c.ka.trust); v.Verdict != sigVerdictOK {
			st.markProblem(g, destructionVerdictUnauthorized, v.Verdict+": "+v.Detail)
		} else if c.streamID != "" && e.StreamID != "" && e.StreamID != c.streamID {
			st.markProblem(g, destructionVerdictUnauthorized, "belongs to stream "+e.StreamID+", not this export directory")
		} else if dg, derr := destructionEntryDigest(&e); derr != nil || dg != e.EntryDigest {
			st.markProblem(g, destructionVerdictChainBroken, "entry_digest does not match its canonical payload")
		}

		// Order (log-fatal — the tail's prev pointers are untrustworthy).
		ld, lerr := destructionLineDigest(&e)
		if lerr != nil {
			return nil, lerr
		}
		if !lineExempt && e.PrevDigest != groupPrev {
			st.verifiable = false
			st.markProblem(g, destructionVerdictChainBroken, "prev_digest breaks the line chain")
			st.errs = append(st.errs, fmt.Sprintf("destruction_seq %d: prev_digest breaks the line chain", e.DestructionSeq))
		}
		prevLine = ld

		// State machine (quarantine — the group cannot count, but the log's
		// order and the other groups are untouched).
		switch {
		case g.lines == 1:
			if e.State != destructionStateIntended {
				// A group must OPEN with the intent: a record that starts at a
				// terminal state is not a destruction we can account for.
				st.markProblem(g, destructionVerdictConflict, fmt.Sprintf("the first line of a group must be intended, got %q", e.State))
			}
		case g.lines > 2:
			// I8: a terminal state is terminal. A third line is a conflict even
			// when it carries the same payload (T220).
			st.markProblem(g, destructionVerdictConflict, "a third line after a terminal state is a conflict")
		case g.state != destructionStateIntended:
			st.markProblem(g, destructionVerdictConflict, fmt.Sprintf("cannot advance from terminal state %q", g.state))
		case !destructionPayloadEqual(g.entry, e):
			// I1': same seq + different payload is a conflict, never a fix-up.
			st.markProblem(g, destructionVerdictConflict, "a second line with a different payload is a conflict")
		case e.State != destructionStateCompleted && e.State != destructionStateAborted:
			st.markProblem(g, destructionVerdictConflict, fmt.Sprintf("%q is not a legal advance from intended", e.State))
		default:
			g.state = e.State
		}
	}

	seqs := st.seqs()
	st.window.MinSeq, st.window.MaxSeq = seqs[0], seqs[0]
	st.window.Entries = len(seqs)
	st.window.Continuous = true
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			st.window.Continuous = false
		}
		if seqs[i] < st.window.MinSeq {
			st.window.MinSeq = seqs[i]
		}
		if seqs[i] > st.window.MaxSeq {
			st.window.MaxSeq = seqs[i]
		}
	}
	if !st.window.Continuous {
		st.verifiable = false
		st.errs = append(st.errs, "window_discontinuous: the surviving destruction_seq run has a hole — no disappearance may be asserted")
	}
	return st, nil
}

// markProblem quarantines ONE group: it never counts as accounting, but the
// problem verdict is PRESERVED (review finding 3 — collapsing every problem
// into a single relabel made a forged record indistinguishable from a broken
// chain on the read surface). Quarantine is per-group by design: the on-disk
// chain keeps running through the quarantined lines, so every OTHER group's
// order and authority remain verifiable.
func (st *destructionState) markProblem(g *destructionGroup, verdict, detail string) {
	g.usable = false
	if g.verdict == "" {
		g.verdict = verdict
		g.detail = detail
	}
	st.errs = append(st.errs, fmt.Sprintf("destruction_seq %d: %s (%s)", g.seq, verdict, detail))
}

// ---------------------------------------------------------------------------
// Appending (I2 fail-closed, R41-6 seq derivation, I8 advance)
// ---------------------------------------------------------------------------

// appendDestructionEntry records ONE line.
//
// When entry.State is `intended` this opens a new group: the seq is derived from
// the log's own maximum inside this critical section (no watermark file — R41-6)
// and the payload is signed by the KAK. When entry.State is a terminal state the
// caller must pass the group's intended entry: the payload, the entry_digest and
// the signature bytes are REUSED, so the advance needs no KAK online (I8).
//
// Appending to a log we cannot fully evaluate is REFUSED (I2): rebuilding a
// clean-looking file around records we cannot read is exactly the laundering
// this discipline exists to prevent.
func appendDestructionEntry(c destructionConfig, e *destructionEntry, at time.Time) error {
	destructionWriteMu.Lock()
	pending := []destructionEntry{}
	err := appendDestructionEntryLocked(c, e, at, &pending)
	destructionWriteMu.Unlock()
	dispatchDestructionPending(c, pending)
	return err
}

func appendDestructionEntryLocked(c destructionConfig, e *destructionEntry, at time.Time, pending *[]destructionEntry) error {
	if !c.writable() {
		return errors.New("destruction: no key-authority key configured (writes disabled)")
	}
	if e == nil {
		return errors.New("destruction: nil entry")
	}
	path := destructionLogPath(c.dir)
	lines, ok, err := readLogLines(path, destructionGroupOf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("destruction log holds %d unclassifiable line(s); refusing to append", countUnclassified(lines))
	}

	maxSeq := int64(0)
	prevLine := ""
	groupPrev := ""
	groupExempt := true // I3, mirrored: the first group's incoming pointer is exempt
	prevSeq := int64(0)
	haveGroup := false
	groupLines := map[int64]int{}
	groupPayload := map[int64]destructionEntry{}
	groupState := map[int64]string{}
	groupLastLd := map[int64]string{}
	for i := range lines {
		var p destructionEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			return fmt.Errorf("destruction line %d is not readable: %w", i+1, jerr)
		}
		if !haveGroup || p.DestructionSeq != prevSeq {
			groupPrev = prevLine
			groupExempt = i == 0
			prevSeq = p.DestructionSeq
			haveGroup = true
		}
		if v := verifyDestructionEntrySignature(&p, c.ka.trust); v.Verdict != sigVerdictOK {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d is %s (%s)", p.DestructionSeq, v.Verdict, v.Detail)
		}
		if c.streamID != "" && p.StreamID != "" && p.StreamID != c.streamID {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d belongs to another export directory", p.DestructionSeq)
		}
		ld, lerr := destructionLineDigest(&p)
		if lerr != nil {
			return lerr
		}
		if !groupExempt && p.PrevDigest != groupPrev {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d breaks the line chain", p.DestructionSeq)
		}
		prevLine = ld
		groupLastLd[p.DestructionSeq] = ld
		groupLines[p.DestructionSeq]++
		if groupLines[p.DestructionSeq] == 1 {
			groupPayload[p.DestructionSeq] = p
			groupState[p.DestructionSeq] = p.State
		} else {
			groupState[p.DestructionSeq] = p.State
		}
		if p.DestructionSeq > maxSeq {
			maxSeq = p.DestructionSeq
		}
	}

	if e.State == destructionStateIntended {
		e.V = 1
		e.DestructionSeq = maxSeq + 1
		e.AuthorityKeyID = c.ka.signer.keyID
		e.StreamID = c.streamID
		e.RecordedAt = at.UTC().Format(time.RFC3339Nano)
		e.PrevDigest = prevLine
		e.EntryDigest = ""
		e.Signature = nil
		if _, ok := destructionKinds[e.Kind]; !ok {
			return fmt.Errorf("destruction: unknown kind %q", e.Kind)
		}
		if len(e.Targets) == 0 {
			return errors.New("destruction: refusing to record a destruction with no targets")
		}
		// destroyed_count is supplied by the caller: publication-face entries
		// carry the number of publications, log-face entries the actual number
		// of dropped LINES (review minor 9 — a range target is one target but
		// many destroyed lines).
		dg, derr := destructionEntryDigest(e)
		if derr != nil {
			return derr
		}
		e.EntryDigest = dg
		if serr := c.ka.signer.signDestructionEntry(e, at); serr != nil {
			return serr
		}
	} else {
		// A legal advance: the payload (hence entry_digest and signature) is
		// byte-identical to the intended line's, only `state` differs.
		if groupLines[e.DestructionSeq] == 0 {
			return fmt.Errorf("destruction: refusing to advance destruction_seq %d — no intended line exists", e.DestructionSeq)
		}
		if groupLines[e.DestructionSeq] >= 2 {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d is already terminal", e.DestructionSeq)
		}
		if groupState[e.DestructionSeq] != destructionStateIntended {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d is %s, not intended", e.DestructionSeq, groupState[e.DestructionSeq])
		}
		base := groupPayload[e.DestructionSeq]
		if !destructionPayloadEqual(base, *e) {
			return fmt.Errorf("destruction: refusing to append — destruction_seq %d payload differs from its intended line", e.DestructionSeq)
		}
		// R43-9 (review round 3, N8): an advance is legal ONLY while the group
		// is still the CHAIN TAIL. Two operations may hold groups open at once
		// (Tick prune across os.Remove, a verifyTick compaction, an HTTP
		// lifecycle event); if this group's incoming pointer is no longer the
		// last physical line's digest, appending the advance here would be
		// NON-ADJACENT and would read as a chain break — a false
		// destruction_chain_broken with no tampering, permanent phase death.
		// Refusing leaves the group `intended` ⇒ loud destruction_unconfirmed
		// (ADR-consistent: no auto-advance, no false close), and the log and
		// every other group stay healthy.
		// Adjacency: the group's INTENT line must still be the chain tail. The
		// advance line reuses the group's incoming pointer (I9), so appending it
		// after any later line would place it non-adjacently and read as a
		// chain break.
		if prevLine != groupLastLd[e.DestructionSeq] {
			return fmt.Errorf("destruction: refusing to advance destruction_seq %d — the group is no longer the chain tail (another destruction opened after it); it stays intended and will surface as destruction_unconfirmed", e.DestructionSeq)
		}
		// The group's incoming pointer is part of the payload, so the advance
		// must carry the SAME one — that is what keeps the payloads identical
		// and the signature reusable (I8 / I1').
		e.EntryDigest = base.EntryDigest
		e.Signature = base.Signature
		e.PrevDigest = base.PrevDigest
	}

	raw, merr := serializeDestructionEntryBytes(e)
	if merr != nil {
		return merr
	}
	if aerr := appendLogLine(path, raw); aerr != nil {
		return aerr
	}
	// I5: the log bounds ITSELF, and only once a group is closed. Compacting
	// after the intent line would let the self-compaction's own intent trigger
	// another compaction, which is the recursion the dedicated path exists to
	// avoid (termination is otherwise only "empirically" shallow).
	if c.capacity > 0 && e.State != destructionStateIntended {
		if cerr := compactDestructionSelfLocked(c, at, pending); cerr != nil {
			return cerr
		}
	}
	return nil
}

var destructionKinds = map[string]bool{
	destructionKindSnapshotRetention:      true,
	destructionKindLedgerCompaction:       true,
	destructionKindAnchorCompaction:       true,
	destructionKindKeyLifecycleCompaction: true,
	destructionKindVerificationCompaction: true,
	destructionKindSelfCompaction:         true,
	destructionKindAcceptanceCompaction:   true,
}

// destructionWriteMu is the log's critical section (R43-3 / review finding 1).
// Destruction records are the FIRST shared log with writers in three independent
// synchronization domains — the scheduler Tick (prune + compaction observers),
// the verification tick (verification/lifecycle compactions) and the admin HTTP
// face (key-lifecycle events). Two interleaved read→append sequences would both
// derive the same destruction_seq and prev_digest, and the second line would
// land as a same-seq/different-payload conflict — an unrecoverably forked chain.
// Every write path therefore funnels through the *Locked helpers below, which
// assume the caller holds this mutex; the public wrappers acquire it exactly
// once per top-level operation (no nesting — compactDestructionSelfLocked calls
// the Locked variants of begin/complete directly).
var destructionWriteMu sync.Mutex

// beginDestruction records the INTENT line and returns it (the caller needs both
// its seq and, for the anchor, its digest). The caller must then perform the
// destruction and call completeDestruction.
func beginDestruction(c destructionConfig, kind, policy string, unavailable []string, targets []destructionTarget, destroyedCount int, at time.Time) (*destructionEntry, error) {
	destructionWriteMu.Lock()
	pending := []destructionEntry{}
	e, err := beginDestructionLocked(c, kind, policy, unavailable, targets, destroyedCount, at, &pending)
	destructionWriteMu.Unlock()
	// beginDestruction appends an `intended` line, which never triggers the
	// self-compaction tail, so `pending` is empty here in practice — the
	// dispatch is threaded for signature uniformity and future safety.
	dispatchDestructionPending(c, pending)
	return e, err
}

func beginDestructionLocked(c destructionConfig, kind, policy string, unavailable []string, targets []destructionTarget, destroyedCount int, at time.Time, pending *[]destructionEntry) (*destructionEntry, error) {
	if !c.writable() {
		return nil, errors.New("destruction: not writable")
	}
	e := &destructionEntry{
		Kind:                    kind,
		Targets:                 targets,
		DestroyedCount:          destroyedCount,
		Policy:                  policy,
		State:                   destructionStateIntended,
		TargetDigestUnavailable: unavailable,
	}
	if aerr := appendDestructionEntryLocked(c, e, at, pending); aerr != nil {
		return nil, aerr
	}
	return e, nil
}

// completeDestruction closes the group. `aborted` is used when the destruction
// did not fully happen — claiming `completed` for a destruction that only partly
// succeeded would be the log itself lying (ADR-061 §6.3).
//
// Implementation note (a deliberate, reported deviation): because I8 requires the
// advance line to reuse the intended line's signature bytes, the payload —
// including `destroyed_count` — is frozen at intent time and can never be
// retro-fitted with an "actual" count. A partial destruction is therefore
// recorded as `aborted`, never as a `completed` line carrying a corrected
// number.
func completeDestruction(c destructionConfig, seq int64, state string, at time.Time) error {
	destructionWriteMu.Lock()
	pending := []destructionEntry{}
	err := completeDestructionLocked(c, seq, state, at, &pending)
	destructionWriteMu.Unlock()
	// N1 fix (review round 2): the anchor dispatch runs AFTER the mutex is
	// released. The dispatch can compact the destruction-anchor log, whose
	// observer re-enters the PUBLIC beginDestruction — under the lock that was
	// a deterministic self-deadlock (reproduced); here the mutex is free. It
	// also keeps witness network I/O out of the critical section (N2).
	dispatchDestructionPending(c, pending)
	return err
}

func completeDestructionLocked(c destructionConfig, seq int64, state string, at time.Time, pending *[]destructionEntry) error {
	if !c.writable() {
		return errors.New("destruction: not writable")
	}
	st, err := loadDestructionState(c)
	if err != nil {
		return err
	}
	g, ok := st.bySeq[seq]
	if !ok {
		return fmt.Errorf("destruction: group %d vanished before it could be closed", seq)
	}
	e := g.entry
	e.State = state
	if aerr := appendDestructionEntryLocked(c, &e, at, pending); aerr != nil {
		return aerr
	}
	// ADR-061 §4.3 step 4 — EVERY terminal destruction record is dispatched for
	// anchoring (review finding 6: prune and self_compaction were previously
	// never witnessed, an asymmetric accountability). The DISPATCH itself is
	// deferred to the outermost public caller (N1): inside the Locked helpers
	// we only collect.
	*pending = append(*pending, e)
	return nil
}

// destructionDispatchMu serializes the post-unlock anchor dispatches (review
// round 3, N9): Tick, verifyTick and the HTTP face can dispatch concurrently,
// and two interleaved nextAnchorSeqPath + append sequences on
// destruction-anchor.jsonl would fork that witness stream. Deliberately a
// DIFFERENT mutex from destructionWriteMu — the dispatch may re-enter the
// write path through compaction observers, so a shared lock would deadlock.
// Lock ordering is always dispatchMu → writeMu, never the reverse.
var destructionDispatchMu sync.Mutex

// dispatchDestructionPending runs the anchor hook for every durably appended
// terminal record. I6-safe by contract: the hook logs its own failures and
// never blocks. Called OUTSIDE the write mutex only.
func dispatchDestructionPending(c destructionConfig, pending []destructionEntry) {
	if c.anchorDispatch == nil || len(pending) == 0 {
		return
	}
	destructionDispatchMu.Lock()
	defer destructionDispatchMu.Unlock()
	for _, e := range pending {
		_ = c.anchorDispatch(e)
	}
}

// ---------------------------------------------------------------------------
// I5 — the log accounting for its own compaction
// ---------------------------------------------------------------------------

// compactDestructionSelf bounds the destruction log by whole groups. It MUST NOT
// go through the observed variant of the primitive: the observer records into
// this very log, so observing our own compaction would recurse. Termination is
// structural — the dedicated path below writes its own record and never
// triggers an observer (ADR-061 §6.2).
func compactDestructionSelf(c destructionConfig, at time.Time) error {
	destructionWriteMu.Lock()
	pending := []destructionEntry{}
	err := compactDestructionSelfLocked(c, at, &pending)
	destructionWriteMu.Unlock()
	dispatchDestructionPending(c, pending)
	return err
}

func compactDestructionSelfLocked(c destructionConfig, at time.Time, pending *[]destructionEntry) error {
	path := destructionLogPath(c.dir)
	lines, ok, err := readLogLines(path, destructionGroupOf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("destruction self-compaction refused: %d unclassifiable line(s) present", countUnclassified(lines))
	}
	// One fewer than the cap: the record we are about to write is itself a
	// group, so landing ON the cap is what makes the recursion terminate. With
	// `capacity` here instead of `capacity-1` every cycle would drop exactly one
	// group and add exactly one, and the log would never converge.
	order := make([]int64, 0, len(lines))
	seen := make(map[int64]bool, len(lines))
	for _, l := range lines {
		if !seen[l.group] {
			seen[l.group] = true
			order = append(order, l.group)
		}
	}
	if len(order) <= c.capacity {
		return nil
	}
	keep := c.capacity - 1
	if keep < 0 {
		keep = 0
	}
	drop := make(map[int64]bool, len(order))
	if len(order) > keep {
		for _, g := range order[:len(order)-keep] {
			drop[g] = true
		}
	}
	kept := make([]logLine, 0, len(lines))
	var dropped []logLine
	for _, l := range lines {
		if drop[l.group] {
			dropped = append(dropped, l)
			continue
		}
		kept = append(kept, l)
	}
	if len(dropped) == 0 {
		return nil
	}
	minSeq, maxSeq := dropped[0].group, dropped[0].group
	for _, l := range dropped {
		if l.group < minSeq {
			minSeq = l.group
		}
		if l.group > maxSeq {
			maxSeq = l.group
		}
	}
	h := sha256.New()
	for _, l := range dropped {
		h.Write(l.raw)
		h.Write([]byte{'\n'})
	}
	targets := []destructionTarget{{FromSeq: minSeq, ToSeq: maxSeq, PrefixDigest: hex.EncodeToString(h.Sum(nil))}}

	// 1. intent FIRST — the digest is captured while the prefix still exists.
	intent, berr := beginDestructionLocked(c, destructionKindSelfCompaction, fmt.Sprintf("capacity=%d", c.capacity), nil, targets, len(dropped), at, pending)
	if berr != nil {
		return berr
	}
	seq := intent.DestructionSeq
	// 2. the rewrite keeps the survivors AND the line we just appended.
	st, lerr := loadDestructionState(c)
	if lerr != nil {
		return lerr
	}
	g, ok := st.bySeq[seq]
	if !ok {
		return fmt.Errorf("destruction: self_compaction group %d not found after append", seq)
	}
	raw, serr := serializeDestructionEntryBytes(&g.entry)
	if serr != nil {
		return serr
	}
	trimmed := []byte(strings.TrimSpace(string(raw)))
	kept = append(kept, logLine{raw: trimmed, group: seq, classified: true})
	if rerr := rewriteLogLines(path, kept); rerr != nil {
		// The prefix is still on disk and the intent is recorded: the group
		// stays `intended`, which is loud (destruction_unconfirmed) and honest.
		return rerr
	}
	// 3. close the group (the anchor dispatch happens inside, via the config
	// hook — self_compaction records are witnessed like every other kind).
	return completeDestructionLocked(c, seq, destructionStateCompleted, at, pending)
}

// ---------------------------------------------------------------------------
// Read surface — the reconciliation
// ---------------------------------------------------------------------------

// DestructionView is the derived, strictly read-only projection. It has ZERO
// side effects: it writes nothing, audits nothing and opens no network
// connection (ADR-061 §7).
type DestructionView struct {
	Enabled             bool                 `json:"enabled"`
	Verdict             string               `json:"verdict"`
	Window              *destructionWindow   `json:"window,omitempty"`
	Absent              bool                 `json:"destruction_absent"`
	WindowDiscontinuous bool                 `json:"window_discontinuous,omitempty"`
	Missing             []int64              `json:"missing,omitempty"`
	Accounted           []int64              `json:"accounted,omitempty"`
	Unaccounted         []int64              `json:"unaccounted,omitempty"`
	NotAssertable       []int64              `json:"not_assertable,omitempty"`
	Unconfirmed         []int64              `json:"unconfirmed,omitempty"`
	Problems            []destructionProblem `json:"problems,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	Error               string               `json:"error,omitempty"`
}

// destructionStatusSummary is the roll-up on the scheduler status face.
type destructionStatusSummary struct {
	Enabled  bool               `json:"enabled"`
	Writable bool               `json:"writable,omitempty"`
	Records  int                `json:"records,omitempty"`
	Window   *destructionWindow `json:"window,omitempty"`
	Error    string             `json:"error,omitempty"`
}

func destructionSummary(c destructionConfig) destructionStatusSummary {
	out := destructionStatusSummary{}
	if !c.enabled() {
		return out
	}
	out.Enabled = true
	out.Writable = c.writable()
	st, err := loadDestructionState(c)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Records = st.total
	w := st.window
	out.Window = &w
	if !st.verifiable {
		out.Error = "destruction log is not verifiable — no disappearance is asserted (" + strings.Join(st.errs, "; ") + ")"
	}
	return out
}

// knownPublication is a publication some surviving evidence says once existed.
type knownPublication struct {
	id          int64
	digest      string // empty when the digest is not recoverable
	publishedAt string
}

// DestructionView derives the read-only verdict.
//
//	existed  = ledger ∪ chain(prev refs) ∪ anchor(publication family) ∪ verification items
//	present  = manifests enumerable on disk
//	missing  = existed − present
//	recorded = targets of COMPLETED, authorized, in-window groups
//	unaccounted = missing − recorded         ← the payoff
func (s *HistoryExportScheduler) DestructionView() (DestructionView, error) {
	out := DestructionView{}
	if !s.destructionEnabled() {
		return out, nil
	}
	out.Enabled = true
	c := s.destructionConfig()
	if destructionLedgerAbsent(c.dir) {
		// Escape B, stated honestly: no log at all is indistinguishable from
		// "the log was deleted". It is NEVER dressed up as `accounted` (T216).
		out.Verdict = destructionVerdictAbsent
		out.Absent = true
		out.Reason = "the destruction log is absent: an unrecorded disappearance and a deleted log are locally indistinguishable"
		return out, nil
	}
	st, err := loadDestructionState(c)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	w := st.window
	out.Window = &w
	if st.total > 0 && !st.window.Continuous {
		out.WindowDiscontinuous = true
	}
	for _, g := range st.groups {
		if !g.usable {
			// Per-group problems keep their own verdict (review finding 3): a
			// forged record reports destruction_unauthorized, a rewritten one
			// destruction_chain_broken, a state-machine violation
			// destruction_conflict — an operator can tell them apart.
			out.Problems = append(out.Problems, destructionProblem{DestructionSeq: g.seq, Verdict: g.verdict, Detail: g.detail})
			continue
		}
		if g.state == destructionStateIntended {
			out.Unconfirmed = append(out.Unconfirmed, g.seq)
		}
	}
	// I4 is structural and comes FIRST: a hole in the surviving seq run is a
	// statement about the log's shape, and it forbids any disappearance
	// assertion regardless of what else is wrong with the log.
	if st.total > 0 && !st.window.Continuous {
		out.Verdict = destructionVerdictWindowDiscontinuous
		out.Reason = "the surviving destruction_seq run has a hole — no disappearance may be asserted (" + strings.Join(st.errs, "; ") + ")"
		return out, nil
	}
	if !st.verifiable {
		// LOG-FATAL: the on-disk order itself is untrustworthy (a prev mismatch).
		// Per-group quarantine does NOT land here — an unauthorized entry is
		// quarantined and the accounting below proceeds without it, which is
		// what keeps T217's payoff alive (the victim stays named).
		out.Error = "destruction log order is not verifiable — no disappearance is asserted (" + strings.Join(st.errs, "; ") + ")"
		out.Verdict = destructionVerdictChainBroken
		return out, nil
	}

	existed, err := s.knownPublications()
	if err != nil {
		// A source that cannot be read is not a source that can be skipped
		// (review finding 5): shrinking the "existed" union on a load failure
		// would report `accounted` for a deletion the phase cannot evaluate.
		return out, fmt.Errorf("destruction: publication census unavailable — no disappearance is asserted: %w", err)
	}
	present, err := s.diskPublications()
	if err != nil {
		return out, fmt.Errorf("destruction: on-disk publication census unavailable — no disappearance is asserted: %w", err)
	}
	var missing []int64
	for id := range existed {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	out.Missing = missing

	covered := map[int64]bool{}
	for _, g := range st.groups {
		// Quarantined groups never count as accounting — that is exactly what
		// keeps a forged record from washing the disappearance it tries to
		// hide (T217's payoff).
		if !g.usable || g.state != destructionStateCompleted {
			continue
		}
		for _, t := range g.entry.Targets {
			if t.PublicationID == 0 {
				continue
			}
			// An empty digest is not a wildcard, but it is also not a
			// contradiction: matching degrades to the id (ADR-061 §4.2).
			if t.ManifestDigest != "" {
				if known, ok := existed[t.PublicationID]; ok && known.digest != "" && known.digest != t.ManifestDigest {
					continue
				}
			}
			covered[t.PublicationID] = true
		}
	}

	// I4 — the window discipline. A publication that existed BEFORE the oldest
	// surviving record may have been recorded in a group that a legal prefix
	// compaction dropped; the two histories are indistinguishable, so nothing is
	// asserted about it (the R42-3 boundary rule, instantiated here).
	fullHistory := st.window.MinSeq <= 1
	oldestRecordedAt := ""
	if len(st.groups) > 0 {
		oldestRecordedAt = st.groups[0].entry.RecordedAt
	}
	var reasons []string
	for _, id := range missing {
		if covered[id] {
			out.Accounted = append(out.Accounted, id)
			continue
		}
		if fullHistory {
			out.Unaccounted = append(out.Unaccounted, id)
			continue
		}
		pub := existed[id]
		if oldestRecordedAt == "" || pub.publishedAt == "" {
			// Review finding 4: an UNKNOWN publish time is exactly the case the
			// boundary rule exists for — its covering record may have been
			// legally compacted away. Asserting `unaccounted` here would be a
			// false accusation; the conservative output is not-assertable.
			out.NotAssertable = append(out.NotAssertable, id)
			reasons = append(reasons, fmt.Sprintf("publication %d has no recoverable publish time — its covering record may predate the assertion boundary", id))
			continue
		}
		if compareRFC3339(pub.publishedAt, oldestRecordedAt) >= 0 {
			// Its record (if any) was written after the oldest survivor ⇒ it
			// would still be here ⇒ the absence of a record is assertable.
			out.Unaccounted = append(out.Unaccounted, id)
			continue
		}
		out.NotAssertable = append(out.NotAssertable, id)
	}
	if len(out.NotAssertable) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d missing publication(s) predate the oldest surviving destruction record (min_seq=%d) — a legal prefix compaction may have dropped their record", len(out.NotAssertable), st.window.MinSeq))
	}

	switch {
	case len(out.Unaccounted) > 0:
		out.Verdict = destructionVerdictUnaccounted
	case len(out.Unconfirmed) > 0:
		out.Verdict = destructionVerdictUnconfirmed
		reasons = append(reasons, "at least one group is stuck at `intended`: the destruction may have happened but was never closed (crash or refusal)")
	case len(out.Problems) > 0:
		out.Verdict = destructionVerdictConflict
	case len(out.Missing) == 0:
		out.Verdict = destructionVerdictAccounted
		reasons = append(reasons, "every publication known to have existed is still present, or its removal is covered by an authorized destruction record")
	default:
		out.Verdict = destructionVerdictAccounted
	}
	if st.total > 0 && !st.window.Continuous {
		out.Verdict = destructionVerdictWindowDiscontinuous
		reasons = append(reasons, "the surviving destruction_seq run is discontinuous — no disappearance is asserted")
	}
	out.Reason = strings.Join(reasons, "; ")
	return out, nil
}

// diskPublications enumerates the publications still on disk. The enumeration
// is PAGINATED to exhaustion (review finding 4): a single bounded page would
// silently drop the oldest publications from `present` and mass-report false
// `unaccounted_disappearance` on every retention size above the page limit. A
// census error is fatal for the view, never an empty map.
func (s *HistoryExportScheduler) diskPublications() (map[int64]bool, error) {
	out := map[int64]bool{}
	cursor := ""
	for {
		entries, next, err := s.ListManifests(destructionSubjectLimit, cursor)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.PublicationID == 0 {
				continue
			}
			out[e.PublicationID] = true
		}
		if next == "" || len(entries) == 0 {
			return out, nil
		}
		cursor = next
	}
}

// knownPublications collects every publication some SURVIVING evidence says once
// existed. P43 consumes the earlier Phases' outputs; it never re-derives them.
// Any source load FAILURE is returned, never swallowed (review finding 5): a
// corrupt ledger silently shrinking the union would turn a real deletion into
// `accounted` — the one fail-open this phase must never contain.
func (s *HistoryExportScheduler) knownPublications() (map[int64]knownPublication, error) {
	out := map[int64]knownPublication{}
	// P39 ledger: a publication is recorded, and its `prev` pointer names one.
	if ls, err := loadLedgerState(s.cfg.Dir, s.trust); err != nil {
		return nil, fmt.Errorf("ledger source: %w", err)
	} else if ls != nil {
		for id, e := range ls.usable {
			if id == 0 {
				continue
			}
			out[id] = knownPublication{id: id, digest: e.ManifestDigest}
			if e.PrevPublicationID > 0 {
				if _, ok := out[e.PrevPublicationID]; !ok {
					out[e.PrevPublicationID] = knownPublication{id: e.PrevPublicationID, digest: e.PrevManifestDigest}
				}
			}
		}
	}
	// P40 anchor: the publication family only (the other families are addressed
	// by their own sequence spaces).
	if as, err := loadAnchorState(s.cfg.Dir, s.trust); err != nil {
		return nil, fmt.Errorf("anchor source: %w", err)
	} else if as != nil {
		for _, e := range as.latest {
			if e.Kind != anchorKindPublication {
				continue
			}
			if e.identityID() <= 0 {
				continue
			}
			id := e.identityID()
			if _, ok := out[id]; !ok {
				out[id] = knownPublication{id: id, digest: e.ManifestDigest}
			}
		}
	}
	// P42 verification reports.
	if s.verificationEnabled() {
		vc := s.verificationConfig()
		vs, err := loadVerificationState(vc)
		if err != nil {
			return nil, fmt.Errorf("verification source: %w", err)
		}
		if vs != nil {
			for _, e := range vs.entries {
				for _, it := range e.Items {
					if it.PublicationID <= 0 {
						continue
					}
					if _, ok := out[it.PublicationID]; !ok {
						out[it.PublicationID] = knownPublication{id: it.PublicationID}
					}
				}
			}
		}
	}
	// Publications still on disk carry their own publish time, which the I4
	// boundary rule needs. Paginated to exhaustion, like the present-census.
	cursor := ""
	for {
		entries, next, err := s.ListManifests(destructionSubjectLimit, cursor)
		if err != nil {
			return nil, fmt.Errorf("manifest source: %w", err)
		}
		for _, e := range entries {
			if e.PublicationID == 0 {
				continue
			}
			prev := out[e.PublicationID]
			prev.id = e.PublicationID
			prev.publishedAt = e.GeneratedAt
			out[e.PublicationID] = prev
		}
		if next == "" || len(entries) == 0 {
			return out, nil
		}
		cursor = next
	}
}

// ---------------------------------------------------------------------------
// Scheduler wiring
// ---------------------------------------------------------------------------

func (s *HistoryExportScheduler) destructionEnabled() bool {
	return s != nil && s.cfg.DestructionLog && s.keyAuthority != nil && s.keyAuthority.verifiable()
}

func (s *HistoryExportScheduler) destructionConfig() destructionConfig {
	if s == nil {
		return destructionConfig{}
	}
	c := destructionConfig{
		dir:      s.cfg.Dir,
		capacity: s.cfg.DestructionCapacity,
		ka:       s.keyAuthority,
		streamID: s.lifecycleStreamID(),
		on:       s.cfg.DestructionLog,
	}
	// I6: the anchor is an EXIT, never an input. The hook logs its own failures
	// (setDestructionError) and never blocks the accounting path.
	if s.anchorEnabled() {
		c.anchorDispatch = func(e destructionEntry) error {
			if aerr := s.anchorDestructionEntry(e); aerr != nil {
				s.setDestructionError(aerr.Error())
			}
			return nil
		}
	}
	return c
}

func (s *HistoryExportScheduler) setDestructionError(msg string) {
	s.mu.Lock()
	s.destructionError = msg
	s.mu.Unlock()
}

func (s *HistoryExportScheduler) currentDestructionError() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destructionError
}

// destructionObserver builds the hook the four compaction paths install. It is
// called by the shared primitive AFTER the drop set is computed and BEFORE the
// rewrite; returning an error REFUSES the compaction (non-target 15: a
// system-initiated destruction we cannot account for does not happen).
//
// When the Phase is off it returns nil, so the four paths behave exactly as they
// did in Phase 42 (default zero regression).
func (s *HistoryExportScheduler) destructionObserver(kind string) compactionObserver {
	if s == nil || !s.destructionEnabled() {
		return nil
	}
	return func(path string, droppedGroups []int64, droppedLines []logLine) (func() error, error) {
		c := s.destructionConfig()
		if !c.writable() {
			return nil, nil
		}
		if len(droppedLines) == 0 {
			return nil, nil
		}
		minSeq, maxSeq := droppedGroups[0], droppedGroups[0]
		for _, g := range droppedGroups {
			if g < minSeq {
				minSeq = g
			}
			if g > maxSeq {
				maxSeq = g
			}
		}
		h := sha256.New()
		for _, l := range droppedLines {
			h.Write(l.raw)
			h.Write([]byte{'\n'})
		}
		targets := []destructionTarget{{FromSeq: minSeq, ToSeq: maxSeq, PrefixDigest: hex.EncodeToString(h.Sum(nil))}}
		at := s.clock()
		// destroyed_count for the log face = the actual number of dropped LINES
		// (review minor 9 — a range target is one target but many lost lines).
		intent, berr := beginDestruction(c, kind, fmt.Sprintf("capacity=%d", c.capacity), nil, targets, len(droppedLines), at)
		if berr != nil {
			s.setDestructionError(berr.Error())
			return nil, berr
		}
		return func() error {
			// The anchor dispatch of the COMPLETED record happens inside
			// completeDestruction via the config hook (review finding 6 —
			// every kind is witnessed, and the witness sees the final state,
			// not a stale `intended`).
			if cerr := completeDestruction(c, intent.DestructionSeq, destructionStateCompleted, s.clock()); cerr != nil {
				s.setDestructionError(cerr.Error())
				return cerr
			}
			return nil
		}, nil
	}
}

// ---------------------------------------------------------------------------
// Read route (admin-only, :8082)
// ---------------------------------------------------------------------------

// handleHistoryExportDestruction serves
// GET /management/v1/protection/alerts/history/export/destruction — the derived,
// strictly read-only view. It answers 503 when the Phase is off: a disabled
// accountability is never dressed up as an empty success.
func (s *Server) handleHistoryExportDestruction(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.historyScheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduled history export is disabled (configure --export-interval and --export-dir to enable)")
		return
	}
	if !s.historyScheduler.destructionEnabled() {
		writeError(w, http.StatusServiceUnavailable, "destruction accountability is not enabled (configure --export-destruction-log with --export-key-authority and --export-key-authority-trust)")
		return
	}
	res, rerr := s.historyScheduler.DestructionView()
	if rerr != nil {
		writeError(w, http.StatusInternalServerError, rerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
