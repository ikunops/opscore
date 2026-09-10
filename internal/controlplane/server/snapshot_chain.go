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
	Detail              string  `json:"detail,omitempty"`
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
// The walk goes newest→oldest and NEVER skips an unusable candidate (R199):
//
//	no manifest at all                          → (nil, nil): genesis
//	newest manifest unreadable / unparseable    → (nil, err): fail-closed
//	newest manifest is pre-chain (v2/v3)        → keep walking (not a candidate)
//	newest chain-bearing manifest, trusted      → (m, nil): extend from it
//	newest chain-bearing manifest, untrusted    → (nil, err): fail-closed
//
// A newer manifest we cannot read or parse MIGHT be the newest chain-bearing
// predecessor; treating "unreadable" as "absent" would silently roll the chain
// back to an older node and hide the break. Evidence honesty applies on the
// publish side exactly as it does on the verify side.
func latestVerifiedChainPredecessor(dir string, beforeID int64, trust *exportTrustStore) (*snapshotManifest, error) {
	groups, _, err := discoverSnapshotGroups(dir, false)
	if err != nil {
		return nil, err
	}
	// discoverSnapshotGroups returns manifests newest-first by identity.
	for _, g := range groups {
		if g.manifestFn == "" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, g.manifestFn))
		if rerr != nil {
			return nil, fmt.Errorf("newest manifest %s is unreadable (%v) — refusing to extend the chain (fail-closed)", g.identity, rerr)
		}
		m, perr := parseSnapshotManifest(data)
		if perr != nil {
			return nil, fmt.Errorf("newest manifest %s cannot be parsed (%v) — refusing to extend the chain (fail-closed)", g.identity, perr)
		}
		if m.SchemaVersion < manifestSchemaVersionV4 || m.Chain == nil {
			continue // pre-chain history is not a chain candidate; keep walking
		}
		if m.PublicationID >= beforeID {
			return nil, fmt.Errorf("manifest %d is not older than the allocated publication id %d — refusing to extend the chain (fail-closed)", m.PublicationID, beforeID)
		}
		if v := verifyManifestSignature(m, trust); v.Verdict != sigVerdictOK {
			return nil, fmt.Errorf("latest chain-bearing manifest %d cannot be trusted (%s) — refusing to extend the chain (fail-closed)", m.PublicationID, v.Verdict)
		}
		return m, nil
	}
	return nil, nil // no chain-bearing manifest on disk: this publication is the genesis
}

// verifyManifestChain evaluates the retained P38 chain over the supplied nodes
// (which MUST be the signature_ok v4 manifests of the FULL retained set — never
// a limit-truncated window, or a truncation would look like a break).
//
// `chainBearing` is how many v4 chain-bearing manifests exist on disk, which is
// NOT the same as how many we could verify. The distinction is evidence honesty
// (R196): "no chain evidence at all" and "chain evidence that cannot be
// cryptographically trusted" are different states and must not collapse.
func verifyManifestChain(nodes []chainNode, chainBearing int) (ChainVerdict, map[string]string) {
	positions := map[string]string{}
	if chainBearing == 0 {
		return ChainVerdict{Verdict: chainVerdictAbsent, Detail: "no chain-bearing manifests (pre-P38 history only)"}, positions
	}
	unverifiable := chainBearing - len(nodes)
	if len(nodes) == 0 {
		// Chain-bearing manifests EXIST — they simply cannot be trusted. That is
		// not "no chain", so it must never be reported as chain_absent.
		return ChainVerdict{
			Verdict: chainVerdictBroken,
			Detail:  fmt.Sprintf("%d chain-bearing manifest(s) exist but the predecessor chain cannot be cryptographically verified (signature not valid)", chainBearing),
		}, positions
	}
	sorted := make([]chainNode, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	v := ChainVerdict{Verdict: chainVerdictOK, AnchorPublicationID: sorted[0].id}
	if unverifiable > 0 {
		// Some chain-bearing nodes dropped out of the trusted set: the chain
		// cannot be established even if the surviving hops happen to line up.
		v.Verdict = chainVerdictBroken
		v.Detail = fmt.Sprintf("%d chain-bearing manifest(s) cannot be cryptographically verified (signature not valid)", unverifiable)
	}

	// The anchor MUST declare genesis explicitly: prev_publication_id == 0 AND no
	// predecessor digest. A first v4 node that claims a predecessor we cannot see
	// — or a digest with no predecessor id — is a BREAK; the anchor is the start
	// of the P38 chain, not "whatever file happens to be oldest after a deletion".
	if sorted[0].prevID != 0 || sorted[0].prevDigest != "" {
		v.Verdict = chainVerdictBroken
		v.BrokenAt = append(v.BrokenAt, sorted[0].id)
		if sorted[0].prevID != 0 {
			v.Detail = fmt.Sprintf("chain anchor %d commits to predecessor %d which is not retained", sorted[0].id, sorted[0].prevID)
		} else {
			v.Detail = fmt.Sprintf("chain anchor %d carries a predecessor digest without a predecessor id", sorted[0].id)
		}
		positions[sorted[0].identity] = chainPosBroken
	} else {
		positions[sorted[0].identity] = chainPosRetentionBoundary
	}

	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		// A non-genesis hop MUST carry BOTH commitments: the id AND the exact
		// canonical digest of its predecessor. An empty digest is never a
		// wildcard — chain_ok must mean every hop is id+digest bound (R197).
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
