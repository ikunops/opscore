package server

import (
	"regexp"
	"strings"
	"time"
)

// Phase 35 shared canonical snapshot-file resolver (R172/R176 Architecture).
//
// This file is the SINGLE identity authority for everything living in the
// export directory: verify discovery (manifestGroups ∪ artifactGroups) and
// prune retention grouping MUST both go through these helpers — a second
// filename parser is forbidden (R172: verify owner and retention owner must
// never drift).
//
// Identity model (frozen in R171/R175):
//   - valid identity      := <ts>[.ordinal]  where <ts> matches the P34
//     timestamp layout (trailing fractional zeros may be trimmed by Go's
//     formatter, so the fraction is OPTIONAL here) and ordinal is pure digits.
//     "T.extra" / "T.old" are NOT valid identities and therefore never form
//     their own group.
//   - registered identity wins over parent-prefix extra classification:
//     "T.1" is its own group even though "T.1.json" also has the prefix "T.".
//   - verify owner        := snapshot identity granularity (manifest-aware).
//   - retention unit      := ExportedAt granularity (R161/R162): the ordinal is
//     stripped, so "T" and "T.1" share one retention unit — both derived from
//     THIS resolver.

type snapshotFileKind int

const (
	snapshotKindIgnore   snapshotFileKind = iota // .tmp/.reserve/state/foreign files
	snapshotKindArtifact                          // alert-transitions-*.(json|csv)
	snapshotKindManifest                          // alert-transitions-*.manifest.json
)

// publicationStateFile is the scheduler-owned durable watermark for
// publication_id allocation (Phase 35 M7). It is scheduler state, never a
// snapshot artifact: it never participates in retention, verify, or listing.
const publicationStateFile = "publication-seq.state"

// validSnapshotIdentityRE matches "<ts>[.<ordinal>]". The fraction is optional
// because Go's "20060102T150405.999999999" layout trims trailing zeros.
var validSnapshotIdentityRE = regexp.MustCompile(`^\d{8}T\d{6}(?:\.\d{1,9})?Z(?:\.\d+)?$`)

// parseSnapshotFile classifies one export-directory entry and extracts its
// self-identity (the filename after the common prefix, with the format and
// ".manifest" markers stripped, collision ordinal KEPT).
func parseSnapshotFile(name string) (self string, kind snapshotFileKind) {
	if !strings.HasPrefix(name, historyExportFileBase+"-") {
		return "", snapshotKindIgnore
	}
	if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".reserve") {
		return "", snapshotKindIgnore
	}
	if name == publicationStateFile {
		return "", snapshotKindIgnore
	}
	const manifestSuffix = ".manifest.json"
	if strings.HasSuffix(name, manifestSuffix) {
		return strings.TrimSuffix(strings.TrimPrefix(name, historyExportFileBase+"-"), manifestSuffix), snapshotKindManifest
	}
	for _, ext := range []string{".json", ".csv"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(strings.TrimPrefix(name, historyExportFileBase+"-"), ext), snapshotKindArtifact
		}
	}
	return "", snapshotKindIgnore
}

// snapshotIdentityValid reports whether self is a valid snapshot identity.
func snapshotIdentityValid(self string) bool {
	return validSnapshotIdentityRE.MatchString(self)
}

// nearestValidAncestor returns the longest valid-identity prefix of self
// ("20260829T170000Z.extra" -> "20260829T170000Z"). Returns "" when self
// carries no valid-identity prefix at all (callers then treat the file as
// unattributable and report it explicitly — never silently drop it).
func nearestValidAncestor(self string) string {
	for cur := self; cur != ""; {
		if snapshotIdentityValid(cur) {
			return cur
		}
		i := strings.LastIndex(cur, ".")
		if i <= 0 {
			return ""
		}
		cur = cur[:i]
	}
	return ""
}

// snapshotTSLayout is the layout used to parse the timestamp part of a
// snapshot identity. It mirrors the P34 filename layout; the fraction is
// optional and Go trims its trailing zeros, so both "…163000Z" and
// "…163000.5Z" parse.
const snapshotTSLayout = "20060102T150405.999999999Z"

// parseSnapshotIdentity splits a valid identity into its wall-clock part and
// its collision ordinal (0 when absent). The ordinal is compared NUMERICALLY
// everywhere (R169: ".10" must not sort before ".2").
func parseSnapshotIdentity(self string) (ts time.Time, ordinal int64, ok bool) {
	body := self
	ord := int64(0)
	if i := strings.LastIndex(body, "."); i >= 0 && i+1 < len(body) {
		suffix := body[i+1:]
		if allDigits(suffix) {
			ord = parseDigits(suffix)
			body = body[:i]
		}
	}
	t, err := time.Parse(snapshotTSLayout, body)
	if err != nil {
		return time.Time{}, 0, false
	}
	return t, ord, true
}

// snapshotIdentityLess orders two valid identities by (ts, ordinal) ascending.
// The DESC paging order is the negation of this.
func snapshotIdentityLess(a, b string) bool {
	ta, oa, oka := parseSnapshotIdentity(a)
	tb, ob, okb := parseSnapshotIdentity(b)
	if !oka || !okb {
		return a < b // fallback for malformed input; both sides stay deterministic
	}
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return oa < ob
}

// snapshotGroupKeyFromIdentity strips the collision ordinal from a valid
// identity, yielding the RETENTION unit (ExportedAt granularity, R161/R162).
func snapshotGroupKeyFromIdentity(self string) string {
	if i := strings.LastIndex(self, "."); i >= 0 && i+1 < len(self) && allDigits(self[i+1:]) {
		return self[:i]
	}
	return self
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseDigits(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}
