package server

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Phase 39 — chain digest ledger: a compact, retention-decoupled trusted root.
//
// The problem it solves: Phase 38's chain proof requires the committed
// predecessor to still be ON DISK, while P34-I6 bounds how much we keep. A legal
// prune therefore used to look exactly like a deletion (chain_broken). The
// ledger keeps the per-node commitments in a tiny append-only file that is NOT
// governed by the snapshot retention policy, so bounded local retention no
// longer destroys the chain proof.
//
// Frozen boundaries (R203/R204):
//   - The ledger makes NO claim about its own history. A missing prefix only
//     defines `verifiable_from` (where the currently verifiable range starts).
//     Prefix eviction and prefix deletion are INDISTINGUISHABLE, and neither is
//     ever reported as tampering.
//   - Commit order: manifest is published first (the only success point), the
//     ledger entry is appended afterwards. Ledger-first is forbidden — it would
//     create phantom publication evidence.
//   - Recovery may only sign a ledger entry from a manifest whose P37 signature
//     verifies; a tampered manifest must never be "washed" into valid ledger
//     evidence.
//   - A publication_id is logically unique in the ledger: same id + same digest
//     is an idempotent no-op, same id + different digest is a CONFLICT (never a
//     silent last-write-wins).

const chainLedgerFile = "chain-ledger.jsonl"

// ledgerEntry is one signed chain-node commitment.
type ledgerEntry struct {
	PublicationID      int64           `json:"publication_id"`
	ManifestDigest     string          `json:"manifest_digest"`
	PrevPublicationID  int64           `json:"prev_publication_id"`
	PrevManifestDigest string          `json:"prev_manifest_digest,omitempty"`
	RecordedAt         string          `json:"recorded_at"`
	Signature          *signatureBlock `json:"signature,omitempty"`
}

func serializeLedgerEntry(w io.Writer, e *ledgerEntry) error {
	enc := json.NewEncoder(w)
	return enc.Encode(e)
}

func serializeLedgerEntryBytes(e *ledgerEntry) ([]byte, error) {
	var buf strings.Builder
	if err := serializeLedgerEntry(&buf, e); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// canonicalLedgerPayload mirrors the Phase 37 model exactly: every business
// field plus signature.{alg,key_id,signed_at}, with only `sig` excluded.
func canonicalLedgerPayload(e *ledgerEntry) ([]byte, error) {
	if e == nil {
		return nil, errors.New("nil ledger entry")
	}
	payload := *e
	if e.Signature != nil {
		sb := *e.Signature
		sb.Sig = ""
		payload.Signature = &sb
	}
	return serializeLedgerEntryBytes(&payload)
}

// signLedgerEntry fills the signature block (Ed25519, same key as manifests).
func (s *exportSigner) signLedgerEntry(e *ledgerEntry, at time.Time) error {
	if s == nil {
		return errors.New("no signer configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("sign key: private key is not a valid Ed25519 key")
	}
	if e.Signature == nil {
		e.Signature = &signatureBlock{}
	}
	e.Signature.Alg = signatureAlgEd25519
	e.Signature.KeyID = s.keyID
	e.Signature.SignedAt = at.UTC().Format(time.RFC3339Nano)
	e.Signature.Sig = ""
	payload, err := canonicalLedgerPayload(e)
	if err != nil {
		return err
	}
	e.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyLedgerEntrySignature applies the frozen Phase 37 decision order to a
// ledger entry (absent / malformed / key_unknown / invalid / ok).
func verifyLedgerEntrySignature(e *ledgerEntry, trust *exportTrustStore) SignatureVerdict {
	if e == nil || e.Signature == nil {
		return SignatureVerdict{Verdict: sigVerdictAbsent, Detail: "ledger entry carries no signature"}
	}
	sb := e.Signature
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "ledger signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "ledger signature is not valid base64"}
	}
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted public keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "ledger key_id is not in the trusted key set"}
	}
	payload, perr := canonicalLedgerPayload(e)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "ledger signature does not verify against the trusted key"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// ledgerState is the read-only view the verifier works from.
type ledgerState struct {
	usable    map[int64]ledgerEntry // signature_ok entries, id -> entry
	unusable  []ledgerProblem       // entries that exist but cannot be trusted
	conflicts map[int64]bool        // ids carrying contradictory digests
	total     int
}

type ledgerProblem struct {
	PublicationID int64  `json:"publication_id"`
	Verdict       string `json:"verdict"`
	Detail        string `json:"detail,omitempty"`
}

// loadLedgerState reads the ledger and classifies every entry. Strictly
// read-only: nothing here writes, repairs, or compacts.
func loadLedgerState(dir string, trust *exportTrustStore) (*ledgerState, error) {
	ls := &ledgerState{usable: map[int64]ledgerEntry{}, conflicts: map[int64]bool{}}
	f, err := os.Open(filepath.Join(dir, chainLedgerFile))
	if err != nil {
		if os.IsNotExist(err) {
			return ls, nil // no ledger yet: absence is not an error
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	seen := map[int64]string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e ledgerEntry
		if jerr := json.Unmarshal([]byte(line), &e); jerr != nil {
			ls.unusable = append(ls.unusable, ledgerProblem{Verdict: sigVerdictMalformed, Detail: "ledger line is not valid JSON: " + jerr.Error()})
			continue
		}
		ls.total++
		if prevDigest, dup := seen[e.PublicationID]; dup {
			if prevDigest != e.ManifestDigest {
				// A publication_id is logically unique. Contradictory digests are
				// a CONFLICT, never a silent last-write-wins.
				ls.conflicts[e.PublicationID] = true
				delete(ls.usable, e.PublicationID)
			}
			continue
		}
		seen[e.PublicationID] = e.ManifestDigest
		if v := verifyLedgerEntrySignature(&e, trust); v.Verdict != sigVerdictOK {
			ls.unusable = append(ls.unusable, ledgerProblem{PublicationID: e.PublicationID, Verdict: v.Verdict, Detail: v.Detail})
			continue
		}
		ls.usable[e.PublicationID] = e
	}
	if serr := scanner.Err(); serr != nil {
		return nil, serr
	}
	return ls, nil
}

// ledgerRawLine is one physical line of the ledger, preserved verbatim when it
// cannot be parsed (exceptional evidence must never be silently discarded).
type ledgerRawLine struct {
	raw   string
	entry *ledgerEntry // nil when the line is not parseable
}

func readLedgerRawLines(dir string) ([]ledgerRawLine, error) {
	path := filepath.Join(dir, chainLedgerFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return scanLedgerRawLines(f)
}

func scanLedgerRawLines(r io.Reader) ([]ledgerRawLine, error) {
	var out []ledgerRawLine
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		rl := ledgerRawLine{raw: strings.TrimSpace(text)}
		var e ledgerEntry
		if jerr := json.Unmarshal([]byte(rl.raw), &e); jerr == nil {
			rl.entry = &e
		}
		out = append(out, rl)
	}
	return out, scanner.Err()
}

// appendLedgerEntry adds one entry. Appending and capacity compaction are TWO
// separate operations (R205):
//
//   - a normal append performs a TRUE append: existing bytes are never rewritten;
//   - a malformed line makes the append FAIL-CLOSED instead of quietly
//     rebuilding a "clean" ledger around it — exceptional evidence must never
//     disappear as a side effect of recording something new;
//   - the only rewrite is an explicit, standalone PREFIX compaction that drops
//     the oldest whole lines and preserves every surviving line verbatim.
//
//	same id + same digest     → idempotent no-op
//	same id + different digest→ error (conflict; nothing is written)
func appendLedgerEntry(dir string, capacity int, e ledgerEntry) error {
	path := filepath.Join(dir, chainLedgerFile)
	lines, err := readLedgerRawLines(dir)
	if err != nil {
		return err
	}
	unparseable := 0
	for i := range lines {
		rl := lines[i]
		if rl.entry == nil {
			unparseable++
			continue
		}
		if rl.entry.PublicationID != e.PublicationID {
			continue
		}
		if sameLedgerIdentity(*rl.entry, e) {
			return nil // idempotent
		}
		return fmt.Errorf("ledger conflict: publication_id %d already recorded with a different digest", e.PublicationID)
	}
	if unparseable > 0 {
		return fmt.Errorf("ledger holds %d unparseable line(s); refusing to append — rewriting the file would silently discard that evidence", unparseable)
	}

	raw, merr := serializeLedgerEntryBytes(&e)
	if merr != nil {
		return merr
	}
	// TRUE append: nothing already on disk is rewritten.
	f, oerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if oerr != nil {
		return oerr
	}
	if _, werr := f.Write(raw); werr != nil {
		f.Close()
		return werr
	}
	if serr := f.Sync(); serr != nil {
		f.Close()
		return serr
	}
	if cerr := f.Close(); cerr != nil {
		return cerr
	}

	if capacity > 0 {
		return compactLedgerPrefix(path, capacity)
	}
	return nil
}

// compactLedgerPrefix drops only the OLDEST whole lines until at most `keep`
// remain, preserving every surviving line byte-for-byte. It never reinterprets,
// repairs, or reorders entries.
func compactLedgerPrefix(path string, keep int) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines, serr := scanLedgerRawLines(f)
	f.Close()
	if serr != nil {
		return serr
	}
	if len(lines) <= keep {
		return nil
	}
	kept := lines[len(lines)-keep:]

	tmp := path + ".tmp"
	out, oerr := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if oerr != nil {
		return oerr
	}
	for _, l := range kept {
		if _, werr := io.WriteString(out, l.raw+"\n"); werr != nil {
			out.Close()
			os.Remove(tmp)
			return werr
		}
	}
	if serr := out.Sync(); serr != nil {
		out.Close()
		os.Remove(tmp)
		return serr
	}
	if cerr := out.Close(); cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		os.Remove(tmp)
		return rerr
	}
	return nil
}

// sameLedgerIdentity reports whether two entries describe the same node state.
func sameLedgerIdentity(a, b ledgerEntry) bool {
	return a.PublicationID == b.PublicationID &&
		a.ManifestDigest == b.ManifestDigest &&
		a.PrevPublicationID == b.PrevPublicationID &&
		a.PrevManifestDigest == b.PrevManifestDigest
}

// ledgerDigestOf computes the compact commitment recorded for a manifest. It
// reuses the Phase 37 canonical payload, so no second canonical form exists.
func ledgerDigestOf(m *snapshotManifest) (string, error) {
	payload, err := canonicalSignaturePayload(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// entryForManifest builds the ledger entry describing a published manifest.
func entryForManifest(m *snapshotManifest, recordedAt time.Time) (ledgerEntry, error) {
	dg, err := ledgerDigestOf(m)
	if err != nil {
		return ledgerEntry{}, err
	}
	e := ledgerEntry{
		PublicationID:     m.PublicationID,
		ManifestDigest:    dg,
		PrevPublicationID: 0,
		RecordedAt:        recordedAt.UTC().Format(time.RFC3339Nano),
	}
	if m.Chain != nil {
		e.PrevPublicationID = m.Chain.PrevPublicationID
		e.PrevManifestDigest = m.Chain.PrevManifestDigest
	}
	return e, nil
}
