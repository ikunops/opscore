package server

// Phase 41 — Signing Key Lifecycle: time-bounded trust (ADR-054 scope,
// ADR-055 architecture).
//
// The problem it solves: Phase 37's trust anchor answers "WHO signed this" and
// nothing else. `exportTrustStore` is a static map key_id -> public key with no
// time dimension, and `signatureBlock.SignedAt` is only ever checked for being
// non-empty — it is never compared against anything. A leaked signing key
// therefore presents an impossible choice: keeping the key in the trust set
// preserves the history but leaves the forgery power standing, while removing
// it revokes the forgery power and turns EVERY historical signature into
// `key_unknown`, which zeroes out the whole chain's verifiability. Both halves
// of that dilemma come from compressing "who" and "when" into one bit.
//
// This Phase splits them. The trust set keeps answering WHO (public keys are
// never removed from it), and an append-only, hash-chained, KAK-authorized
// ledger answers WHEN. The ledger makes three previously impossible verdicts
// assertable:
//
//   signature_before_activation — signed before the key was ever authorized
//   signature_after_rotation    — signed after the key was rotated out
//   signature_after_revocation  — signed after the key was revoked  ← the payoff
//
// and a fourth one closes the interval check's bypass (R41-7):
//
//   signature_time_unparseable  — the time declaration itself is defective, so
//                                 no interval can be evaluated and the
//                                 signature must not quietly stay `ok`.
//
// Division of labour across Phases, stated once:
//
//	P37 decides WHO, P40 decides WHERE, P41 decides WHEN.
//
// Frozen boundaries (ADR-055 §11, honoured here):
//   - P37's five verdicts, P38's chain, P39's ledger, P40's anchor semantics and
//     P35/P36 surfaces are READ but never modified;
//   - `verifyManifestSignature` is untouched — the new step runs AFTER it;
//   - `appendonly_log.go` is reused unchanged (one persistence discipline);
//   - with no KAK configured nothing is written and no output field appears.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// keyLifecycleFile lives in the export directory, next to the manifest, the
// chain ledger and the anchor log (ADR-055 §1 / R41-3 — same domain, never
// fanned out).
const keyLifecycleFile = "signing-key-log.jsonl"

// The three lifecycle event types (ADR-055 §3).
const (
	lifecycleEventActivated  = "activated"
	lifecycleEventRotatedOut = "rotated_out"
	lifecycleEventRevoked    = "revoked"
)

// The verdicts this Phase adds to the Phase 37 set. They are INDEPENDENTLY
// named (never folded into `invalid` / `key_unknown`) but every one of them
// degrades the status by at most `mismatch` — loudness lives in the status,
// category lives in the name (R41-1).
const (
	sigVerdictBeforeActivation = "signature_before_activation"
	sigVerdictAfterRotation    = "signature_after_rotation"
	sigVerdictAfterRevocation  = "signature_after_revocation"
	// sigVerdictTimeUnparseable (R41-7) is the bypass-closing fourth verdict:
	// an unparseable `signed_at` is a DEFECT IN THE TIME DECLARATION, which is
	// structural, deterministic and needs no guessing — unlike a broken chain,
	// where the P38 discipline (do not sort, do not guess) correctly applies.
	// A legal signing path always emits time.RFC3339Nano, so an unparseable
	// value is never a legitimate product and never a false positive.
	sigVerdictTimeUnparseable = "signature_time_unparseable"
)

// Where an authorization interval came from (ADR-055 §4).
const (
	lifecycleSourceLedger    = "lifecycle_ledger"
	lifecycleSourceUnbounded = "unbounded"
)

// keyLifecycleEntry is one immutable fact about a signing key's authorization.
//
// An event is a FACT, not a state (ADR-055 §2.3): once a line is on disk it is
// final. This is the deliberate opposite of the Phase 40 anchor entry, which
// models a DELIVERY STATE and may legally advance pending -> anchored. Allowing
// "the same fact twice" here would only open a replay-washing door, so a second
// line for the same event_seq is always a conflict.
type keyLifecycleEntry struct {
	V                 int             `json:"v"`                           // 1
	EventSeq          int64           `json:"event_seq"`                   // strictly monotone, group key
	EventType         string          `json:"event_type"`                  // activated | rotated_out | revoked
	KeyID             string          `json:"key_id"`                      // P37 derived, never configured (who)
	PubkeyFingerprint string          `json:"pubkey_fingerprint"`          // SHA-256(raw pub), binds who
	NotBefore         string          `json:"not_before,omitempty"`        // RFC3339Nano, activated only
	NotAfter          string          `json:"not_after,omitempty"`         // RFC3339Nano, rotated_out / revoked
	RecordedAt        string          `json:"recorded_at"`                 // self-asserted; never an authority
	AuthorityKeyID    string          `json:"authority_key_id"`            // KAK key id (P37 derivation)
	StreamID          string          `json:"stream_id"`                   // P40 derived (R40-2), never configured (where)
	PrevEventDigest   string          `json:"prev_event_digest,omitempty"` // empty for genesis — the ONLY legal empty prev
	EventDigest       string          `json:"event_digest"`                // sha256(canonicalLifecyclePayload)
	Signature         *signatureBlock `json:"signature,omitempty"`         // KAK signature; P37 block reused verbatim
}

// keyLifecycleSigned is the exact structure canonicalLifecyclePayload
// serializes: the entry minus {event_digest, signature}.
type keyLifecycleSigned struct {
	V                 int    `json:"v"`
	EventSeq          int64  `json:"event_seq"`
	EventType         string `json:"event_type"`
	KeyID             string `json:"key_id"`
	PubkeyFingerprint string `json:"pubkey_fingerprint"`
	NotBefore         string `json:"not_before,omitempty"`
	NotAfter          string `json:"not_after,omitempty"`
	RecordedAt        string `json:"recorded_at"`
	AuthorityKeyID    string `json:"authority_key_id"`
	StreamID          string `json:"stream_id"`
	PrevEventDigest   string `json:"prev_event_digest,omitempty"`
}

func keyLifecycleSignedFields(e *keyLifecycleEntry) keyLifecycleSigned {
	return keyLifecycleSigned{
		V:                 e.V,
		EventSeq:          e.EventSeq,
		EventType:         e.EventType,
		KeyID:             e.KeyID,
		PubkeyFingerprint: e.PubkeyFingerprint,
		NotBefore:         e.NotBefore,
		NotAfter:          e.NotAfter,
		RecordedAt:        e.RecordedAt,
		AuthorityKeyID:    e.AuthorityKeyID,
		StreamID:          e.StreamID,
		PrevEventDigest:   e.PrevEventDigest,
	}
}

// canonicalLifecyclePayload returns the exact bytes the KAK signature covers.
// It is the same one canonical serializer family as Phase 37's manifest payload
// and Phase 40's anchor payload — a second serialization would drift, and drift
// here silently changes what the evidence means.
func canonicalLifecyclePayload(e *keyLifecycleEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil key lifecycle entry")
	}
	return json.Marshal(keyLifecycleSignedFields(e))
}

// lifecycleEventDigest is the per-event commitment: sha256(canonical payload).
func lifecycleEventDigest(e *keyLifecycleEntry) (string, error) {
	payload, err := canonicalLifecyclePayload(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func serializeKeyLifecycleEntryBytes(e *keyLifecycleEntry) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func keyLifecycleLogPath(dir string) string { return filepath.Join(dir, keyLifecycleFile) }

// keyLifecycleGroupOf is the classifier this log injects into the shared
// append-only primitive: one `event_seq` is ONE group (ADR-055 §2.3).
func keyLifecycleGroupOf(raw []byte) (int64, bool) {
	var e keyLifecycleEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return e.EventSeq, true
}

// ---------------------------------------------------------------------------
// KAK signing / verification
// ---------------------------------------------------------------------------

// keyAuthority is the offline authority that signs lifecycle events. It is
// deliberately a DIFFERENT key from the manifest signing key: if the KAK were
// the signing key, the authorization chain would collapse into self-assertion
// (ADR-055 §7.1) and revocation would mean nothing.
type keyAuthority struct {
	signer *exportSigner     // KAK private key; nil ⇒ the ledger cannot be written
	trust  *exportTrustStore // KAK public keys; nil ⇒ the ledger cannot be verified
}

// writable reports whether new events can be appended. Both halves are needed:
// a KAK we can sign with but not verify would build a ledger nobody can check.
func (ka *keyAuthority) writable() bool { return ka != nil && ka.signer != nil && ka.trust != nil }

// verifiable reports whether the ledger can be evaluated at all.
func (ka *keyAuthority) verifiable() bool {
	return ka != nil && ka.trust != nil && len(ka.trust.keys) > 0
}

// signKeyLifecycleEntry fills the entry's signature block with the KAK
// signature (Ed25519, the same block type Phase 37 uses — no second format).
func (s *exportSigner) signKeyLifecycleEntry(e *keyLifecycleEntry, at time.Time) error {
	if s == nil {
		return errors.New("key authority: no KAK private key configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("key authority: private key is not a valid Ed25519 key")
	}
	if e.Signature == nil {
		e.Signature = &signatureBlock{}
	}
	e.Signature.Alg = signatureAlgEd25519
	e.Signature.KeyID = s.keyID
	e.Signature.SignedAt = at.UTC().Format(time.RFC3339Nano)
	e.Signature.Sig = ""
	payload, err := canonicalLifecyclePayload(e)
	if err != nil {
		return err
	}
	e.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyKeyLifecycleEntrySignature applies the frozen Phase 37 decision order
// to a lifecycle entry, against the KAK trust anchor.
func verifyKeyLifecycleEntrySignature(e *keyLifecycleEntry, trust *exportTrustStore) SignatureVerdict {
	if e == nil || e.Signature == nil {
		return SignatureVerdict{Verdict: sigVerdictAbsent, KeyID: e.KeyID, Detail: "lifecycle event carries no signature"}
	}
	sb := e.Signature
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "lifecycle signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "lifecycle signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted key-authority keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "lifecycle authority_key_id is not in the trusted KAK set"}
	}
	// An event that claims an authority it was not signed by is self-assertion,
	// i.e. the "re-baseline" attack of R41-2 in disguise.
	if e.AuthorityKeyID != "" && e.AuthorityKeyID != sb.KeyID {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "lifecycle authority_key_id disagrees with the signing key id"}
	}
	payload, perr := canonicalLifecyclePayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "lifecycle signature does not verify against the trusted KAK"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// ---------------------------------------------------------------------------
// Configuration bundle — assembled once by the scheduler, passed explicitly so
// every function below stays a pure function of its inputs.
// ---------------------------------------------------------------------------

type keyLifecycleConfig struct {
	dir          string
	capacity     int
	ka           *keyAuthority
	signingTrust *exportTrustStore // P37 trust set: WHO may ever be given a window
	streamID     string            // P40 derived identity of this export directory
	// observe (Phase 43) is the destruction hook this log's prefix compaction
	// installs. nil ⇒ the log compacts exactly as it did in Phase 42.
	observe compactionObserver
}

func (c keyLifecycleConfig) enabled() bool  { return c.ka != nil && c.ka.verifiable() }
func (c keyLifecycleConfig) writable() bool { return c.ka != nil && c.ka.writable() }

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// lifecycleWindow is the surviving contiguous run of event_seq values. It is
// the same discipline R40-1 introduced for the anchor log, instantiated here:
// compaction is prefix-only, so everything below `min` may legally be gone and
// must never be read as "never existed".
type lifecycleWindow struct {
	MinSeq     int64 `json:"min_seq"`
	MaxSeq     int64 `json:"max_seq"`
	Entries    int   `json:"entries"`
	Continuous bool  `json:"continuous"`
}

// keyLifecycleState is the read-only view the verifier works from. Building it
// never writes, repairs or compacts.
type keyLifecycleState struct {
	entries    []keyLifecycleEntry // verified, in file order
	byKey      map[string][]keyLifecycleEntry
	window     lifecycleWindow
	verifiable bool     // the whole ledger can be trusted (signatures + chain)
	errs       []string // loud reasons the ledger is NOT being used
	total      int
}

// loadKeyLifecycleState reads and validates the ledger.
//
// It is strictly fail-closed along three axes:
//  1. an unclassifiable line makes the load FAIL — there is no
//     skip-and-continue, because skipping is how a tampered prefix disappears;
//  2. an entry whose KAK signature does not verify, whose digest does not match
//     its canonical payload, or whose prev pointer is broken, poisons the whole
//     ledger: a partially trusted ledger is not trusted (the "rotten ledger"
//     known cost, ADR-055 §12-6, is reported loudly instead of being hidden);
//  3. an entry written for another stream (another export directory) is not
//     ours — it poisons the ledger too, because accepting it would silently
//     import a foreign revocation (R41-3).
func loadKeyLifecycleState(c keyLifecycleConfig) (*keyLifecycleState, error) {
	st := &keyLifecycleState{byKey: map[string][]keyLifecycleEntry{}, verifiable: true}
	path := keyLifecycleLogPath(c.dir)
	lines, ok, err := readLogLines(path, keyLifecycleGroupOf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("key lifecycle log holds %d unclassifiable line(s); refusing to evaluate it", countUnclassified(lines))
	}
	if len(lines) == 0 {
		return st, nil
	}

	prevDigest := ""
	var seqs []int64
	for i := range lines {
		var e keyLifecycleEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			// Unreachable: the classifier already parsed this line.
			return nil, fmt.Errorf("key lifecycle line %d is not readable: %w", i+1, jerr)
		}
		st.total++
		seqs = append(seqs, e.EventSeq)

		if v := verifyKeyLifecycleEntrySignature(&e, c.ka.trust); v.Verdict != sigVerdictOK {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("event_seq %d: %s (%s)", e.EventSeq, v.Verdict, v.Detail))
			prevDigest = ""
			continue
		}
		dg, derr := lifecycleEventDigest(&e)
		if derr != nil || dg != e.EventDigest {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("event_seq %d: event_digest does not match its canonical payload", e.EventSeq))
			prevDigest = ""
			continue
		}
		// The hash chain is checked against the PREVIOUS SURVIVING entry. The
		// first surviving entry is deliberately exempt: after a legal prefix
		// compaction it points at an entry that is legitimately gone, and we
		// must not read that as tampering (the R40-1 discipline again).
		if i > 0 && e.PrevEventDigest != prevDigest {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("event_seq %d: prev_event_digest breaks the hash chain", e.EventSeq))
			prevDigest = ""
			continue
		}
		if c.streamID != "" && e.StreamID != "" && e.StreamID != c.streamID {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("event_seq %d: belongs to stream %s, not this export directory (%s)", e.EventSeq, e.StreamID, c.streamID))
			prevDigest = ""
			continue
		}
		prevDigest = e.EventDigest
		st.entries = append(st.entries, e)
		st.byKey[e.KeyID] = append(st.byKey[e.KeyID], e)
	}

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
		st.errs = append(st.errs, "window_discontinuous: the surviving event_seq run has a hole — no interval may be asserted")
	}
	return st, nil
}

// keyAuthorization is the resolved authorization interval of one key.
type keyAuthorization struct {
	KeyID     string `json:"key_id,omitempty"`
	NotBefore string `json:"not_before,omitempty"`
	NotAfter  string `json:"not_after,omitempty"`
	Terminal  string `json:"terminal_event,omitempty"`
	Complete  bool   `json:"complete"`
	Source    string `json:"source"`
	// Evaluable reports whether Phase 41 is in play at all. When it is not, the
	// verify surface must be byte-identical to Phase 40 — no assertion, and no
	// `validity` block either.
	Evaluable bool `json:"-"`
}

func unboundedKeyAuthorization(keyID string) keyAuthorization {
	return keyAuthorization{KeyID: keyID, Source: lifecycleSourceUnbounded}
}

// authorizationFor resolves one key's interval (ADR-055 §4). Pure, zero side
// effects. Every "we cannot know" path returns UNBOUNDED rather than a guess:
// the Phase's entire value is in never asserting what the evidence cannot
// support.
func (st *keyLifecycleState) authorizationFor(keyID string) keyAuthorization {
	if st == nil || !st.verifiable || keyID == "" {
		return unboundedKeyAuthorization(keyID)
	}
	evts := st.byKey[keyID]
	if len(evts) == 0 {
		// A key with no recorded event was never brought under the ledger (it
		// predates enablement). Asserting a window for it would invent history.
		return unboundedKeyAuthorization(keyID)
	}
	a := keyAuthorization{KeyID: keyID, Source: lifecycleSourceUnbounded, Evaluable: true}
	hasActivated := false
	var notAfters []string
	for _, e := range evts {
		switch e.EventType {
		case lifecycleEventActivated:
			hasActivated = true
			if e.NotBefore != "" {
				a.NotBefore = e.NotBefore
			}
		case lifecycleEventRotatedOut, lifecycleEventRevoked:
			if e.NotAfter != "" {
				notAfters = append(notAfters, e.NotAfter)
				// A tie is broken toward the STRONGER semantics: `revoked` names
				// the audit meaning an operator actually cares about.
				if a.Terminal != lifecycleEventRevoked {
					a.Terminal = e.EventType
				}
			}
		}
	}
	if !hasActivated {
		// The activation was compacted away (or never recorded): indistinguishable
		// from "never brought under the ledger". Unbounded — never revoked.
		return keyAuthorization{KeyID: keyID, Source: lifecycleSourceUnbounded, Evaluable: true}
	}
	if len(notAfters) > 0 {
		best := notAfters[0]
		for _, v := range notAfters[1:] {
			if compareRFC3339(v, best) < 0 {
				best = v
			}
		}
		a.NotAfter = best
	}
	// The interval is only assertable when EVERY event of this key is still
	// visible inside the surviving window (ADR-055 §5). If any of them fell out
	// of it, a bound we cannot see might exist — or might not — and the two are
	// indistinguishable.
	a.Complete = st.window.Continuous && keyEventsInside(evts, st.window)
	if !a.Complete {
		return keyAuthorization{KeyID: keyID, Source: lifecycleSourceUnbounded, Evaluable: true}
	}
	a.Source = lifecycleSourceLedger
	return a
}

func keyEventsInside(evts []keyLifecycleEntry, w lifecycleWindow) bool {
	if len(evts) == 0 {
		return false
	}
	for _, e := range evts {
		if e.EventSeq < w.MinSeq || e.EventSeq > w.MaxSeq {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// The interval check (ADR-055 §6.2, plus R41-7)
// ---------------------------------------------------------------------------

// signatureValidity is the Phase 41 projection on the verify surface.
type signatureValidity struct {
	AuthorizedFrom  string `json:"authorized_from,omitempty"`
	AuthorizedUntil string `json:"authorized_until,omitempty"`
	TerminalEvent   string `json:"terminal_event,omitempty"`
	Source          string `json:"source"`
}

func validityOf(a keyAuthorization) *signatureValidity {
	if !a.Evaluable {
		return nil
	}
	return &signatureValidity{
		AuthorizedFrom:  a.NotBefore,
		AuthorizedUntil: a.NotAfter,
		TerminalEvent:   a.Terminal,
		Source:          a.Source,
	}
}

// authorizeByLifecycle is applied ONLY when Phase 37 already returned
// `signature_ok` — it can never rescue a bad signature, only narrow a good one.
//
// Order matters and is deliberate:
//
//  1. Phase 41 not in play ⇒ return unchanged (zero regression, ADR-055 §11);
//  2. [R41-7] the `signed_at` declaration is parsed FIRST. An unparseable
//     declaration is a structural, deterministic defect of the time claim
//     itself — asserting it requires no guessing, and leaving it as
//     `signature_ok` would make forging an interval-proof signature as cheap as
//     writing a bad string;
//  3. unbounded / incomplete evidence ⇒ hold `signature_ok` (never assert);
//  4. otherwise the interval decides.
func authorizeByLifecycle(v SignatureVerdict, signedAt string, a keyAuthorization) SignatureVerdict {
	if v.Verdict != sigVerdictOK || !a.Evaluable {
		return v
	}
	t, terr := parseRFC3339Nano(signedAt)
	if terr != nil {
		out := SignatureVerdict{
			Verdict:  sigVerdictTimeUnparseable,
			KeyID:    v.KeyID,
			Detail:   "signature signed_at is not a parseable RFC3339Nano timestamp: the time declaration itself is defective, so no authorization interval can be evaluated",
			Validity: validityOf(a),
		}
		return out
	}
	if a.Source != lifecycleSourceLedger || !a.Complete {
		v.Validity = validityOf(a)
		return v
	}
	if a.NotBefore != "" {
		if nb, nerr := parseRFC3339Nano(a.NotBefore); nerr == nil && t.Before(nb) {
			return SignatureVerdict{
				Verdict:  sigVerdictBeforeActivation,
				KeyID:    v.KeyID,
				Detail:   "signed before the key was activated",
				Validity: validityOf(a),
			}
		}
	}
	if a.NotAfter != "" {
		if na, nerr := parseRFC3339Nano(a.NotAfter); nerr == nil && !t.Before(na) {
			switch a.Terminal {
			case lifecycleEventRevoked:
				return SignatureVerdict{
					Verdict:  sigVerdictAfterRevocation,
					KeyID:    v.KeyID,
					Detail:   "signed at or after the key was revoked",
					Validity: validityOf(a),
				}
			case lifecycleEventRotatedOut:
				return SignatureVerdict{
					Verdict:  sigVerdictAfterRotation,
					KeyID:    v.KeyID,
					Detail:   "signed at or after the key was rotated out",
					Validity: validityOf(a),
				}
			}
		}
	}
	v.Validity = validityOf(a)
	return v
}

func parseRFC3339Nano(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
}

// compareRFC3339 compares two RFC3339Nano strings; unparseable sorts last so a
// comparison error can never widen an interval.
func compareRFC3339(a, b string) int {
	ta, ea := parseRFC3339Nano(a)
	tb, eb := parseRFC3339Nano(b)
	switch {
	case ea != nil && eb != nil:
		return 0
	case ea != nil:
		return 1
	case eb != nil:
		return -1
	case ta.Before(tb):
		return -1
	case ta.After(tb):
		return 1
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Appending (ADR-055 §3, §8; R41-6)
// ---------------------------------------------------------------------------

// keyLifecycleRequest is the operator-submitted fact.
type keyLifecycleRequest struct {
	EventType string `json:"event_type"`
	KeyID     string `json:"key_id"`
	NotBefore string `json:"not_before,omitempty"`
	NotAfter  string `json:"not_after,omitempty"`
}

// appendKeyLifecycleEvent records one lifecycle fact. It is a SINGLE critical
// section: read the ledger, validate, allocate the seq, compute the digest,
// sign, append, compact. Nothing is cached in memory and no watermark file
// exists, which is what R41-6 demands:
//
//   - the duplicate check SCANS THE LEDGER (a memory-only check would let a
//     crashed-and-retried submission land twice);
//   - event_seq is derived from the ledger's own maximum, inside this section;
//   - there is deliberately NO `event-seq.state` watermark. Persisting a seq
//     before appending would burn a number the append never used, leaving a hole
//     that `window_discontinuous` would then have to read as tampering. The
//     Phase 35 watermark pattern exists for publication IDENTITY; copying it
//     here would wound this log with its own discipline.
func appendKeyLifecycleEvent(c keyLifecycleConfig, req keyLifecycleRequest, at time.Time) (keyLifecycleEntry, error) {
	var zero keyLifecycleEntry
	if !c.writable() {
		return zero, errors.New("key lifecycle: no key-authority key configured (writes disabled)")
	}
	switch req.EventType {
	case lifecycleEventActivated, lifecycleEventRotatedOut, lifecycleEventRevoked:
	default:
		return zero, fmt.Errorf("key lifecycle: unknown event_type %q", req.EventType)
	}
	// WHO is still decided by Phase 37: we never open a ledger entry for a key
	// that is not in the signing trust set.
	if c.signingTrust == nil || len(c.signingTrust.keys) == 0 {
		return zero, errors.New("key lifecycle: no signing trust anchor configured — cannot bind a key identity")
	}
	pub, known := c.signingTrust.keys[req.KeyID]
	if !known {
		return zero, fmt.Errorf("key lifecycle: key_id %q is not in the signing trust set", req.KeyID)
	}
	switch req.EventType {
	case lifecycleEventActivated:
		if strings.TrimSpace(req.NotAfter) != "" {
			return zero, errors.New("key lifecycle: an activated event must not carry not_after")
		}
		if _, perr := parseRFC3339Nano(req.NotBefore); perr != nil {
			return zero, fmt.Errorf("key lifecycle: not_before %q is not a parseable RFC3339Nano timestamp", req.NotBefore)
		}
	default:
		if strings.TrimSpace(req.NotBefore) != "" {
			return zero, fmt.Errorf("key lifecycle: a %s event must not carry not_before", req.EventType)
		}
		if _, perr := parseRFC3339Nano(req.NotAfter); perr != nil {
			return zero, fmt.Errorf("key lifecycle: not_after %q is not a parseable RFC3339Nano timestamp", req.NotAfter)
		}
	}

	path := keyLifecycleLogPath(c.dir)
	lines, ok, err := readLogLines(path, keyLifecycleGroupOf)
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, fmt.Errorf("key lifecycle log holds %d unclassifiable line(s); refusing to append", countUnclassified(lines))
	}

	var history []keyLifecycleEntry
	maxSeq := int64(0)
	prevDigest := ""
	for i := range lines {
		var e keyLifecycleEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			return zero, fmt.Errorf("key lifecycle line %d is not readable: %w", i+1, jerr)
		}
		// An entry we cannot trust stops everything: appending to a ledger we
		// cannot evaluate would launder the corruption (ADR-055 §8).
		if v := verifyKeyLifecycleEntrySignature(&e, c.ka.trust); v.Verdict != sigVerdictOK {
			return zero, fmt.Errorf("key lifecycle: refusing to append — event_seq %d is %s (%s)", e.EventSeq, v.Verdict, v.Detail)
		}
		if c.streamID != "" && e.StreamID != "" && e.StreamID != c.streamID {
			return zero, fmt.Errorf("key lifecycle: refusing to append — event_seq %d belongs to another export directory", e.EventSeq)
		}
		// [R41-6.1] Duplicate detection reads the LEDGER, not a memory state.
		if e.KeyID == req.KeyID && e.EventType == req.EventType && e.NotAfter == req.NotAfter {
			return zero, fmt.Errorf("key lifecycle: duplicate submission — (%s, %s, %s) is already recorded at event_seq %d",
				req.KeyID, req.EventType, req.NotAfter, e.EventSeq)
		}
		if e.EventSeq > maxSeq {
			maxSeq = e.EventSeq
			prevDigest = e.EventDigest
		}
		history = append(history, e)
	}
	if verr := validateLifecycleTransition(history, req); verr != nil {
		return zero, verr
	}

	e := keyLifecycleEntry{
		V:                 1,
		EventSeq:          maxSeq + 1, // [R41-6.2] derived from the ledger, in-section
		EventType:         req.EventType,
		KeyID:             req.KeyID,
		PubkeyFingerprint: fingerprintOfPublicKey(pub),
		NotBefore:         req.NotBefore,
		NotAfter:          req.NotAfter,
		RecordedAt:        at.UTC().Format(time.RFC3339Nano),
		AuthorityKeyID:    c.ka.signer.keyID,
		StreamID:          c.streamID,
		PrevEventDigest:   prevDigest,
	}
	dg, derr := lifecycleEventDigest(&e)
	if derr != nil {
		return zero, derr
	}
	e.EventDigest = dg
	if serr := c.ka.signer.signKeyLifecycleEntry(&e, at); serr != nil {
		return zero, serr
	}
	raw, merr := serializeKeyLifecycleEntryBytes(&e)
	if merr != nil {
		return zero, merr
	}
	if aerr := appendLogLine(path, raw); aerr != nil {
		return zero, aerr
	}
	if c.capacity > 0 {
		if cerr := compactKeyLifecyclePrefix(c); cerr != nil {
			return zero, cerr
		}
	}
	return e, nil
}

// validateLifecycleTransition enforces the per-key state machine (ADR-055 §3).
// Every refusal is fail-closed: nothing is written and the file is untouched.
func validateLifecycleTransition(history []keyLifecycleEntry, req keyLifecycleRequest) error {
	var mine []keyLifecycleEntry
	for _, e := range history {
		if e.KeyID == req.KeyID {
			mine = append(mine, e)
		}
	}
	activated, terminal := false, ""
	for _, e := range mine {
		switch e.EventType {
		case lifecycleEventActivated:
			activated = true
		case lifecycleEventRotatedOut, lifecycleEventRevoked:
			if terminal != lifecycleEventRevoked {
				terminal = e.EventType
			}
		}
	}
	// The first fact about a key is its activation: a revocation with no
	// baseline is a meaningless statement.
	if !activated && req.EventType != lifecycleEventActivated {
		return fmt.Errorf("key lifecycle: key %q has no activated event — the first event must be activated", req.KeyID)
	}
	switch {
	case req.EventType == lifecycleEventActivated && activated:
		// Resurrection: re-activating a rotated-out or revoked key would wash the
		// earlier terminal fact out of the interval resolution.
		return fmt.Errorf("key lifecycle: key %q is already activated — resurrection is refused", req.KeyID)
	case req.EventType == lifecycleEventActivated && terminal != "":
		return fmt.Errorf("key lifecycle: key %q is %s — resurrection is refused", req.KeyID, terminal)
	}
	// A terminal bound may never precede the activation bound: that would
	// describe an empty (and therefore meaningless) interval.
	if req.EventType != lifecycleEventActivated {
		for _, e := range mine {
			if e.EventType == lifecycleEventActivated && e.NotBefore != "" {
				if compareRFC3339(req.NotAfter, e.NotBefore) < 0 {
					return fmt.Errorf("key lifecycle: not_after %s precedes the activation not_before %s", req.NotAfter, e.NotBefore)
				}
			}
		}
	}
	return nil
}

func fingerprintOfPublicKey(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// compactKeyLifecyclePrefix bounds the log by whole event groups. It is the
// ONLY rewrite and it copies survivors verbatim (shared primitive, R40-4).
func compactKeyLifecyclePrefix(c keyLifecycleConfig) error {
	return compactLogPrefixGroupsObserved(keyLifecycleLogPath(c.dir), c.capacity, keyLifecycleGroupOf, c.observe)
}

// ---------------------------------------------------------------------------
// Read surface
// ---------------------------------------------------------------------------

// keyLifecycleStatusSummary is the roll-up exposed on the management face.
// Every field is omitempty at the parent level, so a disabled Phase 41 leaves
// the status document byte-identical to Phase 40.
type keyLifecycleStatusSummary struct {
	Enabled     bool               `json:"enabled"`
	Writable    bool               `json:"writable,omitempty"`
	EventCount  int                `json:"event_count,omitempty"`
	Error       string             `json:"error,omitempty"`
	Window      *lifecycleWindow   `json:"window,omitempty"`
	Authorities []keyAuthorization `json:"authorizations,omitempty"`
	ActiveKeyID string             `json:"active_key_id,omitempty"`
}

// keyLifecycleSummary builds the roll-up. It is read-only: it never creates the
// file, never repairs it and never compacts it.
func keyLifecycleSummary(c keyLifecycleConfig) keyLifecycleStatusSummary {
	out := keyLifecycleStatusSummary{}
	if !c.enabled() {
		return out
	}
	out.Enabled = true
	out.Writable = c.writable()
	st, err := loadKeyLifecycleState(c)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.EventCount = st.total
	w := st.window
	out.Window = &w
	if !st.verifiable {
		out.Error = "key lifecycle ledger is not verifiable — every key is treated as unbounded (" + strings.Join(st.errs, "; ") + ")"
		return out
	}
	keyIDs := make([]string, 0, len(st.byKey))
	for k := range st.byKey {
		keyIDs = append(keyIDs, k)
	}
	sortStrings(keyIDs)
	for _, k := range keyIDs {
		a := st.authorizationFor(k)
		out.Authorities = append(out.Authorities, a)
		if a.Source == lifecycleSourceLedger && a.NotAfter == "" {
			out.ActiveKeyID = k
		}
	}
	return out
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// keyLifecycleLedgerAbsent reports whether the Phase has never produced a file.
func keyLifecycleLedgerAbsent(dir string) bool {
	_, err := os.Stat(keyLifecycleLogPath(dir))
	return os.IsNotExist(err)
}
