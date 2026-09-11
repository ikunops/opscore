package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Phase 38 — publication-chain continuity evidence (R195 freeze).
//
// New question this answers: P35 proves one snapshot is internally consistent,
// P36 proves seq coverage, P37 proves one manifest's provenance — but NONE of
// them can tell "103 was never published" apart from "103 was published and then
// deleted", because on disk those two histories look identical.
//
// The chain closes that gap: every manifest commits (inside its P37 signature)
// to the id AND digest of the manifest it actually followed. Two histories that
// are indistinguishable on disk therefore carry DIFFERENT signed commitments:
//
//	crash gap   101,102,104  with 104.prev = 102   → 102 is the retained predecessor → chain_ok
//	deletion    101,102,104  with 104.prev = 103   → 103 is missing but committed   → chain_broken
//
// Frozen boundaries (R194/R195):
//   - `prev_publication_id` points at the last manifest that ACTUALLY PUBLISHED,
//     never at `publication_id-1`; so a legal crash gap is not a chain gap.
//   - P38 verifies v4→v4 only. Manifests predating P38 (v2/v3) are pre-chain
//     history and contribute `chain_absent` — they are never asked to satisfy a
//     chain they cannot express.
//   - The FIRST v4 manifest is the chain anchor and declares genesis explicitly
//     with `prev_publication_id == 0`. A v4 node that instead claims an
//     unresolvable predecessor is a BREAK, not a boundary: the anchor is bound
//     to the real start of the P38 chain, never to "whichever file happens to be
//     oldest right now" (that would silently swallow a deleted first link).
//   - chain_broken means "a cryptographically committed predecessor cannot be
//     satisfied": retained evidence disagrees with the signed publication chain.
//     It does NOT claim "someone deleted it" — the cause is event interpretation.
//   - The chain verdict is a THIRD ORTHOGONAL dimension. It never modifies the
//     Phase 35 status, the Phase 37 signature verdict, or any Phase 36 field.

const (
	chainVerdictOK     = "chain_ok"
	chainVerdictBroken = "chain_broken"
	chainVerdictAbsent = "chain_absent"

	// per-snapshot chain positions
	chainPosPredecessorVerified = "predecessor_verified"
	chainPosRetentionBoundary   = "retention_boundary"
	chainPosBroken              = "broken"
	chainPosAbsent              = "absent"
)

// manifestChain is the v4 chain commitment. Both fields are manifest business
// fields, so the Phase 37 signature payload covers them automatically.
type manifestChain struct {
	PrevPublicationID  int64  `json:"prev_publication_id"`
	PrevManifestDigest string `json:"prev_manifest_digest,omitempty"`
}

// ChainVerdict is the aggregate Phase 38 result (the third orthogonal dimension).
type ChainVerdict struct {
	Verdict             string  `json:"verdict"`
	AnchorPublicationID int64   `json:"anchor_publication_id,omitempty"`
	BrokenAt            []int64 `json:"broken_at,omitempty"`
	// Phase 39: the oldest publication_id whose chain position is currently
	// PROVEN. Anything older lies outside the verifiable range — a shrink, and
	// emphatically not a tampering claim (prefix eviction and prefix deletion
	// are indistinguishable).
	VerifiableFromPublicationID int64 `json:"verifiable_from_publication_id,omitempty"`
	// Phase 39: how many ledger entries the backtrack actually consumed.
	LedgerEntriesUsed int `json:"ledger_entries_used,omitempty"`
	// Phase 39: ledger entries that exist but cannot be trusted (untrusted
	// signature, conflicting duplicate id). Exposed, never silently ignored.
	LedgerErrors []string `json:"ledger_errors,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

// manifestDigest is the canonical digest used both for chain commitments and
// for comparing a predecessor. It reuses the EXACT Phase 37 signing payload
// bytes (manifest business fields + signature.{alg,key_id,signed_at}, minus
// signature.sig), so no second canonical form is introduced.
func manifestDigest(m *snapshotManifest) (string, error) {
	payload, err := canonicalSignaturePayload(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// chainNode is one v4 manifest reduced to what chain verification needs.
type chainNode struct {
	id         int64
	digest     string
	prevID     int64
	prevDigest string
	identity   string
}

// latestVerifiedChainPredecessor returns the chain-bearing manifest this
// publication must extend, or an error when the chain cannot be extended.
//
// TWO INDEPENDENT AXES are at work here (R200) and are never mixed:
//
//   - the DIRECTORY order is the snapshot identity (ts, ordinal) order, a
//     storage concern used only to find files;
//   - the PREDECESSOR is defined on the PUBLICATION-ID axis: "the last manifest
//     that actually published". A snapshot's timestamp may move backwards, so
//     the selection takes the MAXIMUM publication_id among readable candidates.
//
// An UNREADABLE / UNPARSEABLE manifest has no knowable publication_id, so it
// cannot be ordered on the publication axis at all (R201). It must therefore
// never be judged "older" via its identity — that would be exactly the
// cross-axis inference R200 forbids — and the publication is REFUSED:
//
//	readable   → publication axis decides (maximum id), fail-closed on any
//	             candidate that is not older than the allocated id or that is
//	             not signature_ok
//	unreadable → publication_id unknown → not orderable → fail-closed
func latestVerifiedChainPredecessor(dir string, beforeID int64, trust *exportTrustStore) (*snapshotManifest, error) {
	groups, _, err := discoverSnapshotGroups(dir, false)
	if err != nil {
		return nil, err
	}
	var candidates []*snapshotManifest
	for _, g := range groups {
		if g.manifestFn == "" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, g.manifestFn))
		if rerr != nil {
			return nil, fmt.Errorf("manifest %s is unreadable (%v) — its publication position cannot be established, refusing to extend the chain (fail-closed)", g.identity, rerr)
		}
		m, perr := parseSnapshotManifest(data)
		if perr != nil {
			return nil, fmt.Errorf("manifest %s cannot be parsed (%v) — its publication position cannot be established, refusing to extend the chain (fail-closed)", g.identity, perr)
		}
		if m.SchemaVersion < manifestSchemaVersionV4 || m.Chain == nil {
			continue // pre-chain history is not a chain candidate
		}
		if m.PublicationID >= beforeID {
			return nil, fmt.Errorf("manifest %d is not older than the allocated publication id %d — refusing to extend the chain (fail-closed)", m.PublicationID, beforeID)
		}
		candidates = append(candidates, m)
	}

	var best *snapshotManifest
	for _, m := range candidates {
		if best == nil || m.PublicationID > best.PublicationID {
			best = m
		}
	}
	if best == nil {
		return nil, nil // no chain-bearing manifest: this publication is the genesis
	}
	if v := verifyManifestSignature(best, trust); v.Verdict != sigVerdictOK {
		return nil, fmt.Errorf("latest chain-bearing manifest %d cannot be trusted (%s) — refusing to extend the chain (fail-closed)", best.PublicationID, v.Verdict)
	}
	return best, nil
}

// verifyManifestChain evaluates the retained P38 chain over the supplied nodes
// (which MUST be the signature_ok v4 manifests of the FULL retained set — never
// a limit-truncated window, or a truncation would look like a break).
//
// `chainBearing` is how many v4 chain-bearing manifests exist on disk, which is
// NOT the same as how many we could verify. The distinction is evidence honesty
// (R196): "no chain evidence at all" and "chain evidence that cannot be
// cryptographically trusted" are different states and must not collapse.
// verifyChainWithLedger evaluates the retained Phase 38 chain AND, for the
// region retention has already reclaimed, the Phase 39 ledger.
//
// `chainBearing` is how many v4 chain-bearing manifests exist on disk, which is
// NOT the same as how many we could verify (R196 evidence honesty).
//
// Ledger backtracking (R203/R204):
//   - the retained hops are verified exactly as in Phase 38 (rules unchanged);
//   - the boundary BELOW the oldest retained node is then walked backwards
//     through the ledger, using only entries actually referenced, so phantom or
//     orphan entries can never participate;
//   - a matching entry extends the verified range; a digest mismatch, an
//     untrusted entry, a conflicting id or a cycle is a BREAK;
//   - running out of entries simply ENDS the verifiable range. That is a
//     shrink, never a break: prefix eviction and prefix deletion are
//     indistinguishable and neither is ever reported as tampering.
func verifyChainWithLedger(nodes []chainNode, chainBearing int, ls *ledgerState) (ChainVerdict, map[string]string) {
	positions := map[string]string{}
	if ls == nil {
		ls = &ledgerState{usable: map[int64]ledgerEntry{}, conflicts: map[int64]bool{}}
	}
	problemsByID := map[int64]ledgerProblem{}
	for _, p := range ls.unusable {
		problemsByID[p.PublicationID] = p
	}
	collectLedgerErrors := func() []string {
		var out []string
		for _, p := range ls.unusable {
			out = append(out, fmt.Sprintf("ledger entry %d: %s (%s)", p.PublicationID, p.Verdict, p.Detail))
		}
		for id := range ls.conflicts {
			out = append(out, fmt.Sprintf("ledger entry %d: conflicting digests recorded for the same publication_id", id))
		}
		sort.Strings(out)
		return out
	}

	if chainBearing == 0 {
		return ChainVerdict{Verdict: chainVerdictAbsent, Detail: "no chain-bearing manifests (pre-P38 history only)"}, positions
	}
	unverifiable := chainBearing - len(nodes)
	if len(nodes) == 0 {
		return ChainVerdict{
			Verdict:      chainVerdictBroken,
			LedgerErrors: collectLedgerErrors(),
			Detail:       fmt.Sprintf("%d chain-bearing manifest(s) exist but the predecessor chain cannot be cryptographically verified (signature not valid)", chainBearing),
		}, positions
	}
	sorted := make([]chainNode, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	v := ChainVerdict{Verdict: chainVerdictOK, AnchorPublicationID: sorted[0].id}
	v.LedgerErrors = collectLedgerErrors()
	if unverifiable > 0 {
		v.Verdict = chainVerdictBroken
		v.Detail = fmt.Sprintf("%d chain-bearing manifest(s) cannot be cryptographically verified (signature not valid)", unverifiable)
	}

	// ---- boundary below the oldest retained node ---------------------------
	switch {
	case sorted[0].prevID == 0 && sorted[0].prevDigest == "":
		// Explicit genesis.
		positions[sorted[0].identity] = chainPosRetentionBoundary
		v.VerifiableFromPublicationID = sorted[0].id
	case sorted[0].prevID == 0:
		// A digest with no predecessor id is a malformed anchor.
		v.Verdict = chainVerdictBroken
		v.BrokenAt = append(v.BrokenAt, sorted[0].id)
		positions[sorted[0].identity] = chainPosBroken
		if v.Detail == "" {
			v.Detail = fmt.Sprintf("chain anchor %d carries a predecessor digest without a predecessor id", sorted[0].id)
		}
	default:
		if ls.total == 0 {
			// NO ledger at all (pre-P39 deployment): Phase 38 semantics apply —
			// an anchor committing to a predecessor nobody can produce is a break.
			v.Verdict = chainVerdictBroken
			v.BrokenAt = append(v.BrokenAt, sorted[0].id)
			positions[sorted[0].identity] = chainPosBroken
			if v.Detail == "" {
				v.Detail = fmt.Sprintf("chain anchor %d commits to predecessor %d which is not retained", sorted[0].id, sorted[0].prevID)
			}
			break
		}
		// A ledger exists, so the boundary below the oldest retained node may be
		// provable: walk it backwards.
		from, used, brokenDetail := backtrackLedger(ls, problemsByID, sorted[0].prevID, sorted[0].prevDigest)
		v.LedgerEntriesUsed += used
		switch {
		case brokenDetail != "":
			v.Verdict = chainVerdictBroken
			v.BrokenAt = append(v.BrokenAt, sorted[0].id)
			positions[sorted[0].identity] = chainPosBroken
			if v.Detail == "" {
				v.Detail = brokenDetail
			}
		case used > 0:
			positions[sorted[0].identity] = chainPosRetentionBoundary
			v.VerifiableFromPublicationID = from
		default:
			positions[sorted[0].identity] = chainPosRetentionBoundary
			v.VerifiableFromPublicationID = sorted[0].id
		}
	}

	// A publication_id is ONE logical node (R204): when the disk manifest and its
	// ledger entry disagree about the digest, the node has been replaced. A
	// CONFLICTING duplicate id is likewise a break — never a silent
	// last-write-wins.
	for _, n := range sorted {
		if ls.conflicts[n.id] {
			v.Verdict = chainVerdictBroken
			v.BrokenAt = append(v.BrokenAt, n.id)
			positions[n.identity] = chainPosBroken
			if v.Detail == "" {
				v.Detail = fmt.Sprintf("ledger records conflicting digests for publication_id %d", n.id)
			}
			continue
		}
		e, ok := ls.usable[n.id]
		if !ok || e.ManifestDigest == n.digest {
			continue
		}
		v.Verdict = chainVerdictBroken
		v.BrokenAt = append(v.BrokenAt, n.id)
		positions[n.identity] = chainPosBroken
		if v.Detail == "" {
			v.Detail = fmt.Sprintf("manifest %d and its ledger entry record different digests", n.id)
		}
	}

	// ---- hop-by-hop over the retained set (Phase 38 rules, unchanged) -------
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if cur.prevID == prev.id && cur.prevDigest == prev.digest {
			positions[cur.identity] = chainPosPredecessorVerified
			continue
		}
		v.Verdict = chainVerdictBroken
		v.BrokenAt = append(v.BrokenAt, cur.id)
		positions[cur.identity] = chainPosBroken
		if v.Detail == "" {
			switch {
			case cur.prevID != prev.id:
				v.Detail = fmt.Sprintf("manifest %d commits to predecessor %d but the retained predecessor is %d", cur.id, cur.prevID, prev.id)
			case cur.prevDigest == "":
				v.Detail = fmt.Sprintf("manifest %d commits to predecessor %d without a predecessor digest", cur.id, cur.prevID)
			default:
				v.Detail = fmt.Sprintf("manifest %d commits to a predecessor digest that does not match %d", cur.id, prev.id)
			}
		}
	}
	return v, positions
}

// backtrackLedger walks the ledger backwards from a committed predecessor.
//
// It returns the oldest proven publication_id, how many entries were used, and
// a non-empty detail when the walk proves a BREAK (digest mismatch, untrusted
// entry, conflicting id, or a cycle). Running out of entries is NOT a break.
func backtrackLedger(ls *ledgerState, problems map[int64]ledgerProblem, id int64, digest string) (int64, int, string) {
	if ls == nil {
		return 0, 0, ""
	}
	if len(ls.usable) == 0 && len(problems) == 0 && len(ls.conflicts) == 0 {
		return 0, 0, "" // no ledger at all: nothing provable below this node
	}
	oldest := int64(0)
	used := 0
	curID, curDigest := id, digest
	for curID != 0 {
		if used > len(ls.usable)+len(problems)+1 {
			return oldest, used, fmt.Sprintf("ledger walk from %d exceeds the recorded entry count (cycle or corruption)", id)
		}
		if ls.conflicts[curID] {
			return oldest, used, fmt.Sprintf("ledger records conflicting digests for publication_id %d", curID)
		}
		e, ok := ls.usable[curID]
		if !ok {
			if p, bad := problems[curID]; bad {
				return oldest, used, fmt.Sprintf("ledger entry %d is not trustworthy (%s)", curID, p.Verdict)
			}
			// Entry unavailable (evicted, or never recorded): the verifiable
			// range ends here — a shrink, not a tampering claim.
			return oldest, used, ""
		}
		if curDigest != "" && e.ManifestDigest != curDigest {
			return oldest, used, fmt.Sprintf("ledger entry %d records a digest that does not match the committed predecessor", curID)
		}
		used++
		oldest = e.PublicationID
		if e.PrevPublicationID != 0 && e.PrevManifestDigest == "" {
			return oldest, used, "" // cannot continue along a digest-less commitment
		}
		curID, curDigest = e.PrevPublicationID, e.PrevManifestDigest
	}
	return oldest, used, ""
}
