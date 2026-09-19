package server

// Phase 45 — Input Integrity: record-level at-acceptance evidence (ADR-065
// scope, ADR-066 architecture).
//
// What P30~P44 cannot say: every one of their proofs starts at the EXPORT
// point. The single durable write ingress is FileBackedTransitionStore.Append
// (P30-I6), and an attacker holding STORE write permission (sqlite/file direct
// write, not process control) can alter a payload, delete a record or insert a
// forged one; the next export faithfully signs the tampered content —
// signature_ok, chain_ok, anchor, verification, destruction all green. "The
// record the system originally accepted" and "the record now in the store" are
// locally the same shape, and six phases of evidence are silent about it.
//
// This Phase fixes the record's at-acceptance identity: right after the durable
// Sync, INSIDE the store's critical section, the store computes
//
//	record_digest = sha256(canonical durable line bytes)
//
// and appends a KAK-signed acceptance entry to record-acceptance.jsonl — a
// hash-chained ledger of FACTS (an entry is never a state machine). The
// reconciliation (verify/read time) pairs the store snapshot with the ledger
// and can then assert, for the first time:
//
//	record_modified        — the durable bytes no longer match what was accepted
//	record_missing_in_span — a record inside the assertable span disappeared
//	record_unaccepted      — a record exists that was never accepted (crash
//	                         window or refused recording — loud, never silent)
//
// Division of labour across Phases, stated once:
//
//	P37 WHO · P40 WHERE · P41 WHEN · P42 WHETHER · P43 WHY ABSENT ·
//	P45 WHAT ACCEPTED.
//
// Frozen boundaries (ADR-065 A6 / ADR-066 §1, honoured here):
//   - appendonly_log.go is REUSED unchanged (one persistence discipline);
//   - internal/protection/alerting.go gains ONLY the additive Seqs field;
//   - the signature is the Phase 37 block, signed by the Phase 41 KAK — never
//     by the export signing key (R43-1 lineage: same-key signing would let the
//     holder of the export key launder the ledger);
//   - with --export-acceptance-log off nothing is written, no file is created,
//     no output field appears anywhere (I9 — default zero regression).

import (
	"bufio"
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
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// acceptanceLogFile lives in the export directory, next to the manifest, the
// chain ledger, the anchor logs, the lifecycle log, the verification log and
// the destruction log (R41-3: same domain, never fanned out).
const acceptanceLogFile = "record-acceptance.jsonl"

func acceptanceLogPath(dir string) string { return filepath.Join(dir, acceptanceLogFile) }

// The verdicts this Phase adds. `input_modified` is the payoff (the core
// red case T239); every "we cannot say" is said out loud instead of being
// folded into `input_ok`.
const (
	inputVerdictAbsent       = "input_absent"
	inputVerdictOK           = "input_ok"
	inputVerdictModified     = "input_modified"
	inputVerdictIncomplete   = "input_incomplete"
	inputVerdictUnverifiable = "input_unverifiable"

	// Trust reasons for the unverifiable verdict (ADR-065 A4 trust_reason).
	inputTrustLedgerUnverifiable = "acceptance_ledger_unverifiable"
	inputTrustStoreUnverifiable  = "store_unverifiable"

	// acceptanceVerdictConflict is the per-entry problem verdict for a second
	// line carrying one entry_seq (I3 — an acceptance entry is a FACT, so a
	// duplicate is a conflict even with an identical payload, T244).
	acceptanceVerdictConflict = "conflict"
)

// ---------------------------------------------------------------------------
// The entry and its canonical bytes (ADR-066 §2.1)
// ---------------------------------------------------------------------------

// acceptanceEntry is one line of the acceptance ledger.
//
// It is a FACT, not a state (ADR-065 A1 — the P41 model, not the P40 anchor
// model): there is NO state region, and a second line for the same entry_seq
// is a conflict at BOTH the load and the append guard (I3). There is
// deliberately no line_digest: the entry has no state to advance, so
// entry_digest alone is the chain value (ADR-066 §2.2 — the P41 single-digest
// pattern, not the P43 two-digest pattern).
type acceptanceEntry struct {
	// ---- payload: immutable, KAK-signed ----
	V               int    `json:"v"` // 1
	EntrySeq        int64  `json:"entry_seq"`
	RecordSeq       int64  `json:"record_seq"`
	RecordDigest    string `json:"record_digest"` // sha256(canonical durable line bytes)
	RecordedAt      string `json:"recorded_at"`   // RFC3339Nano, store-side clock (P34-CLOCK-1)
	AuthorityKeyID  string `json:"authority_key_id"`
	PrevEntryDigest string `json:"prev_entry_digest,omitempty"` // first surviving entry exempt (I4)
	// ---- derived ----
	EntryDigest string          `json:"entry_digest"` // sha256(canonical payload)
	Signature   *signatureBlock `json:"signature,omitempty"`
}

// acceptanceSigned is the exact structure canonicalAcceptancePayload
// serializes: the payload region, in a fixed field order. Same canonical
// serializer family as P37/P40/P41/P42/P43 — a second one would drift.
type acceptanceSigned struct {
	V               int    `json:"v"`
	EntrySeq        int64  `json:"entry_seq"`
	RecordSeq       int64  `json:"record_seq"`
	RecordDigest    string `json:"record_digest"`
	RecordedAt      string `json:"recorded_at"`
	AuthorityKeyID  string `json:"authority_key_id"`
	PrevEntryDigest string `json:"prev_entry_digest,omitempty"`
}

func acceptanceSignedFields(e *acceptanceEntry) acceptanceSigned {
	return acceptanceSigned{
		V:               e.V,
		EntrySeq:        e.EntrySeq,
		RecordSeq:       e.RecordSeq,
		RecordDigest:    e.RecordDigest,
		RecordedAt:      e.RecordedAt,
		AuthorityKeyID:  e.AuthorityKeyID,
		PrevEntryDigest: e.PrevEntryDigest,
	}
}

// canonicalAcceptancePayload returns the exact bytes the KAK signature covers.
func canonicalAcceptancePayload(e *acceptanceEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil acceptance entry")
	}
	return json.Marshal(acceptanceSignedFields(e))
}

// acceptanceEntryDigest is the per-entry commitment: sha256(canonical payload).
func acceptanceEntryDigest(e *acceptanceEntry) (string, error) {
	payload, err := canonicalAcceptancePayload(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// acceptanceDigestOfLine is the RECORD commitment (I2): sha256 of the durable
// line bytes exactly as Append wrote them — json.Marshal(newPersisted(seq, t)).
// On reconciliation the bytes are REBUILT through the same constructor, so a
// semantic-equivalent JSON rewrite (key order / whitespace) correctly reads
// intact while any field value change reads modified (ADR-065 A2).
func acceptanceDigestOfLine(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

// acceptanceCanonicalRecordBytes rebuilds the canonical durable line bytes of a
// snapshot record (I2): the SAME constructor (newPersisted — the single
// construction point) and the SAME field order the Append path used.
// T257's golden-digest fixture freezes this cross-version byte contract.
func acceptanceCanonicalRecordBytes(seq int64, t protection.AlertTransition) ([]byte, error) {
	return json.Marshal(newPersisted(seq, t))
}

func serializeAcceptanceEntryBytes(e *acceptanceEntry) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// acceptanceGroupOf is the classifier this ledger injects into the shared
// append-only primitive: one `entry_seq` is ONE group.
func acceptanceGroupOf(raw []byte) (int64, bool) {
	var e acceptanceEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return e.EntrySeq, true
}

// ---------------------------------------------------------------------------
// KAK signing / verification (R43-1 lineage — never the export signing key)
// ---------------------------------------------------------------------------

func (s *exportSigner) signAcceptanceEntry(e *acceptanceEntry, at time.Time) error {
	if s == nil {
		return errors.New("acceptance: no key-authority (KAK) private key configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("acceptance: private key is not a valid Ed25519 key")
	}
	if e.Signature == nil {
		e.Signature = &signatureBlock{}
	}
	e.Signature.Alg = signatureAlgEd25519
	e.Signature.KeyID = s.keyID
	e.Signature.SignedAt = at.UTC().Format(time.RFC3339Nano)
	e.Signature.Sig = ""
	payload, err := canonicalAcceptancePayload(e)
	if err != nil {
		return err
	}
	e.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyAcceptanceEntrySignature applies the frozen Phase 37 decision order to
// an acceptance entry, against the KAK trust anchor.
func verifyAcceptanceEntrySignature(e *acceptanceEntry, trust *exportTrustStore) SignatureVerdict {
	if e == nil || e.Signature == nil {
		id := ""
		if e != nil {
			id = e.AuthorityKeyID
		}
		return SignatureVerdict{Verdict: sigVerdictAbsent, KeyID: id, Detail: "acceptance entry carries no signature"}
	}
	sb := e.Signature
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "acceptance signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "acceptance signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted key-authority keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "acceptance authority_key_id is not in the trusted KAK set"}
	}
	if e.AuthorityKeyID != "" && e.AuthorityKeyID != sb.KeyID {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "acceptance authority_key_id disagrees with the signing key id"}
	}
	payload, perr := canonicalAcceptancePayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "acceptance signature does not verify against the trusted KAK"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// ---------------------------------------------------------------------------
// Loading — strictly fail-closed (I2 generalised: T243)
// ---------------------------------------------------------------------------

// acceptanceWindow is the surviving contiguous run of entry_seq values — the
// R40-1 discipline instantiated for the acceptance ledger (ADR-065 A8-6: the
// window is ALWAYS part of the result, so an operator can see how much of the
// history is still covered by at-acceptance evidence).
type acceptanceWindow struct {
	MinEntry   int64 `json:"min_entry"`
	MaxEntry   int64 `json:"max_entry"`
	Entries    int   `json:"entries"`
	Continuous bool  `json:"continuous"`
}

// acceptanceProblem is one ledger line that exists but cannot be trusted.
type acceptanceProblem struct {
	EntrySeq int64  `json:"entry_seq"`
	Verdict  string `json:"verdict"`
	Detail   string `json:"detail,omitempty"`
}

// acceptanceState is the read-only view the reconciliation works from.
type acceptanceState struct {
	entries    []acceptanceEntry // fully verified, in file order
	window     acceptanceWindow
	verifiable bool // the WHOLE ledger can be trusted
	errs       []string
	problems   []acceptanceProblem
	total      int
}

// loadAcceptanceState reads and validates the acceptance ledger.
//
// It is strictly fail-closed along the same axes as P41/P42 (I2, T243):
//   - an unclassifiable line fails the whole load (skipping is how a tampered
//     prefix disappears);
//   - an entry whose KAK signature does not verify, whose digest does not
//     match its canonical payload, or whose prev pointer is broken POISONS the
//     whole ledger: a partially trusted ledger is not trusted ("rotten ledger"
//     discipline — never silently claimed intact, ADR-065 A4);
//   - a second line for one entry_seq is a conflict (I3/T244) and poisons it;
//   - the FIRST surviving entry is exempt from the prev check (I4): after a
//     legal prefix compaction it points at an entry that is legitimately gone.
func loadAcceptanceState(path string, ka *keyAuthority) (*acceptanceState, error) {
	st := &acceptanceState{verifiable: true}
	if ka == nil || !ka.verifiable() {
		return nil, errors.New("acceptance: the ledger cannot be verified (no usable KAK trust anchor)")
	}
	lines, ok, err := readLogLines(path, acceptanceGroupOf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("acceptance ledger holds %d unclassifiable line(s); refusing to evaluate it", countUnclassified(lines))
	}
	if len(lines) == 0 {
		return st, nil
	}

	prevDigest := ""
	seenEntry := map[int64]bool{}
	seqs := make([]int64, 0, len(lines))
	for i := range lines {
		var e acceptanceEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			// Unreachable: the classifier already parsed this line.
			return nil, fmt.Errorf("acceptance line %d is not readable: %w", i+1, jerr)
		}
		st.total++
		seqs = append(seqs, e.EntrySeq)

		if seenEntry[e.EntrySeq] {
			st.verifiable = false
			st.problems = append(st.problems, acceptanceProblem{
				EntrySeq: e.EntrySeq, Verdict: acceptanceVerdictConflict,
				Detail: "a second line for the same entry_seq is a conflict (an acceptance entry is a fact, not a state)",
			})
			st.errs = append(st.errs, fmt.Sprintf("entry_seq %d: a second line for the same entry_seq is a conflict", e.EntrySeq))
			prevDigest = ""
			continue
		}
		seenEntry[e.EntrySeq] = true

		if v := verifyAcceptanceEntrySignature(&e, ka.trust); v.Verdict != sigVerdictOK {
			st.verifiable = false
			st.problems = append(st.problems, acceptanceProblem{EntrySeq: e.EntrySeq, Verdict: v.Verdict, Detail: v.Detail})
			st.errs = append(st.errs, fmt.Sprintf("entry_seq %d: %s (%s)", e.EntrySeq, v.Verdict, v.Detail))
			prevDigest = ""
			continue
		}
		dg, derr := acceptanceEntryDigest(&e)
		if derr != nil || dg != e.EntryDigest {
			st.verifiable = false
			st.problems = append(st.problems, acceptanceProblem{
				EntrySeq: e.EntrySeq, Verdict: destructionVerdictChainBroken,
				Detail: "entry_digest does not match its canonical payload",
			})
			st.errs = append(st.errs, fmt.Sprintf("entry_seq %d: entry_digest does not match its canonical payload", e.EntrySeq))
			prevDigest = ""
			continue
		}
		// I4: only the FIRST surviving entry's incoming pointer is exempt — a
		// legal prefix compaction legitimately removed what it pointed at.
		if i > 0 && e.PrevEntryDigest != prevDigest {
			st.verifiable = false
			st.problems = append(st.problems, acceptanceProblem{
				EntrySeq: e.EntrySeq, Verdict: destructionVerdictChainBroken,
				Detail: "prev_entry_digest breaks the hash chain",
			})
			st.errs = append(st.errs, fmt.Sprintf("entry_seq %d: prev_entry_digest breaks the hash chain", e.EntrySeq))
			prevDigest = ""
			continue
		}
		prevDigest = e.EntryDigest
		st.entries = append(st.entries, e)
	}

	st.window.MinEntry, st.window.MaxEntry = seqs[0], seqs[0]
	st.window.Entries = len(seqs)
	st.window.Continuous = true
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			st.window.Continuous = false
		}
		if seqs[i] < st.window.MinEntry {
			st.window.MinEntry = seqs[i]
		}
		if seqs[i] > st.window.MaxEntry {
			st.window.MaxEntry = seqs[i]
		}
	}
	if !st.window.Continuous {
		st.verifiable = false
		st.errs = append(st.errs, "window_discontinuous: the surviving entry_seq run has a hole — the ledger cannot be trusted")
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// The recorder — the write side (ADR-066 §3)
// ---------------------------------------------------------------------------

// acceptanceRecorder owns the acceptance ledger. It is constructed by the
// scheduler (which holds the KAK) and injected into the FileBackedTransitionStore
// at construction time; the store calls record() INSIDE its s.mu critical
// section, after the durable Sync and the watermark advance (record-first).
//
// Concurrency discipline (I6 / P43 lessons, ADR-066 §3):
//   - mu (acceptanceMu) covers read→append→compaction as ONE critical section;
//     the concurrent domain is the store's single-writer Append plus the
//     scheduler's Status/View reads (which never write);
//   - NO entry_seq watermark file: the seq is derived from the ledger's own
//     maximum inside this critical section (R41-6);
//   - duplicate detection SCANS the ledger (a memory-only check would let a
//     crashed-and-retried append land twice);
//   - ZERO network I/O under any lock: the acceptance-anchor dispatch is
//     COLLECTED via onAnchor (B1) and the compaction's destruction observer is
//     the COLLECT-ONLY variant — the scheduler tick tail does the real
//     dispatch. Witness I/O never holds s.mu / acceptanceMu / destructionWriteMu.
type acceptanceRecorder struct {
	mu       sync.Mutex // acceptanceMu: the ledger's single critical section
	path     string
	capacity int
	ka       *keyAuthority
	// clock is the STORE-side clock for recorded_at (P34-CLOCK-1: never the
	// scheduler's tick clock). Defaults to time.Now; tests may inject.
	clock func() time.Time
	// observe is the COLLECT-ONLY destruction observer for the ledger's own
	// prefix compaction (I5: the ledger is evidence — trimming it is accounted;
	// B1: its anchor dispatch is deferred to the tick tail). nil ⇒ the ledger
	// compacts unobserved exactly as the pre-P43 logs did.
	observe compactionObserver
	// onAnchor, when non-nil (anchoring enabled), COLLECTS the durably appended
	// entry for the acceptance-anchor dispatch. It must be zero-I/O and never
	// block on witness activity (B1).
	onAnchor func(acceptanceEntry)
	// tail is the INCREMENTALLY VERIFIED state of the ledger (final review
	// MAJOR-1): the append path must be O(new bytes), never O(capacity) — a
	// full read+verify of every entry on every Append serialized the whole
	// alert-transition hot path behind O(n) Ed25519 work. The cache holds the
	// file size the verification covers plus the verified chain head; record()
	// verifies only bytes beyond it. A same-size rewrite of already-verified
	// bytes is NOT caught here — it is caught by the full verification at
	// reconcile/compaction time (≤1 tick later), the documented I2 refinement
	// (ADR-066 §3).
	tail acceptanceTailState
}

// acceptanceTailState is the verified-prefix cache. valid=false forces the
// next record() through the full load (construction, shrink, external rewrite).
type acceptanceTailState struct {
	valid      bool
	size       int64
	prevDigest string // EntryDigest of the last verified entry (chain head)
	maxEntry   int64
	entries    int64
}

// record commits one accepted record: seq is the durable record identity, line
// the canonical durable bytes (json.Marshal(newPersisted(seq, t))) the store
// just persisted. Called with the store's s.mu HELD (ADR-066 §3).
//
// Failure semantics: any error leaves the ledger byte-identical (fail-closed)
// and the record persisted-but-unaccepted (loud). Failures are never retried
// and never back-filled (A8-8 — a back-fill would weaken the at-acceptance
// semantics into "whenever we noticed").
func (a *acceptanceRecorder) record(seq int64, line []byte) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ka == nil || !a.ka.writable() {
		return errors.New("acceptance: no key-authority key configured (writes disabled)")
	}

	// Establish the verified tail state. Three cases (final review MAJOR-1 —
	// the append path must be O(new bytes), never O(capacity)):
	//   equal size  ⇒ no new bytes: the cached chain head IS the state;
	//   grew        ⇒ verify ONLY the suffix bytes, continuing the cached chain;
	//   shrank/miss ⇒ full load (construction, compaction rewrite, external
	//                 rewrite) — the expensive path, taken rarely by design.
	var prevDigest string
	var maxEntry, entries int64
	fi, serr := os.Stat(a.path)
	switch {
	case serr != nil && !os.IsNotExist(serr):
		return serr
	case serr != nil && os.IsNotExist(serr):
		// An absent ledger is an empty one: genesis starts fresh.
		prevDigest, maxEntry, entries = "", 0, 0
		a.tail = acceptanceTailState{valid: true, size: 0, prevDigest: "", maxEntry: 0, entries: 0}
	case a.tail.valid && fi.Size() == a.tail.size:
		prevDigest, maxEntry, entries = a.tail.prevDigest, a.tail.maxEntry, a.tail.entries
	case a.tail.valid && fi.Size() > a.tail.size:
		var nerr error
		prevDigest, maxEntry, entries, nerr = a.verifySuffix(a.tail, fi.Size())
		if nerr != nil {
			return nerr
		}
		a.tail.size = fi.Size()
	default:
		lines, ok, err := readLogLines(a.path, acceptanceGroupOf)
		if err != nil {
			return err
		}
		if !ok {
			// T243: a ledger we cannot fully evaluate is refused — rebuilding a
			// clean-looking file around lines we cannot read is exactly the
			// laundering this discipline exists to prevent. The file (including
			// the unreadable evidence) stays byte-identical.
			return fmt.Errorf("acceptance: ledger holds %d unclassifiable line(s); refusing to append", countUnclassified(lines))
		}
		seenEntry := map[int64]bool{}
		for i := range lines {
			var e acceptanceEntry
			if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
				return fmt.Errorf("acceptance line %d is not readable: %w", i+1, jerr)
			}
			// I3 (append-side guard): a second line for one entry_seq is a
			// conflict — never a fix-up, even with an identical payload (T244).
			if seenEntry[e.EntrySeq] {
				return fmt.Errorf("acceptance: refusing to append — entry_seq %d already carries a second line (conflict)", e.EntrySeq)
			}
			seenEntry[e.EntrySeq] = true
			if v := verifyAcceptanceEntrySignature(&e, a.ka.trust); v.Verdict != sigVerdictOK {
				return fmt.Errorf("acceptance: refusing to append — entry_seq %d is %s (%s)", e.EntrySeq, v.Verdict, v.Detail)
			}
			dg, derr := acceptanceEntryDigest(&e)
			if derr != nil || dg != e.EntryDigest {
				return fmt.Errorf("acceptance: refusing to append — entry_seq %d entry_digest does not match its canonical payload", e.EntrySeq)
			}
			// I4: the first surviving entry's incoming pointer is exempt.
			if i > 0 && e.PrevEntryDigest != prevDigest {
				return fmt.Errorf("acceptance: refusing to append — entry_seq %d breaks the hash chain", e.EntrySeq)
			}
			prevDigest = e.EntryDigest
			if e.EntrySeq > maxEntry {
				maxEntry = e.EntrySeq
			}
		}
		entries = int64(len(seenEntry))
		a.tail = acceptanceTailState{valid: true, size: fi.Size(), prevDigest: prevDigest, maxEntry: maxEntry, entries: entries}
	}

	// I7 (ADR-066 §3): boundedness is the OBSERVED compaction — never an
	// unaccounted trim (R43-4). The compaction runs BEFORE the append, so a
	// REFUSED compaction refuses the NEW entry (the record stays persisted but
	// unaccepted — loud, T256) and the ledger never exceeds capacity. There is
	// deliberately no "full" state otherwise: the ledger is bounded by rotating
	// through accounting (A8-6).
	if a.capacity > 0 && entries+1 > int64(a.capacity) {
		keep := a.capacity - 1
		if keep <= 0 {
			return fmt.Errorf("acceptance: capacity %d cannot rotate (the shared compaction primitive always keeps at least one group); refusing the new entry (I7)", a.capacity)
		}
		if cerr := compactLogPrefixGroupsObserved(a.path, keep, acceptanceGroupOf, a.observe); cerr != nil {
			return fmt.Errorf("acceptance: compaction refused (%v) — new acceptance entries are refused until accounting recovers (I7)", cerr)
		}
		// The rewrite kept the newest `keep` groups verbatim: the chain head and
		// max entry are UNCHANGED; only the file size and the group count move
		// (the shared primitive's verbatim-copy guarantee, T124d). The next full
		// verification (reconcile / compaction) re-derives them anyway.
		if fi2, serr2 := os.Stat(a.path); serr2 == nil {
			a.tail.size = fi2.Size()
		}
		entries = int64(keep)
		a.tail.entries = int64(keep)
	}

	e := acceptanceEntry{
		V:               1,
		EntrySeq:        maxEntry + 1, // no watermark: derived from the ledger max, in this section (R41-6)
		RecordSeq:       seq,
		RecordDigest:    acceptanceDigestOfLine(line),
		RecordedAt:      a.clock().UTC().Format(time.RFC3339Nano),
		AuthorityKeyID:  a.ka.signer.keyID,
		PrevEntryDigest: prevDigest,
	}
	dg, derr := acceptanceEntryDigest(&e)
	if derr != nil {
		return derr
	}
	e.EntryDigest = dg
	if serr := a.ka.signer.signAcceptanceEntry(&e, a.clock()); serr != nil {
		return serr
	}
	raw, merr := serializeAcceptanceEntryBytes(&e)
	if merr != nil {
		return merr
	}
	if aerr := appendLogLine(a.path, raw); aerr != nil {
		return aerr
	}
	// The cache follows the append: the chain head is this entry, the size the
	// post-append stat. From here the next record() takes the equal-size fast
	// path (or the suffix path if someone else grew the file).
	if fi3, serr3 := os.Stat(a.path); serr3 == nil {
		a.tail.size = fi3.Size()
	}
	a.tail.prevDigest = e.EntryDigest
	a.tail.maxEntry = e.EntrySeq
	a.tail.entries = entries + 1
	a.tail.valid = true
	// B1: the anchor dispatch is COLLECTED here (zero I/O, never blocking on
	// witness activity) and performed by the scheduler tick tail.
	if a.onAnchor != nil {
		a.onAnchor(e)
	}
	return nil
}

// verifySuffix classifies and verifies ONLY the bytes beyond a verified prefix
// (final review MAJOR-1): every line must be classifiable, carry the NEXT
// entry_seq (a seq at or below the cached max is the append-side I3 conflict —
// externally appended duplicates are refused here), verify against the KAK
// trust, match its canonical payload digest, and continue the cached chain
// (the first suffix line's prev must equal the cached head — exempt only when
// the cached ledger was empty, i.e. this suffix starts the genesis).
// Any failure refuses the append with the file byte-identical (I2 fail-closed).
func (a *acceptanceRecorder) verifySuffix(tail acceptanceTailState, upto int64) (string, int64, int64, error) {
	f, err := os.Open(a.path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	if _, serr := f.Seek(tail.size, io.SeekStart); serr != nil {
		return "", 0, 0, serr
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	prev, maxEntry, entries := tail.prevDigest, tail.maxEntry, tail.entries
	first := tail.entries == 0 // genesis: the very first entry's prev is exempt
	for scanner.Scan() {
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		var e acceptanceEntry
		if jerr := json.Unmarshal(trimmed, &e); jerr != nil {
			return "", 0, 0, fmt.Errorf("acceptance: refusing to append — suffix line is not readable (%w); the ledger stays byte-identical", jerr)
		}
		if e.EntrySeq <= maxEntry {
			return "", 0, 0, fmt.Errorf("acceptance: refusing to append — suffix entry_seq %d already exists (conflict)", e.EntrySeq)
		}
		if v := verifyAcceptanceEntrySignature(&e, a.ka.trust); v.Verdict != sigVerdictOK {
			return "", 0, 0, fmt.Errorf("acceptance: refusing to append — suffix entry_seq %d is %s (%s)", e.EntrySeq, v.Verdict, v.Detail)
		}
		dg, derr := acceptanceEntryDigest(&e)
		if derr != nil || dg != e.EntryDigest {
			return "", 0, 0, fmt.Errorf("acceptance: refusing to append — suffix entry_seq %d entry_digest does not match its canonical payload", e.EntrySeq)
		}
		if !first && e.PrevEntryDigest != prev {
			return "", 0, 0, fmt.Errorf("acceptance: refusing to append — suffix entry_seq %d breaks the hash chain", e.EntrySeq)
		}
		first = false
		prev = e.EntryDigest
		maxEntry = e.EntrySeq
		entries++
	}
	if serr := scanner.Err(); serr != nil {
		return "", 0, 0, serr
	}
	return prev, maxEntry, entries, nil
}

// ---------------------------------------------------------------------------
// Scheduler wiring — the two collect-only sources and their tick-tail drains
// ---------------------------------------------------------------------------

// acceptancePendingMu guards the scheduler-level queue of acceptance entries
// awaiting their anchor dispatch. Deliberately NOT destructionDispatchMu: the
// enqueue happens under the store's s.mu + acceptanceMu (via the recorder's
// zero-I/O hook) and must never wait on witness dispatch activity (B1).
var acceptancePendingMu sync.Mutex

// enqueueAcceptanceAnchor is the recorder's onAnchor hook: zero-I/O collect.
func (s *HistoryExportScheduler) enqueueAcceptanceAnchor(e acceptanceEntry) {
	acceptancePendingMu.Lock()
	s.acceptancePending = append(s.acceptancePending, e)
	acceptancePendingMu.Unlock()
}

// dispatchAcceptancePending (ADR-066 §3: "scheduler tick 尾部
// dispatchAcceptancePending() 统一派发") performs the REAL acceptance-anchor
// dispatch at the tick tail, holding NO store lock. Two sources, unified:
//
//  1. durable pendings recovered from acceptance-anchor.jsonl — a crash after
//     the pending line was written but before the witness answered (the same
//     backlog discipline as anchorHousekeeping, on the fifth stream);
//  2. the in-memory queue the recorder collected during Appends (B1).
//
// The sweep runs BEFORE the queue drain: an entry is either in the durable log
// (pre-crash) or in this process's queue — never both.
func (s *HistoryExportScheduler) dispatchAcceptancePending() {
	if !s.anchorEnabled() {
		return
	}
	path := acceptanceAnchorPath(s.cfg.Dir)
	st, err := loadAnchorStatePath(path, s.cfg.Dir, s.trust)
	if err != nil {
		s.setAnchorError(err.Error())
	} else {
		for _, seq := range st.seqs() {
			e := st.latest[seq]
			if e.State != anchorStatePending {
				continue
			}
			if e.Attempts >= s.anchorMaxAttempts() {
				final := e
				final.State = anchorStateUnanchored
				final.LastError = fmt.Sprintf("attempts exhausted (%d)", e.Attempts)
				if aerr := appendAnchorEntryPath(path, s.cfg.AnchorCapacity, final); aerr != nil {
					s.setAnchorError(aerr.Error())
				}
				continue
			}
			if derr := s.dispatchAnchorPath(context.Background(), path, e); derr != nil {
				s.setAnchorError(derr.Error())
			}
		}
	}
	acceptancePendingMu.Lock()
	q := s.acceptancePending
	s.acceptancePending = nil
	acceptancePendingMu.Unlock()
	for _, e := range q {
		if aerr := s.anchorAcceptanceEntry(e); aerr != nil {
			s.setAnchorError(aerr.Error())
		}
	}
}

// collectOnlyDestructionConfig is the B1 variant of the destruction config for
// the acceptance ledger's compaction observation point: identical ACCOUNTING
// (destructionWriteMu, destruction-log.jsonl writes) but the anchor EXIT is
// replaced by an ENQUEUER. The enqueuer runs inside dispatchDestructionPending
// — i.e. with destructionWriteMu already released and destructionDispatchMu
// already held — so the queue append is zero-I/O, never nested with
// destructionWriteMu, and automatically serialized against the tick-tail drain.
func (s *HistoryExportScheduler) collectOnlyDestructionConfig() destructionConfig {
	c := s.destructionConfig()
	if s.anchorEnabled() {
		c.anchorDispatch = func(e destructionEntry) error {
			s.destructionAnchorQueue = append(s.destructionAnchorQueue, e)
			return nil
		}
	}
	return c
}

// acceptanceCompactionObserver is the destruction observer for the acceptance
// ledger's own prefix compaction (I5): the ledger is EVIDENCE, so trimming its
// prefix is a destruction that must be accounted for. It is the COLLECT-ONLY
// variant (B1, review major fix): the observation point runs under the store's
// s.mu and acceptanceMu, so the complete closure must never dispatch to the
// witness inline — the terminal entry lands in the scheduler queue and the
// tick tail drains it. Returns an error to REFUSE the compaction (a prefix we
// cannot account for is not destroyed — the shared primitive's contract).
func (s *HistoryExportScheduler) acceptanceCompactionObserver() compactionObserver {
	if s == nil || !s.destructionEnabled() {
		return nil
	}
	return func(path string, droppedGroups []int64, droppedLines []logLine) (func() error, error) {
		c := s.collectOnlyDestructionConfig()
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
		// destroyed_count for the log face = the actual number of dropped
		// LINES (review minor 9 lineage — a range target is one target but
		// many destroyed lines).
		intent, berr := beginDestruction(c, destructionKindAcceptanceCompaction, fmt.Sprintf("capacity=%d", c.capacity), nil, targets, len(droppedLines), at)
		if berr != nil {
			s.setDestructionError(berr.Error())
			return nil, berr
		}
		return func() error {
			// The anchor dispatch of the COMPLETED record is COLLECTED inside
			// completeDestruction via the collect-only hook; the tick tail's
			// drainDestructionAnchorQueue performs the real dispatch.
			if cerr := completeDestruction(c, intent.DestructionSeq, destructionStateCompleted, s.clock()); cerr != nil {
				s.setDestructionError(cerr.Error())
				return cerr
			}
			return nil
		}, nil
	}
}

// drainDestructionAnchorQueue (B1) performs the REAL witness dispatch for the
// destruction records whose observation point was collect-only (the acceptance
// ledger's compaction). Runs at the scheduler tick tail, holding NO store lock;
// destructionDispatchMu serializes with every other destruction-anchor
// dispatcher (the established order destructionDispatchMu → scheduler.mu via
// setDestructionError; never the reverse — no path takes scheduler.mu and then
// destructionDispatchMu).
func (s *HistoryExportScheduler) drainDestructionAnchorQueue() {
	if !s.anchorEnabled() {
		return
	}
	destructionDispatchMu.Lock()
	q := s.destructionAnchorQueue
	s.destructionAnchorQueue = nil
	for _, e := range q {
		if aerr := s.anchorDestructionEntry(e); aerr != nil {
			s.setDestructionError(aerr.Error())
		}
	}
	destructionDispatchMu.Unlock()
}

// ---------------------------------------------------------------------------
// The reconciliation — InputIntegrityView (ADR-066 §5)
// ---------------------------------------------------------------------------

// InputIntegrityView is the derived, strictly read-only projection. It has
// ZERO side effects: it writes nothing, compacts nothing, never repairs.
type InputIntegrityView struct {
	Enabled              bool                `json:"enabled"`
	Verdict              string              `json:"verdict"`
	TrustReason          string              `json:"trust_reason,omitempty"`
	Window               *acceptanceWindow   `json:"acceptance_window,omitempty"` // ALWAYS output when the ledger is non-empty (T251)
	WindowDiscontinuous  bool                `json:"window_discontinuous,omitempty"`
	CoverageFloor        int64               `json:"coverage_floor"`
	ModifiedIDs          []int64             `json:"modified_ids"`
	MissingInSpanIDs     []int64             `json:"missing_in_span_ids"`
	UnacceptedIDs        []int64             `json:"unaccepted_ids"`
	OutsideSpanCount     int                 `json:"outside_span_count"`
	OutsideCoverageCount int                 `json:"outside_coverage_count"`
	Problems             []acceptanceProblem `json:"problems,omitempty"`
	Reason               string              `json:"reason,omitempty"`
	InputError           string              `json:"input_error,omitempty"`
}

func (s *HistoryExportScheduler) inputIntegrityEnabled() bool {
	return s != nil && s.cfg.AcceptanceLog
}

// InputIntegrityView reconciles the store snapshot against the acceptance
// ledger (ADR-066 §5).
//
// I10 (the load-bearing ORDER, review n2): the acceptance ledger is loaded
// FIRST, the store snapshot SECOND. A record accepted between the two reads
// lands in the snapshot WITHOUT a ledger entry ⇒ `record_unaccepted` — the
// honest concurrent-append normal state (A8-3). The reverse order would read a
// concurrent append as a deletion: ledger entry seq > snapshot MaxSeq ⇒ a
// FALSE `record_missing_in_span`. The order is the wall that keeps concurrency
// honest.
func (s *HistoryExportScheduler) InputIntegrityView(ctx context.Context) InputIntegrityView {
	out := InputIntegrityView{
		Verdict:          inputVerdictAbsent,
		ModifiedIDs:      []int64{},
		MissingInSpanIDs: []int64{},
		UnacceptedIDs:    []int64{},
	}
	if !s.inputIntegrityEnabled() {
		return out
	}
	out.Enabled = true

	st, err := loadAcceptanceState(acceptanceLogPath(s.cfg.Dir), s.keyAuthority)
	if err != nil {
		out.Verdict = inputVerdictUnverifiable
		out.TrustReason = inputTrustLedgerUnverifiable
		out.InputError = err.Error()
		return out
	}
	if st.total > 0 {
		w := st.window
		out.Window = &w
		if !st.window.Continuous {
			out.WindowDiscontinuous = true
		}
	}
	if st.total > 0 && !st.verifiable {
		// A ledger we cannot trust proves nothing: never silently read as
		// intact, never input_ok (T243/T244/T251 fail-closed family).
		out.Verdict = inputVerdictUnverifiable
		out.TrustReason = inputTrustLedgerUnverifiable
		out.Problems = st.problems
		out.InputError = strings.Join(st.errs, "; ")
		return out
	}
	if st.total == 0 {
		// Enabled but the ledger is absent or empty: locally indistinguishable
		// from "nothing was ever accepted" — stated honestly (A8-7), never
		// dressed up as input_ok.
		out.Verdict = inputVerdictAbsent
		out.Reason = "the acceptance ledger is absent or empty: no record was ever accepted (or the ledger was deleted — locally indistinguishable)"
		return out
	}

	snap := s.cfg.Store.ReadAll(ctx)
	if snap.LoadErr != nil {
		out.Verdict = inputVerdictUnverifiable
		out.TrustReason = inputTrustStoreUnverifiable
		out.InputError = "store read: " + snap.LoadErr.Error()
		return out
	}
	if snap.Corrupt {
		// The snapshot is not trustworthy ⇒ never input_ok (T255, review F3).
		out.Verdict = inputVerdictUnverifiable
		out.TrustReason = inputTrustStoreUnverifiable
		out.InputError = "durable transition history is corrupt — the snapshot cannot be reconciled (never input_ok)"
		return out
	}
	if len(snap.Seqs) != len(snap.Transitions) {
		out.Verdict = inputVerdictUnverifiable
		out.TrustReason = inputTrustStoreUnverifiable
		out.InputError = "store snapshot lacks per-record seq alignment — reconciliation is impossible"
		return out
	}

	ledger := make(map[int64]string, len(st.entries))
	for _, e := range st.entries {
		ledger[e.RecordSeq] = e.RecordDigest
	}
	// coverage_floor (A7-4): the first accepted record's seq minus one. Every
	// snapshot record BEFORE it predates enablement and is forever
	// outside_coverage — never unaccepted.
	coverageFloor := st.entries[0].RecordSeq - 1
	out.CoverageFloor = coverageFloor

	snapHas := make(map[int64]bool, len(snap.Seqs))
	for i, rec := range snap.Transitions {
		seq := snap.Seqs[i]
		snapHas[seq] = true
		line, merr := acceptanceCanonicalRecordBytes(seq, rec)
		if merr != nil {
			out.Verdict = inputVerdictUnverifiable
			out.TrustReason = inputTrustStoreUnverifiable
			out.InputError = "canonical rebuild: " + merr.Error()
			return out
		}
		d := acceptanceDigestOfLine(line)
		if dg, ok := ledger[seq]; ok {
			if dg != d {
				// ★ the core payoff (T239): the durable bytes no longer match
				// what the system accepted.
				out.ModifiedIDs = append(out.ModifiedIDs, seq)
			}
		} else if seq <= coverageFloor {
			out.OutsideCoverageCount++
		} else {
			out.UnacceptedIDs = append(out.UnacceptedIDs, seq)
		}
	}
	for seq := range ledger {
		switch {
		case seq < snap.MinSeq:
			// R40-1 boundary discipline: raising MinSeq requires deleting the
			// OLDEST record, which is indistinguishable from a legal ring
			// eviction — never asserted (T241/T250).
			out.OutsideSpanCount++
		case seq > snap.MaxSeq:
			// There is NO legal upper-bound eviction: deleting the newest
			// record (or rolling the file back) is assertable (T254, review F2).
			out.MissingInSpanIDs = append(out.MissingInSpanIDs, seq)
		case !snapHas[seq]:
			// Inside the span, paired by the pairing loop's absence: assertable
			// deletion (T240).
			out.MissingInSpanIDs = append(out.MissingInSpanIDs, seq)
		}
	}
	sort.Slice(out.ModifiedIDs, func(i, j int) bool { return out.ModifiedIDs[i] < out.ModifiedIDs[j] })
	sort.Slice(out.MissingInSpanIDs, func(i, j int) bool { return out.MissingInSpanIDs[i] < out.MissingInSpanIDs[j] })
	sort.Slice(out.UnacceptedIDs, func(i, j int) bool { return out.UnacceptedIDs[i] < out.UnacceptedIDs[j] })

	switch {
	case len(out.ModifiedIDs) > 0 || len(out.MissingInSpanIDs) > 0:
		out.Verdict = inputVerdictModified
	case len(out.UnacceptedIDs) > 0:
		out.Verdict = inputVerdictIncomplete
	default:
		out.Verdict = inputVerdictOK
	}
	return out
}

// ---------------------------------------------------------------------------
// Read route (admin-only, :8082) — ADR-066 §5
// ---------------------------------------------------------------------------

// handleHistoryExportInputIntegrity serves
// GET /management/v1/protection/alerts/history/export/input-integrity — the
// derived, strictly read-only view. It answers 503 when the Phase is off: a
// disabled input integrity is never dressed up as an empty success.
func (s *Server) handleHistoryExportInputIntegrity(w http.ResponseWriter, r *http.Request) {
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
	if !s.historyScheduler.inputIntegrityEnabled() {
		writeError(w, http.StatusServiceUnavailable, "input integrity is not enabled (configure --export-acceptance-log with --export-key-authority and --export-key-authority-trust)")
		return
	}
	writeJSON(w, http.StatusOK, s.historyScheduler.InputIntegrityView(r.Context()))
}

// inputIntegrityStatusSummary is the roll-up on the scheduler status face: it
// carries the sticky acceptance-recording failure (input_error, ADR-066 §3).
type inputIntegrityStatusSummary struct {
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
}
