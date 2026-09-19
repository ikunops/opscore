package server

// Phase 42 — Verification Attestation: making "whether" assertable
// (ADR-057 scope, ADR-058 architecture).
//
// The problem it solves. Phases 35~41 built seven separate read-only judgement
// dimensions — status (P35), coverage (P36), signature (P37/P41), chain
// (P38), chain source (P39), anchor (P40) and the key-lifecycle ruler (P41) —
// and every one of them is an EVANESCENT, LAZY, PURE QUERY. Nothing in the
// codebase records that a verification ever happened: `VerifySnapshotsDetailed`
// and `Coverage` both write nothing, and no `verified_at` / `last_verified`
// exists anywhere. The consequence is a paradox the earlier Phases cannot see:
// because every judgement is a pure function of the current disk state, an
// attacker who simply never triggers a verification never produces an adverse
// conclusion. "Silence" and "everything is fine" are byte-identical.
//
// Phase 42 closes that by turning verification into a RECORDED FACT: a signed,
// hash-chained, append-only log of verification reports, plus a comparison
// engine that can say, of two reports about the same publication, that they
// DISAGREE. The four new assertable verdicts are:
//
//	verification_absent   — this publication has never been verified at all
//	verification_gap      — it was never covered by any surviving report
//	verification_divergent— two reports about it disagree
//	verification_scope_changed — the two reports were measured with a different ruler
//
// Division of labour across Phases, stated once:
//
//	P37 decides WHO, P40 decides WHERE, P41 decides WHEN, P42 decides WHETHER.
//
// Frozen boundaries (ADR-058 §2/§9, honoured here):
//   - `VerifySnapshotsDetailed`, `Coverage` and every Phase 35~41 file are READ
//     but never modified; P42 only assembles their outputs into a report;
//   - `appendonly_log.go` is reused unchanged (one persistence discipline);
//   - `snapshot_anchor.go` grows three omitempty fields and one kind constant
//     and nothing else — P40/P41 anchor bytes are bit-identical (T196b);
//   - with attestation off nothing is written, no new file appears and the new
//     routes answer 503 (default zero regression).
//
// Two rules dominate this file and must never be weakened:
//
//  1. THE MONOTONE MERGE. A merged verdict may never be more optimistic than
//     the worst ASSERTED dimension (ADR-057 I2). "Cannot be asserted" is
//     `unattested`, which is emphatically NOT `contradicted`: unprovable is not
//     disproven. Evidence never adds up.
//  2. I7 — ANCHORING NEVER FLOWS BACK. The delivery state of a report's own
//     anchor is an EXIT, not an INPUT: it is shown for reference but never
//     enters `Evaluable`, never enters `reasons[]` and never participates in
//     any merge. Otherwise the availability of an external witness would become
//     a judgement input (A7-6) and the merge would depend on itself.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// verificationLogFile lives in the export directory, next to the manifest, the
// ledger, the lifecycle log and the anchor logs (ADR-058 §2 — same domain).
const verificationLogFile = "verification-log.jsonl"

// verificationSubjectLimit caps one report's subject, matching the verify and
// coverage surfaces' own cap so the three views describe the same window.
const verificationSubjectLimit = 100

// The three verdict values of the merged judgement (ADR-057 I2).
const (
	verificationAttested     = "attested"
	verificationUnattested   = "unattested"
	verificationContradicted = "contradicted"
)

// Internal classification of one dimension's value. `classUnavailable` is not a
// value class at all: it means the dimension could not be evaluated in this
// report, which by the monotone rule yields `unattested` — never `attested`.
const (
	classOK          = "ok"
	classContradicted = "contradicted"
	classUnattested  = "unattested"
	classUnavailable = "unavailable"
)

// The seven judgement dimensions, in a FIXED order. The order is part of the
// on-disk format: reasons[] and changed_dimensions[] are emitted in it.
const (
	dimStatus      = "status"
	dimSignature   = "signature"
	dimChain       = "chain"
	dimChainSource = "chain_source"
	dimAnchor      = "anchor"
	dimLifecycle   = "lifecycle"
	dimCoverage    = "coverage"
)

var verificationDimensions = []string{
	dimStatus, dimSignature, dimChain, dimChainSource, dimAnchor, dimLifecycle, dimCoverage,
}

// The two report-level (global ruler) dimensions. They are evaluated ONCE per
// report and copied to every item, with the scope carried in the reason —
// pretending a report-wide fact is per-publication evidence is evidence
// dishonesty (ADR-058 §3.2-D2).
const (
	reportScopeLedger = "scope=ledger"
	reportScopeReport = "scope=report"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

// VerificationItem is one publication's seven dimensions plus its own merged
// judgement. The per-publication level is not decoration: without it a
// divergence between two reports could not be attributed to a subject
// (ADR-058 §3.3 — the two-level merge is the DEFINITION PREMISE of `divergent`).
type VerificationItem struct {
	PublicationID int64  `json:"publication_id"`
	Status        string `json:"status"`
	Signature     string `json:"signature,omitempty"`
	Chain         string `json:"chain,omitempty"`
	ChainSource   string `json:"chain_source,omitempty"`
	Anchor        string `json:"anchor,omitempty"`
	Lifecycle     string `json:"lifecycle,omitempty"`
	Coverage      string `json:"coverage,omitempty"`
	Overall       string `json:"overall"`
}

// DimensionAvailability is the report's RULER: which dimensions could be
// evaluated at all. It is the sole input of the R42-2 comparison
// normalisation, so it must be produced by the SAME evaluation as the items.
type DimensionAvailability struct {
	Status      bool `json:"status"`
	Signature   bool `json:"signature"`
	Chain       bool `json:"chain"`
	ChainSource bool `json:"chain_source"`
	Anchor      bool `json:"anchor"`
	Lifecycle   bool `json:"lifecycle"`
	Coverage    bool `json:"coverage"`
}

func (d DimensionAvailability) get(dim string) bool {
	switch dim {
	case dimStatus:
		return d.Status
	case dimSignature:
		return d.Signature
	case dimChain:
		return d.Chain
	case dimChainSource:
		return d.ChainSource
	case dimAnchor:
		return d.Anchor
	case dimLifecycle:
		return d.Lifecycle
	case dimCoverage:
		return d.Coverage
	}
	return false
}

type VerificationSubject struct {
	Publications []int64 `json:"publications"`
	MinID        int64   `json:"min_id"`
	MaxID        int64   `json:"max_id"`
	Limit        int     `json:"limit"`
	Truncated    bool    `json:"truncated"`
	// Unattributed counts snapshot groups the verify surface could not bind to
	// a publication id. They are counted, never silently dropped.
	Unattributed int `json:"unattributed"`
}

// VerificationReason is one dimension's contribution to a judgement. A verdict
// with an empty reason chain is never produced (ADR-057 §3).
type VerificationReason struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value,omitempty"`
	Evaluable bool   `json:"evaluable"`
	Blocking  bool   `json:"blocking"`
	Scope     string `json:"scope,omitempty"`
}

// VerificationReport is the signed, deterministic statement "these publications
// were measured with this ruler and came out like this".
type VerificationReport struct {
	ReportSeq  int64                 `json:"report_seq"`
	Subject    VerificationSubject   `json:"subject"`
	Evaluable  DimensionAvailability `json:"evaluable"`
	Items      []VerificationItem    `json:"items"`
	Overall    string                `json:"overall"`
	Reasons    []VerificationReason  `json:"reasons"`
	VerifiedAt string                `json:"verified_at"`
	// Anchor is a REFERENCE to the out-of-domain copy of this report (R42-1).
	// Per I7 it is never an input: it is excluded from the canonical payload
	// and from every merge.
	Anchor    *verificationAnchorRef `json:"anchor,omitempty"`
	Signature *signatureBlock        `json:"signature,omitempty"`
}

// verificationAnchorRef is the report's pointer to its anchor record.
type verificationAnchorRef struct {
	AnchorSeq int64  `json:"anchor_seq,omitempty"`
	State     string `json:"state,omitempty"`
	Digest    string `json:"report_digest,omitempty"`
}

func (it VerificationItem) value(dim string) string {
	switch dim {
	case dimStatus:
		return it.Status
	case dimSignature:
		return it.Signature
	case dimChain:
		return it.Chain
	case dimChainSource:
		return it.ChainSource
	case dimAnchor:
		return it.Anchor
	case dimLifecycle:
		return it.Lifecycle
	case dimCoverage:
		return it.Coverage
	}
	return ""
}

// ---------------------------------------------------------------------------
// Canonical bytes, digest and signature — the P37 serializer family, reused.
// ---------------------------------------------------------------------------

// verificationSigned is the exact structure canonicalVerificationPayload
// serializes: the report minus {anchor, signature}.
type verificationSigned struct {
	ReportSeq  int64                 `json:"report_seq"`
	Subject    VerificationSubject   `json:"subject"`
	Evaluable  DimensionAvailability `json:"evaluable"`
	Items      []VerificationItem    `json:"items"`
	Overall    string                `json:"overall"`
	Reasons    []VerificationReason  `json:"reasons"`
	VerifiedAt string                `json:"verified_at"`
}

func verificationSignedFields(r *VerificationReport) verificationSigned {
	return verificationSigned{
		ReportSeq:  r.ReportSeq,
		Subject:    r.Subject,
		Evaluable:  r.Evaluable,
		Items:      r.Items,
		Overall:    r.Overall,
		Reasons:    r.Reasons,
		VerifiedAt: r.VerifiedAt,
	}
}

// canonicalVerificationPayload returns the exact bytes the signature covers.
// It is the same one canonical serializer family as P37/P40/P41 — a second
// serialization would drift, and drift here silently changes what the evidence
// means (ADR-058 §3.1).
func canonicalVerificationPayload(r *VerificationReport) ([]byte, error) {
	if r == nil {
		return nil, errors.New("nil verification report")
	}
	return json.Marshal(verificationSignedFields(r))
}

// reportDigest is the report's own commitment: sha256(canonical payload).
func reportDigest(r *VerificationReport) (string, error) {
	payload, err := canonicalVerificationPayload(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// The log entry (one fact per report_seq)
// ---------------------------------------------------------------------------

type verificationLogEntry struct {
	V          int                   `json:"v"` // 1
	ReportSeq  int64                 `json:"report_seq"`
	Subject    VerificationSubject   `json:"subject"`
	Evaluable  DimensionAvailability `json:"evaluable"`
	Items      []VerificationItem    `json:"items"`
	Overall    string                `json:"overall"`
	Reasons    []VerificationReason  `json:"reasons"`
	VerifiedAt string                `json:"verified_at"`
	StreamID   string                `json:"stream_id"`
	KeyID      string                `json:"key_id"`
	PrevDigest string                `json:"prev_digest,omitempty"`
	Digest     string                `json:"digest"`
	Signature  *signatureBlock       `json:"signature,omitempty"`
}

// verificationSignedEntry is the entry minus {digest, signature}: the exact
// bytes its own signature covers.
type verificationSignedEntry struct {
	V          int                   `json:"v"`
	ReportSeq  int64                 `json:"report_seq"`
	Subject    VerificationSubject   `json:"subject"`
	Evaluable  DimensionAvailability `json:"evaluable"`
	Items      []VerificationItem    `json:"items"`
	Overall    string                `json:"overall"`
	Reasons    []VerificationReason  `json:"reasons"`
	VerifiedAt string                `json:"verified_at"`
	StreamID   string                `json:"stream_id"`
	KeyID      string                `json:"key_id"`
	PrevDigest string                `json:"prev_digest,omitempty"`
}

func verificationSignedEntryFields(e *verificationLogEntry) verificationSignedEntry {
	return verificationSignedEntry{
		V:          e.V,
		ReportSeq:  e.ReportSeq,
		Subject:    e.Subject,
		Evaluable:  e.Evaluable,
		Items:      e.Items,
		Overall:    e.Overall,
		Reasons:    e.Reasons,
		VerifiedAt: e.VerifiedAt,
		StreamID:   e.StreamID,
		KeyID:      e.KeyID,
		PrevDigest: e.PrevDigest,
	}
}

func canonicalVerificationEntryPayload(e *verificationLogEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil verification entry")
	}
	return json.Marshal(verificationSignedEntryFields(e))
}

func verificationEntryDigest(e *verificationLogEntry) (string, error) {
	payload, err := canonicalVerificationEntryPayload(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func serializeVerificationEntryBytes(e *verificationLogEntry) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func verificationLogPath(dir string) string { return filepath.Join(dir, verificationLogFile) }

// verificationGroupOf is the classifier this log injects into the shared
// append-only primitive: one `report_seq` is ONE group. A report is a FACT, so
// a second line for the same seq is always a conflict — never a state advance
// (the deliberate opposite of the P40 anchor entry, ADR-057 §3).
func verificationGroupOf(raw []byte) (int64, bool) {
	var e verificationLogEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return e.ReportSeq, true
}

// ---------------------------------------------------------------------------
// Classification and the monotone merge
// ---------------------------------------------------------------------------

// classOf maps one dimension's value onto the three-way class. Anything not
// explicitly recognised is UNASSESSABLE, never contradictory: P42's whole
// honesty rests on "we cannot tell" not becoming "it is fine" or "it is broken".
func classOf(dim, value string) string {
	if value == "" {
		return classUnattested
	}
	switch dim {
	case dimStatus:
		switch value {
		case verifyStatusOK:
			return classOK
		case verifyStatusMismatch:
			return classContradicted
		}
		return classUnattested
	case dimSignature:
		switch value {
		case sigVerdictOK:
			return classOK
		case sigVerdictInvalid, sigVerdictBeforeActivation, sigVerdictAfterRotation,
			sigVerdictAfterRevocation, sigVerdictTimeUnparseable:
			return classContradicted
		}
		return classUnattested
	case dimChain:
		switch value {
		case chainPosPredecessorVerified, chainPosRetentionBoundary:
			return classOK
		case chainVerdictBroken:
			return classContradicted
		}
		return classUnattested
	case dimChainSource:
		switch value {
		case "disk", "ledger", "disk+ledger":
			return classOK
		}
		return classUnattested
	case dimAnchor:
		// Only a CONFIRMED anchor is evidence (ADR-052 A5): pending and
		// unanchored are delivery states, not judgements.
		if value == anchorStateAnchored {
			return classOK
		}
		return classUnattested
	case dimLifecycle:
		// P42 records the RULER only: P41's four verdicts are already folded
		// into `signature` by `authorizeByLifecycle` (ADR-058 §3.2-D1).
		if value == "bounded" {
			return classOK
		}
		return classUnattested
	case dimCoverage:
		// P36 never accuses tampering: a gap is UNASSESSABLE, never broken.
		if value == completenessComplete {
			return classOK
		}
		return classUnattested
	}
	return classUnattested
}

// mergeOverall is the ONE monotone merge, used at BOTH levels (per-publication
// and per-report) — a second merge implementation would silently diverge
// (ADR-058 §3.3).
//
//	any asserted-bad            ⇒ contradicted
//	any unattested/unavailable  ⇒ unattested
//	otherwise                   ⇒ attested
func mergeOverall(classes []string) string {
	weak := false
	for _, c := range classes {
		switch c {
		case classContradicted:
			return verificationContradicted
		case classOK:
		default:
			weak = true
		}
	}
	if weak {
		return verificationUnattested
	}
	return verificationAttested
}

// classesOfItem evaluates one item against the report's ruler. A dimension the
// report could not evaluate at all contributes `unavailable` — which, per the
// monotone rule, can only ever produce `unattested` (a report built with an
// incomplete ruler is never `attested`).
func classesOfItem(it VerificationItem, ev DimensionAvailability) []string {
	out := make([]string, 0, len(verificationDimensions))
	for _, d := range verificationDimensions {
		if !ev.get(d) {
			out = append(out, classUnavailable)
			continue
		}
		out = append(out, classOf(d, it.value(d)))
	}
	return out
}

func itemClassesForReport(it VerificationItem) []string {
	return []string{
		mapItemOverall(it.Overall),
	}
}

func mapItemOverall(v string) string {
	switch v {
	case verificationAttested:
		return classOK
	case verificationContradicted:
		return classContradicted
	}
	return classUnattested
}

// ---------------------------------------------------------------------------
// Configuration bundle
// ---------------------------------------------------------------------------

type verificationConfig struct {
	dir      string
	capacity int
	signer   *exportSigner
	trust    *exportTrustStore
	streamID string
	// on is the deployment's switch (--export-verify-attest). Without it the
	// Phase is inert even when keys happen to be configured, so a default
	// deployment stays byte-identical to Phase 41 (ADR-058 §1).
	on bool
}

func (c verificationConfig) enabled() bool {
	return c.on && c.signer != nil && c.trust != nil && len(c.trust.keys) > 0
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// verificationWindow is the surviving contiguous run of report_seq values — the
// R40-1 discipline instantiated for this log. Everything below `min` may
// legally have been compacted away and must never be read as "never existed".
type verificationWindow struct {
	MinSeq     int64 `json:"min_seq"`
	MaxSeq     int64 `json:"max_seq"`
	Entries    int   `json:"entries"`
	Continuous bool  `json:"continuous"`
}

type verificationState struct {
	entries    []verificationLogEntry
	window     verificationWindow
	verifiable bool
	errs       []string
	total      int
}

// loadVerificationState reads and validates the log. It is strictly fail-closed
// along every axis (ADR-058 §3.1, P41 I2 generalised):
//
//   - an unclassifiable line fails the whole load (skipping is how a tampered
//     prefix disappears);
//   - an entry whose signature does not verify, whose digest does not match its
//     canonical payload, whose prev pointer is broken, or which belongs to
//     another stream, poisons the log: a partially trusted history is not
//     trusted;
//   - the FIRST surviving entry is exempt from the prev check, because after a
//     legal prefix compaction it points at an entry that is legitimately gone.
func loadVerificationState(c verificationConfig) (*verificationState, error) {
	st := &verificationState{verifiable: true}
	lines, ok, err := readLogLines(verificationLogPath(c.dir), verificationGroupOf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("verification log holds %d unclassifiable line(s); refusing to evaluate it", countUnclassified(lines))
	}
	if len(lines) == 0 {
		return st, nil
	}

	prevDigest := ""
	seqs := make([]int64, 0, len(lines))
	seenSeq := map[int64]bool{}
	for i := range lines {
		var e verificationLogEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			return nil, fmt.Errorf("verification line %d is not readable: %w", i+1, jerr)
		}
		st.total++
		seqs = append(seqs, e.ReportSeq)
		// A report is a FACT, not a state (ADR-057 §3): a second line for the
		// same report_seq — even a byte-identical one — is a conflict and
		// poisons the log. This is the deliberate OPPOSITE of the P40 anchor
		// entry, whose state may legitimately advance.
		if seenSeq[e.ReportSeq] {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("report_seq %d appears more than once: a verification report is a fact, so a duplicate is a conflict — fail-closed", e.ReportSeq))
			prevDigest = ""
			continue
		}
		seenSeq[e.ReportSeq] = true

		if v := verifyVerificationEntrySignature(&e, c.trust); v.Verdict != sigVerdictOK {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("report_seq %d: %s (%s)", e.ReportSeq, v.Verdict, v.Detail))
			prevDigest = ""
			continue
		}
		dg, derr := verificationEntryDigest(&e)
		if derr != nil || dg != e.Digest {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("report_seq %d: digest does not match its canonical payload", e.ReportSeq))
			prevDigest = ""
			continue
		}
		if i > 0 && e.PrevDigest != prevDigest {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("report_seq %d: prev_digest breaks the hash chain", e.ReportSeq))
			prevDigest = ""
			continue
		}
		if c.streamID != "" && e.StreamID != "" && e.StreamID != c.streamID {
			st.verifiable = false
			st.errs = append(st.errs, fmt.Sprintf("report_seq %d: belongs to stream %s, not this export directory (%s)", e.ReportSeq, e.StreamID, c.streamID))
			prevDigest = ""
			continue
		}
		prevDigest = e.Digest
		st.entries = append(st.entries, e)
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
		st.errs = append(st.errs, "window_discontinuous: the surviving report_seq run has a hole — no verdict may be asserted")
	}
	return st, nil
}

// signVerificationEntry fills the entry's signature block (Ed25519, the P37
// block reused verbatim — no second format, no second crypto).
func (s *exportSigner) signVerificationEntry(e *verificationLogEntry, at time.Time) error {
	if s == nil {
		return errors.New("verification: no signing key configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("verification: private key is not a valid Ed25519 key")
	}
	if e.Signature == nil {
		e.Signature = &signatureBlock{}
	}
	e.Signature.Alg = signatureAlgEd25519
	e.Signature.KeyID = s.keyID
	e.Signature.SignedAt = at.UTC().Format(time.RFC3339Nano)
	e.Signature.Sig = ""
	payload, err := canonicalVerificationEntryPayload(e)
	if err != nil {
		return err
	}
	e.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

func verifyVerificationEntrySignature(e *verificationLogEntry, trust *exportTrustStore) SignatureVerdict {
	if e == nil || e.Signature == nil {
		return SignatureVerdict{Verdict: sigVerdictAbsent, KeyID: e.KeyID, Detail: "verification report carries no signature"}
	}
	sb := e.Signature
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "verification signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "verification signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted export keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "verification key_id is not in the trusted set"}
	}
	payload, perr := canonicalVerificationEntryPayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "verification signature does not verify against the trust anchor"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// ---------------------------------------------------------------------------
// Building a report (pure: no writes)
// ---------------------------------------------------------------------------

// verificationInputs is everything the assembler reads, gathered once so the
// report is a deterministic function of one snapshot of the world.
type verificationInputs struct {
	results   []VerifyResult
	idOf      map[string]int64
	coverage  *CoverageResult
	anchor    *anchorState
	lifecycle *keyLifecycleState
	limit     int
	now       time.Time
	// ruler switches, all decided by the deployment and none of them by the
	// data being judged.
	signerTrustKeys  int
	anchorEnabled    bool
	lifecycleEnabled bool
}

func buildVerificationReport(in verificationInputs) (VerificationReport, error) {
	var zero VerificationReport
	ev := DimensionAvailability{
		Status:      true,
		Signature:   false,
		Chain:       true,
		ChainSource: true,
		Anchor:      false,
		Lifecycle:   false,
		Coverage:    in.coverage != nil,
	}
	// Signature is evaluable only when a trust anchor exists: without one no
	// signature statement can be checked, so the ruler is incomplete.
	ev.Signature = in.signerTrustKeys > 0
	ev.Anchor = in.anchorEnabled
	ev.Lifecycle = in.lifecycleEnabled

	coverageValue := coverageVerdictOf(in.coverage)

	items := make([]VerificationItem, 0, len(in.results))
	var pubs []int64
	unattributed := 0
	for _, res := range in.results {
		id, ok := in.idOf[res.Snapshot]
		if !ok || id == 0 {
			unattributed++
			continue
		}
		it := VerificationItem{PublicationID: id, Status: res.Status}
		if res.Signature != nil {
			it.Signature = res.Signature.Verdict
		}
		it.Chain = res.Chain
		it.ChainSource = res.ChainSource
		if in.anchor != nil {
			if ae, ok := in.anchor.byID[id]; ok {
				it.Anchor = ae.State
			}
		}
		it.Lifecycle = lifecycleRulerOf(in.lifecycle, sigKeyIDOf(res))
		it.Coverage = coverageValue
		it.Overall = mergeOverall(classesOfItem(it, ev))
		items = append(items, it)
		pubs = append(pubs, id)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PublicationID < items[j].PublicationID })
	sort.Slice(pubs, func(i, j int) bool { return pubs[i] < pubs[j] })

	sub := VerificationSubject{Limit: in.limit, Unattributed: unattributed}
	if len(pubs) > 0 {
		sub.Publications = pubs
		sub.MinID, sub.MaxID = pubs[0], pubs[0]
		for _, p := range pubs {
			if p < sub.MinID {
				sub.MinID = p
			}
			if p > sub.MaxID {
				sub.MaxID = p
			}
		}
		sub.Truncated = len(pubs) >= in.limit
	}

	report := VerificationReport{
		Subject:    sub,
		Evaluable:  ev,
		Items:      items,
		Reasons:    buildVerificationReasons(items, ev, coverageValue),
		VerifiedAt: in.now.UTC().Format(time.RFC3339Nano),
	}
	if len(report.Reasons) == 0 {
		return zero, errors.New("verification: refusing to produce a verdict with an empty reason chain (fail-closed)")
	}
	report.Overall = mergeOverall(reportOverallClasses(items))
	return report, nil
}

// buildVerificationReasons emits one reason per dimension, always. The two
// report-level dimensions carry their scope so a reader can never mistake a
// report-wide ruler for per-publication evidence (ADR-058 §3.2-D2).
func buildVerificationReasons(items []VerificationItem, ev DimensionAvailability, coverageValue string) []VerificationReason {
	out := make([]VerificationReason, 0, len(verificationDimensions))
	for _, d := range verificationDimensions {
		r := VerificationReason{Dimension: d, Evaluable: ev.get(d)}
		switch d {
		case dimLifecycle:
			r.Scope = reportScopeLedger
		case dimCoverage:
			r.Scope = reportScopeReport
			r.Value = coverageValue
		}
		if !r.Evaluable {
			out = append(out, r)
			continue
		}
		worst, value := classOK, ""
		for _, it := range items {
			c := classOf(d, it.value(d))
			if worse(c, worst) {
				worst, value = c, it.value(d)
			}
			if worst == classContradicted {
				break
			}
		}
		if d == dimCoverage {
			value = coverageValue
		}
		r.Value = value
		r.Blocking = worst == classContradicted
		out = append(out, r)
	}
	return out
}

// worse orders the classes by severity (contradicted > unattested > ok).
func worse(a, b string) bool {
	rank := func(c string) int {
		switch c {
		case classContradicted:
			return 2
		case classUnattested, classUnavailable:
			return 1
		}
		return 0
	}
	return rank(a) > rank(b)
}

func reportOverallClasses(items []VerificationItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, mapItemOverall(it.Overall))
	}
	return out
}

func coverageVerdictOf(cov *CoverageResult) string {
	if cov == nil {
		return ""
	}
	switch {
	case cov.Completeness == completenessComplete && len(cov.Gaps) == 0:
		return completenessComplete
	case len(cov.Gaps) > 0:
		return "gap"
	case len(cov.Indeterminate.PublicationGaps) > 0:
		return "indeterminate"
	}
	return "unknown"
}

func sigKeyIDOf(res VerifyResult) string {
	if res.Signature == nil {
		return ""
	}
	return res.Signature.KeyID
}

// lifecycleRulerOf projects the P41 ledger into P42's RULER: can the time
// dimension be asserted at all for this key? It deliberately does NOT repeat
// P41's verdicts — those are already inside `signature` (D1).
func lifecycleRulerOf(st *keyLifecycleState, keyID string) string {
	if st == nil || !st.verifiable || keyID == "" {
		return ""
	}
	a := st.authorizationFor(keyID)
	if !a.Evaluable || !a.Complete || a.Source != lifecycleSourceLedger {
		return "unbounded"
	}
	return "bounded"
}

// ---------------------------------------------------------------------------
// Appending (write path)
// ---------------------------------------------------------------------------

// appendVerificationReport records one report. It is a SINGLE critical section:
// read the log, validate, allocate the seq, digest, sign, append, compact.
//
// There is deliberately NO watermark file: report_seq is derived from the log's
// own maximum inside this section (P41 R41-6, inherited verbatim). Persisting a
// seq before the append would burn a number the append never used and leave a
// hole that `window_discontinuous` would then have to read as tampering.
//
// A report is a FACT: a second line for the same report_seq is always a
// conflict, never a state advance (ADR-057 §3 — the opposite of the P40 anchor).
func appendVerificationReport(c verificationConfig, r *VerificationReport, at time.Time, hook func(dir string, seq int64)) error {
	if !c.enabled() {
		return errors.New("verification: no signing key / trust anchor configured (writes disabled)")
	}
	path := verificationLogPath(c.dir)
	lines, ok, err := readLogLines(path, verificationGroupOf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("verification log holds %d unclassifiable line(s); refusing to append", countUnclassified(lines))
	}

	maxSeq := int64(0)
	prevDigest := ""
	seenSeq := map[int64]bool{}
	for i := range lines {
		var e verificationLogEntry
		if jerr := json.Unmarshal(lines[i].raw, &e); jerr != nil {
			return fmt.Errorf("verification line %d is not readable: %w", i+1, jerr)
		}
		if seenSeq[e.ReportSeq] {
			return fmt.Errorf("verification: refusing to append — report_seq %d already recorded (a report is a fact, so a duplicate is a conflict)", e.ReportSeq)
		}
		seenSeq[e.ReportSeq] = true
		// Appending to a log we cannot evaluate would launder the corruption
		// (P41 I2, generalised to this log).
		if v := verifyVerificationEntrySignature(&e, c.trust); v.Verdict != sigVerdictOK {
			return fmt.Errorf("verification: refusing to append — report_seq %d is %s (%s)", e.ReportSeq, v.Verdict, v.Detail)
		}
		if c.streamID != "" && e.StreamID != "" && e.StreamID != c.streamID {
			return fmt.Errorf("verification: refusing to append — report_seq %d belongs to another export directory", e.ReportSeq)
		}
		if e.ReportSeq > maxSeq {
			maxSeq = e.ReportSeq
			prevDigest = e.Digest
		}
	}

	r.ReportSeq = maxSeq + 1
	e := verificationLogEntry{
		V:          1,
		ReportSeq:  r.ReportSeq,
		Subject:    r.Subject,
		Evaluable:  r.Evaluable,
		Items:      r.Items,
		Overall:    r.Overall,
		Reasons:    r.Reasons,
		VerifiedAt: r.VerifiedAt,
		StreamID:   c.streamID,
		KeyID:      c.signer.keyID,
		PrevDigest: prevDigest,
	}
	edg, ederr := verificationEntryDigest(&e)
	if ederr != nil {
		return ederr
	}
	e.Digest = edg
	if serr := c.signer.signVerificationEntry(&e, at); serr != nil {
		return serr
	}
	// A second entry digest over the SAME report bytes ties the log line to the
	// report the reader will see; both derive from one canonical serializer.
	if hook != nil {
		hook(c.dir, e.ReportSeq)
	}
	raw, merr := serializeVerificationEntryBytes(&e)
	if merr != nil {
		return merr
	}
	if aerr := appendLogLine(path, raw); aerr != nil {
		return aerr
	}
	if c.capacity > 0 {
		if cerr := compactVerificationPrefix(c); cerr != nil {
			return cerr
		}
	}
	return nil
}

func compactVerificationPrefix(c verificationConfig) error {
	return compactLogPrefixGroups(verificationLogPath(c.dir), c.capacity, verificationGroupOf)
}

// ---------------------------------------------------------------------------
// Scheduler wiring
// ---------------------------------------------------------------------------

func (s *HistoryExportScheduler) verificationEnabled() bool {
	return s != nil && s.cfg.VerifyAttest && s.signer != nil && s.trust != nil && len(s.trust.keys) > 0
}

func (s *HistoryExportScheduler) verificationConfig() verificationConfig {
	if s == nil {
		return verificationConfig{}
	}
	return verificationConfig{
		dir:      s.cfg.Dir,
		capacity: s.cfg.VerifyCapacity,
		signer:   s.signer,
		trust:    s.trust,
		streamID: s.lifecycleStreamID(),
		on:       s.cfg.VerifyAttest,
	}
}

func (s *HistoryExportScheduler) setVerificationError(msg string) {
	s.mu.Lock()
	s.verificationError = msg
	s.mu.Unlock()
}

// AttestVerification builds, signs and records one verification report, then
// hands it to the witness. It is the ONLY write path.
//
// ORDER IS FROZEN (ADR-058 §4.1, ADR-052 A2): the log line is durable BEFORE
// the anchor is dispatched — never anchor-first — and an anchor failure never
// rolls back or blocks the recorded fact.
func (s *HistoryExportScheduler) AttestVerification(limit int) (VerificationReport, error) {
	var zero VerificationReport
	if !s.verificationEnabled() {
		return zero, errors.New("verification: attestation is not enabled (--export-verify-attest with a signing key and trust anchor)")
	}
	if limit <= 0 || limit > verificationSubjectLimit {
		limit = verificationSubjectLimit
	}
	now := s.clock()

	results, _, verr := s.VerifySnapshotsDetailed(limit)
	if verr != nil {
		s.setVerificationError(verr.Error())
		return zero, verr
	}
	manifests, _, merr := s.ListManifests(limit, "")
	if merr != nil {
		s.setVerificationError(merr.Error())
		return zero, merr
	}
	idOf := map[string]int64{}
	for _, m := range manifests {
		idOf[m.Snapshot] = m.PublicationID
	}
	cov, cerr := s.Coverage(nil, nil, limit)
	if cerr != nil {
		// A failing coverage dimension is not a reason to fabricate one: the
		// dimension simply becomes unavailable, which the merge reads as
		// `unattested`.
		cov = nil
	}
	var anchorSt *anchorState
	if s.anchorEnabled() {
		st, aerr := loadAnchorState(s.cfg.Dir, s.trust)
		if aerr == nil {
			anchorSt = st
		} else {
			s.setAnchorError(aerr.Error())
		}
	}
	var lifecycleSt *keyLifecycleState
	lifecycleOn := false
	if klc := s.keyLifecycleConfig(); klc.enabled() {
		lst, lerr := loadKeyLifecycleState(klc)
		if lerr != nil {
			s.setKeyLifecycleError(lerr.Error())
		} else {
			s.setKeyLifecycleError("")
			lifecycleSt = lst
			lifecycleOn = true
		}
	}
	signerTrustKeys := 0
	if s.trust != nil {
		signerTrustKeys = len(s.trust.keys)
	}

	in := verificationInputs{
		results:          results,
		idOf:             idOf,
		coverage:         cov,
		anchor:           anchorSt,
		lifecycle:        lifecycleSt,
		limit:            limit,
		now:              now,
		signerTrustKeys:  signerTrustKeys,
		anchorEnabled:    s.anchorEnabled(),
		lifecycleEnabled: lifecycleOn,
	}
	report, berr := buildVerificationReport(in)
	if berr != nil {
		s.setVerificationError(berr.Error())
		return zero, berr
	}
	c := s.verificationConfig()
	if aerr := appendVerificationReport(c, &report, now, s.beforeVerificationAppend); aerr != nil {
		s.setVerificationError(aerr.Error())
		return zero, aerr
	}
	s.setVerificationError("")
	if s.anchorEnabled() {
		if aerr := s.anchorVerificationReport(&report); aerr != nil {
			// Never rolls back: the log is the evidence, the anchor is the
			// out-of-domain copy (ADR-052 I6, carried into Phase 42).
			s.setAnchorError(aerr.Error())
		}
	}
	return report, nil
}

// verifyTick is the periodic entry point. It is non-reentrant (P34-I1): an
// attestation already in flight is skipped, never run in parallel.
func (s *HistoryExportScheduler) verifyTick() {
	if !s.verificationEnabled() {
		return
	}
	s.mu.Lock()
	if s.verifyRunning {
		s.mu.Unlock()
		return
	}
	s.verifyRunning = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.verifyRunning = false
		s.mu.Unlock()
	}()
	if _, err := s.AttestVerification(verificationSubjectLimit); err != nil {
		s.setVerificationError(err.Error())
	}
}

func (s *HistoryExportScheduler) runVerify(parent context.Context) {
	ticker := time.NewTicker(s.cfg.VerifyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
			s.verifyTick()
		}
	}
}

// anchorVerificationReport records and dispatches the anchor of one report
// (R42-1). The pending line is durable BEFORE the witness is contacted (R40-6).
// It is a WRITE to the third anchor family and, per I7, never an input to the
// report that is being anchored.
func (s *HistoryExportScheduler) anchorVerificationReport(r *VerificationReport) error {
	if !s.anchorEnabled() || s.signer == nil {
		return nil
	}
	path := verificationAnchorPath(s.cfg.Dir)
	seq, err := nextAnchorSeqPath(path)
	if err != nil {
		return err
	}
	dg, derr := reportDigest(r)
	if derr != nil {
		return derr
	}
	ae := anchorEntry{
		AnchorSeq:    seq,
		Kind:         anchorKindVerification,
		ReportSeq:    r.ReportSeq,
		ReportDigest: dg,
		Overall:      r.Overall,
		RecordedAt:   s.clock().UTC().Format(time.RFC3339Nano),
		State:        anchorStatePending,
	}
	if serr := s.signer.signAnchorEntry(&ae, s.cfg.Dir, s.clock()); serr != nil {
		return serr
	}
	if aerr := appendAnchorEntryPath(path, s.cfg.AnchorCapacity, ae); aerr != nil {
		return aerr
	}
	if derr := s.dispatchAnchorPath(context.Background(), path, ae); derr != nil {
		return derr
	}
	r.Anchor = &verificationAnchorRef{AnchorSeq: seq, State: anchorStatePending, Digest: dg}
	return nil
}

// ---------------------------------------------------------------------------
// Read surface (strictly read-only)
// ---------------------------------------------------------------------------

// VerificationChange is one comparison result at one publication.
//
// `verification_scope_changed` is a COMPARISON category, never an adjudication:
// it never rewrites the later report's own overall (ADR-057 S2) and it is never
// silent — it always carries from/to overall and the later report's reasons
// (S1).
type VerificationChange struct {
	PublicationID     int64                `json:"publication_id"`
	FromReportSeq     int64                `json:"from_report_seq"`
	ToReportSeq       int64                `json:"to_report_seq"`
	Dimension         string               `json:"dimension,omitempty"`
	ChangedDimensions []string             `json:"changed_dimensions,omitempty"`
	FromOverall       string               `json:"from_overall"`
	ToOverall         string               `json:"to_overall"`
	Reasons           []VerificationReason `json:"reasons"`
}

// verificationGapView is the "was this publication ever verified?" answer.
// An empty `gaps` list is NEVER silent: it always carries a reason, and a
// non-assertable region always carries `gap_assertable_from` (R42-3).
type verificationGapView struct {
	Gaps           []int64 `json:"gaps"`
	NotAssertable  []int64 `json:"not_assertable"`
	Indeterminate  bool    `json:"gap_indeterminate"`
	AssertableFrom string  `json:"gap_assertable_from,omitempty"`
	Reason         string  `json:"reason"`
}

// VerificationView is the derived, strictly read-only projection of the log.
type VerificationView struct {
	Enabled             bool                 `json:"enabled"`
	Window              *verificationWindow  `json:"window,omitempty"`
	Absent              bool                 `json:"verification_absent"`
	Gap                 *verificationGapView `json:"gap,omitempty"`
	Divergent           []VerificationChange `json:"divergent,omitempty"`
	ScopeChanged        []VerificationChange `json:"scope_changed,omitempty"`
	WindowDiscontinuous bool                 `json:"window_discontinuous,omitempty"`
	Error               string               `json:"error,omitempty"`
}

// verificationStatusSummary is the roll-up on the scheduler status face.
type verificationStatusSummary struct {
	Enabled     bool                `json:"enabled"`
	ReportCount int                 `json:"report_count,omitempty"`
	Window      *verificationWindow `json:"window,omitempty"`
	LastOverall string              `json:"last_overall,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// verificationSummary builds the roll-up. Read-only: it never creates,
// repairs or compacts the log.
func verificationSummary(c verificationConfig) verificationStatusSummary {
	out := verificationStatusSummary{}
	if !c.enabled() {
		return out
	}
	out.Enabled = true
	st, err := loadVerificationState(c)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.ReportCount = st.total
	w := st.window
	out.Window = &w
	if len(st.entries) > 0 {
		out.LastOverall = st.entries[len(st.entries)-1].Overall
	}
	if !st.verifiable {
		out.Error = "verification log is not verifiable — no verdict is asserted (" + strings.Join(st.errs, "; ") + ")"
	}
	return out
}

// VerificationView derives the read-only view. It has ZERO side effects: it
// writes nothing, audits nothing and opens no network connection — an auditor
// may call it as often as they like (ADR-053 §6-13 discipline, inherited).
func (s *HistoryExportScheduler) VerificationView() (VerificationView, error) {
	out := VerificationView{}
	if !s.verificationEnabled() {
		return out, nil
	}
	out.Enabled = true
	c := s.verificationConfig()
	st, err := loadVerificationState(c)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	w := st.window
	out.Window = &w
	out.Absent = st.total == 0
	if !st.window.Continuous && st.total > 0 {
		out.WindowDiscontinuous = true
	}
	if !st.verifiable {
		// A log we cannot trust produces NO assertion at all — not even "absent
		// is fine". Fail-closed, never fail-open.
		out.Error = "verification log is not verifiable — no verdict is asserted (" + strings.Join(st.errs, "; ") + ")"
		return out, nil
	}
	out.Gap = s.deriveVerificationGap(st)
	out.Divergent, out.ScopeChanged = deriveVerificationChanges(st)
	return out, nil
}

// deriveVerificationGap answers "which observable publications were never
// covered by any surviving report?".
//
// R42-3 (the whole point of this function): the older rule allowed a gap
// assertion only when the surviving window starts at seq 1, which means that
// after the FIRST legal compaction — with the default capacity of 4096 — the
// core verdict would be permanently buried on any long-lived deployment.
// Refusing to assert in order to avoid false positives is the same failure
// R40-1 repaired from the other side (R40-1 fixed over-loud; this fixes
// over-silent).
//
// The sufficient boundary: a publication whose publish time is NOT EARLIER than
// the earliest surviving report's `verified_at` cannot have had its covering
// report legally compacted away — any report covering it was built after it
// existed, hence after the oldest survivor, hence with a LARGER report_seq,
// which prefix compaction never touches. Below that boundary the two histories
// ("never verified" / "verified by a report that was legally dropped") are
// indistinguishable, so nothing is asserted.
func (s *HistoryExportScheduler) deriveVerificationGap(st *verificationState) *verificationGapView {
	g := &verificationGapView{Gaps: []int64{}, NotAssertable: []int64{}}
	if st == nil || len(st.entries) == 0 {
		g.Indeterminate = true
		g.Reason = "no verification report survives — coverage cannot be asserted"
		return g
	}
	if !st.window.Continuous {
		g.Indeterminate = true
		g.Reason = "window_discontinuous: the surviving report_seq run has a hole — coverage cannot be asserted"
		return g
	}

	covered := map[int64]bool{}
	oldestVerifiedAt := ""
	for _, e := range st.entries {
		for _, it := range e.Items {
			covered[it.PublicationID] = true
		}
		if e.ReportSeq == st.window.MinSeq {
			oldestVerifiedAt = e.VerifiedAt
		}
	}
	// min_seq == 1 ⇒ nothing was ever compacted away ⇒ the whole observed
	// history is assertable (this is the ONLY case the pre-R42-3 rule allowed).
	fullHistory := st.window.MinSeq == 1
	if !fullHistory {
		g.AssertableFrom = oldestVerifiedAt
	}

	pubs, perr := s.observablePublications()
	if perr != nil {
		g.Indeterminate = true
		g.Reason = "publication set unavailable: " + perr.Error()
		return g
	}
	boundary, berr := parseRFC3339Nano(oldestVerifiedAt)
	boundaryOK := berr == nil
	for _, p := range pubs {
		if covered[p.ID] {
			continue
		}
		assertable := fullHistory
		if !fullHistory && boundaryOK && p.PublishedAt != "" {
			pt, perr2 := parseRFC3339Nano(p.PublishedAt)
			assertable = perr2 == nil && !pt.Before(boundary)
		}
		if assertable {
			g.Gaps = append(g.Gaps, p.ID)
		} else {
			g.NotAssertable = append(g.NotAssertable, p.ID)
		}
	}
	switch {
	case len(g.NotAssertable) > 0:
		g.Indeterminate = true
		g.Reason = fmt.Sprintf("%d publication(s) predate the assertion boundary %s — their covering report may have been legally compacted away, so their coverage is not assertable", len(g.NotAssertable), oldestVerifiedAt)
	case fullHistory:
		g.Reason = "window starts at report_seq 1 — no report was ever compacted away, so every uncovered publication is assertably never verified"
	default:
		g.Reason = fmt.Sprintf("coverage is assertable only from %s (the earliest surviving report's verified_at); %d publication(s) are uncovered", oldestVerifiedAt, len(g.Gaps))
	}
	return g
}

type observablePublication struct {
	ID          int64
	PublishedAt string
}

func (s *HistoryExportScheduler) observablePublications() ([]observablePublication, error) {
	entries, _, err := s.ListManifests(verificationSubjectLimit, "")
	if err != nil {
		return nil, err
	}
	out := make([]observablePublication, 0, len(entries))
	for _, e := range entries {
		if e.PublicationID == 0 {
			continue
		}
		out = append(out, observablePublication{ID: e.PublicationID, PublishedAt: e.GeneratedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ---------------------------------------------------------------------------
// R42-2 comparison engine (ADR-058 §5)
// ---------------------------------------------------------------------------

func valueOfEntry(e *verificationLogEntry, p int64, dim string) string {
	for _, it := range e.Items {
		if it.PublicationID == p {
			return it.value(dim)
		}
	}
	return ""
}

func overallOfEntry(e *verificationLogEntry, p int64) string {
	for _, it := range e.Items {
		if it.PublicationID == p {
			return it.Overall
		}
	}
	return ""
}

// availabilityOf is the RULER OF ONE ITEM: the global ruler AND the fact that
// this entry actually obtained a value. Both are required — a dimension the
// deployment cannot evaluate, or one this particular publication has no value
// for, is simply not comparable (ADR-058 §5.2).
func availabilityOf(e *verificationLogEntry, p int64) map[string]bool {
	out := map[string]bool{}
	for _, d := range verificationDimensions {
		out[d] = e.Evaluable.get(d) && valueOfEntry(e, p, d) != ""
	}
	return out
}

func sameAvailability(a, b map[string]bool) bool {
	for _, d := range verificationDimensions {
		if a[d] != b[d] {
			return false
		}
	}
	return true
}

const (
	changeConsistent   = "consistent"
	changeDivergent    = "divergent"
	changeScopeChanged = "scope_changed"
)

// classifyChange implements ADR-057 §4-A4.1 in the frozen order. Rule 1
// precedes rule 3 on purpose (S3): a real value disagreement on a comparable
// dimension MUST come out as `divergent` even when the ruler also changed,
// because "louder" is the direction that cannot hide a real problem.
func classifyChange(r1, r2 *verificationLogEntry, p int64) (string, string, []string) {
	a1, a2 := availabilityOf(r1, p), availabilityOf(r2, p)
	for _, d := range verificationDimensions {
		if a1[d] && a2[d] && valueOfEntry(r1, p, d) != valueOfEntry(r2, p, d) {
			return changeDivergent, d, nil
		}
	}
	if sameAvailability(a1, a2) && overallOfEntry(r1, p) != overallOfEntry(r2, p) {
		return changeDivergent, "", nil
	}
	if !sameAvailability(a1, a2) {
		var changed []string
		for _, d := range verificationDimensions {
			if a1[d] != a2[d] {
				changed = append(changed, d)
			}
		}
		return changeScopeChanged, "", changed
	}
	return changeConsistent, "", nil
}

// deriveVerificationChanges compares, for every publication, the ADJACENT
// surviving reports. Adjacency localises the change point and keeps the last
// change observable even when the window has been truncated.
//
// I5: a comparison is produced only when BOTH reports lie inside the surviving
// contiguous window; outside it the missing history may legally be gone.
func deriveVerificationChanges(st *verificationState) (divergent []VerificationChange, scopeChanged []VerificationChange) {
	if st == nil || !st.window.Continuous {
		return nil, nil
	}
	byPub := map[int64][]int64{}
	seqs := make([]int64, 0, len(st.entries))
	for _, e := range st.entries {
		seqs = append(seqs, e.ReportSeq)
		for _, it := range e.Items {
			byPub[it.PublicationID] = append(byPub[it.PublicationID], e.ReportSeq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	indexOf := map[int64]int{}
	for i, sq := range seqs {
		indexOf[sq] = i
	}
	pubs := make([]int64, 0, len(byPub))
	for p := range byPub {
		pubs = append(pubs, p)
	}
	sort.Slice(pubs, func(i, j int) bool { return pubs[i] < pubs[j] })

	for _, p := range pubs {
		rs := byPub[p]
		sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
		for i := 0; i+1 < len(rs); i++ {
			r1 := entryBySeq(st, rs[i])
			r2 := entryBySeq(st, rs[i+1])
			if r1 == nil || r2 == nil {
				continue
			}
			if rs[i] < st.window.MinSeq || rs[i+1] > st.window.MaxSeq {
				continue
			}
			kind, dim, changed := classifyChange(r1, r2, p)
			if kind == changeConsistent {
				continue
			}
			ch := VerificationChange{
				PublicationID:     p,
				FromReportSeq:     r1.ReportSeq,
				ToReportSeq:       r2.ReportSeq,
				Dimension:         dim,
				ChangedDimensions: changed,
				FromOverall:       overallOfEntry(r1, p),
				ToOverall:         overallOfEntry(r2, p),
				Reasons:           r2.Reasons,
			}
			switch kind {
			case changeDivergent:
				divergent = append(divergent, ch)
			case changeScopeChanged:
				scopeChanged = append(scopeChanged, ch)
			}
		}
	}
	return divergent, scopeChanged
}

func entryBySeq(st *verificationState, seq int64) *verificationLogEntry {
	for i := range st.entries {
		if st.entries[i].ReportSeq == seq {
			return &st.entries[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP surface (Phase 42, admin-only, :8082)
// ---------------------------------------------------------------------------

// handleHistoryExportVerification is the Phase 42 face.
//
//	GET  — the derived, strictly read-only view (never triggers a verification)
//	POST — record one attestation now (synchronous)
//
// Both are admin-only and answer 503 when the Phase is not enabled: a disabled
// attestation is never dressed up as an empty success.
func (s *Server) handleHistoryExportVerification(w http.ResponseWriter, r *http.Request) {
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
	if !s.historyScheduler.verificationEnabled() {
		writeError(w, http.StatusServiceUnavailable, "verification attestation is not enabled (configure --export-verify-attest with --export-sign-key and --export-trust-keys)")
		return
	}
	if r.Method == http.MethodGet {
		res, rerr := s.historyScheduler.VerificationView()
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, rerr.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !sameOriginOrFail(w, r) {
		return
	}
	if r.Body != nil {
		defer r.Body.Close()
	}
	rep, aerr := s.historyScheduler.AttestVerification(verificationSubjectLimit)
	if aerr != nil {
		writeError(w, http.StatusInternalServerError, aerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report_seq":  rep.ReportSeq,
		"overall":     rep.Overall,
		"verified_at": rep.VerifiedAt,
		"subject":     rep.Subject,
		"anchor":      rep.Anchor,
	})
}
