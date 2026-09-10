package server

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Phase 36 — cross-snapshot SEQ COVERAGE analysis (R186 architecture, FROZEN).
//
// Boundary vs Phase 35 verify (frozen, never crossed):
//   verify   = intra-snapshot consistency (hash / bytes / records vs manifest,
//              orphan / missing / undeclared files).
//   coverage = inter-snapshot SEQ coverage (union / merge / gaps / out-of-scope
//              / indeterminate publication holes).
// Coverage consumes ONLY manifest DECLARATIONS. It never re-hashes an artifact,
// never checks artifact presence, and never writes, deletes, or repairs
// anything: strictly read-only (M3).
//
// The pipeline is fixed and must not be re-interpreted at implementation time:
//   manifest → usable/unusable → window clip → seq union/merge →
//   observed span → gap / out_of_scope → publication indeterminate →
//   completeness precedence.

// Completeness verdicts (M4 — strictly ordered precedence).
const (
	completenessComplete    = "complete"
	completenessGapsPresent = "gaps_present"
	completenessUnknown     = "unknown"
)

// Unusable reasons (M1). `records` missing, a reversed range, or a parse
// failure all mean the REAL coverage extent is unknown.
const (
	unusableUnparseable  = "unparseable"
	unusableMissingRange = "missing_range"
	unusableInvalidRange = "invalid_range"
	unusableEmptyRange   = "empty_range"
)

// Unusable classes (M1 v2, R184). coverage_uncertain entries may hide any
// range and therefore block a definitive verdict; coverage_irrelevant entries
// are KNOWN to carry no coverage evidence.
const (
	coverageClassUncertain  = "coverage_uncertain"
	coverageClassIrrelevant = "coverage_irrelevant"
)

// CoverageInterval is a CLOSED seq interval [Min, Max] (both endpoints
// inclusive — R183 M2).
type CoverageInterval struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// CoverageWindow is the observation window the caller asked about, in seq
// space (not wall-clock time). Absent bounds default to the observed extent —
// no extrapolation is ever introduced (R183 M3).
type CoverageWindow struct {
	Since int64 `json:"since"`
	Until int64 `json:"until"`
}

// CoverageUnusable is one manifest that cannot contribute a coverage interval.
type CoverageUnusable struct {
	Snapshot      string `json:"snapshot"`
	PublicationID int64  `json:"publication_id,omitempty"`
	Reason        string `json:"reason"`
	Class         string `json:"class"`
}

// CoverageIndeterminate carries publication-id holes. A hole is NEVER
// interpreted as a deletion or loss — it is simply indeterminate (R182/C3).
type CoverageIndeterminate struct {
	PublicationGaps []CoverageInterval `json:"publication_gaps"`
}

// CoverageResult is the frozen Phase 36 result model.
type CoverageResult struct {
	Window              CoverageWindow        `json:"window"`
	SnapshotsConsidered int                   `json:"snapshots_considered"`
	Unusable            []CoverageUnusable    `json:"unusable"`
	Covered             []CoverageInterval    `json:"covered"`
	Gaps                []CoverageInterval    `json:"gaps"`
	OutOfScope          []CoverageInterval    `json:"out_of_scope"`
	Indeterminate       CoverageIndeterminate `json:"indeterminate"`
	Bounded             bool                  `json:"bounded"`
	SourceTruncated     bool                  `json:"source_truncated"`
	ProvenanceUncertain bool                  `json:"provenance_uncertain"`
	Completeness        string                `json:"completeness"`
}

// Coverage computes the cross-snapshot seq coverage over the newest `limit`
// manifest-bearing snapshot groups. `since` / `until` are optional seq bounds.
// It is strictly read-only and returns an error only when the snapshot
// directory itself cannot be read.
func (s *HistoryExportScheduler) Coverage(since, until *int64, limit int) (*CoverageResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	// Coverage consumes manifest DECLARATIONS only, so the manifest-bearing
	// discovery domain is the right one (A8-MANIFEST-1).
	groups, _, err := discoverSnapshotGroups(s.cfg.Dir, false)
	if err != nil {
		return nil, err
	}
	boundedByLimit := len(groups) > limit
	if boundedByLimit {
		groups = groups[:limit]
	}

	res := &CoverageResult{
		SnapshotsConsidered: len(groups),
		Unusable:            []CoverageUnusable{},
		Covered:             []CoverageInterval{},
		Gaps:                []CoverageInterval{},
		OutOfScope:          []CoverageInterval{},
	}

	var usable []CoverageInterval
	var knownPubIDs []int64
	uncertainRelevant := false
	var spanMin, spanMax int64
	haveUsable := false

	// ---- M1: usable / unusable classification -------------------------------
	for _, g := range groups {
		entry := CoverageUnusable{Snapshot: g.identity}
		m, reason := s.loadCoverageManifest(g)
		if m != nil {
			entry.PublicationID = m.PublicationID
			knownPubIDs = append(knownPubIDs, m.PublicationID)
		}
		if reason != "" {
			entry.Reason = reason
			if reason == unusableEmptyRange {
				entry.Class = coverageClassIrrelevant
			} else {
				// The real extent is unknown: it could fill ANY gap, so it can
				// never be dismissed (R186 final rule).
				entry.Class = coverageClassUncertain
				uncertainRelevant = true
			}
			res.Unusable = append(res.Unusable, entry)
			continue
		}
		// M1 truncated rule: the snapshot may contribute (it honestly declares
		// its own content) but the response must never claim `complete`.
		if m.Truncated {
			res.SourceTruncated = true
		}
		usable = append(usable, CoverageInterval{Min: m.MinSeq, Max: m.MaxSeq})
		if !haveUsable || m.MinSeq < spanMin {
			spanMin = m.MinSeq
		}
		if !haveUsable || m.MaxSeq > spanMax {
			spanMax = m.MaxSeq
		}
		haveUsable = true
	}

	// Publication holes are a property of the CONSIDERED set (the observed
	// publication sequence), never of the whole directory.
	res.Indeterminate.PublicationGaps = publicationGaps(knownPubIDs)

	// ---- M3: observation window --------------------------------------------
	var winSince, winUntil int64
	switch {
	case haveUsable && since == nil:
		winSince = spanMin
	case since != nil:
		winSince = *since
	}
	switch {
	case haveUsable && until == nil:
		winUntil = spanMax
	case until != nil:
		winUntil = *until
	}
	res.Window = CoverageWindow{Since: winSince, Until: winUntil}

	res.ProvenanceUncertain = uncertainRelevant

	if !haveUsable {
		// No usable evidence: no observed span exists, so the whole requested
		// window is out of scope and nothing can be asserted (M3/M4 #1).
		if winSince <= winUntil && (since != nil || until != nil) {
			res.OutOfScope = append(res.OutOfScope, CoverageInterval{Min: winSince, Max: winUntil})
		}
		res.Bounded = boundedByLimit || len(res.OutOfScope) > 0
		res.Completeness = completenessUnknown
		return res, nil
	}

	if winSince > winUntil {
		// The requested bounds do not form a window at all (only reachable when
		// a caller-supplied `since` exceeds the defaulted `until`). Report the
		// empty window honestly instead of inventing a span.
		res.Bounded = true
		res.Completeness = completenessUnknown
		return res, nil
	}

	// ---- M2: seq-axis union / merge (never time-ordered) --------------------
	merged := mergeCoverageIntervals(usable)

	// observed span ∩ window (M3 v2): inside the observed span an uncovered
	// stretch is ALWAYS a gap; only beyond the span is out_of_scope.
	spanLo, spanHi := maxI64(spanMin, winSince), minI64(spanMax, winUntil)
	if winSince < spanMin {
		// spanMin > winSince ⇒ spanMin-1 cannot underflow.
		res.OutOfScope = append(res.OutOfScope, CoverageInterval{Min: winSince, Max: spanMin - 1})
	}
	if winUntil > spanMax {
		// spanMax < winUntil ⇒ spanMax+1 cannot overflow.
		res.OutOfScope = append(res.OutOfScope, CoverageInterval{Min: spanMax + 1, Max: winUntil})
	}

	for _, iv := range merged {
		lo, hi := maxI64(iv.Min, spanLo), minI64(iv.Max, spanHi)
		if lo <= hi {
			res.Covered = append(res.Covered, CoverageInterval{Min: lo, Max: hi})
		}
	}

	// gaps = (observed span ∩ window) \ covered
	cursor := spanLo
	exhausted := false
	for _, iv := range res.Covered {
		if iv.Min > cursor {
			res.Gaps = append(res.Gaps, CoverageInterval{Min: cursor, Max: iv.Min - 1})
		}
		if iv.Max == math.MaxInt64 {
			exhausted = true
			break
		}
		if iv.Max+1 > cursor {
			cursor = iv.Max + 1
		}
	}
	if !exhausted && cursor <= spanHi {
		res.Gaps = append(res.Gaps, CoverageInterval{Min: cursor, Max: spanHi})
	}

	// `bounded` is a pure SCOPE property: it never creates a gap and never
	// downgrades a verdict on its own (R183 M3 / T53).
	res.Bounded = boundedByLimit || len(res.OutOfScope) > 0

	// ---- M4: completeness precedence (frozen order, first hit wins) --------
	switch {
	case uncertainRelevant:
		// R186 final rule: a relevant coverage_uncertain snapshot ⇒ unknown,
		// REGARDLESS of whether the current union already produced a gap. An
		// unknown extent may hide any range, so it can never be dismissed by
		// "there is no gap right now" and must never be washed into `complete`.
		res.Completeness = completenessUnknown
	case len(res.Gaps) > 0:
		res.Completeness = completenessGapsPresent
	case res.SourceTruncated:
		// Truncated source with no derivable gap cannot be called complete:
		// the underlying history admits it dropped data.
		res.Completeness = completenessUnknown
	default:
		res.Completeness = completenessComplete
	}
	return res, nil
}

// loadCoverageManifest reads and classifies one snapshot's manifest against the
// M1 usability table. A non-empty reason means the manifest is unusable; the
// manifest is still returned when it parsed, so callers can report its
// publication id for diagnostics.
func (s *HistoryExportScheduler) loadCoverageManifest(g *snapshotGroup) (*snapshotManifest, string) {
	if g.manifestFn == "" {
		return nil, unusableUnparseable
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.Dir, g.manifestFn))
	if err != nil {
		return nil, unusableUnparseable
	}
	m, perr := parseSnapshotManifest(data)
	if perr != nil {
		return nil, unusableUnparseable
	}
	// M1 checks in frozen order: missing range → invalid range → empty range.
	if m.MinSeq == 0 && m.MaxSeq == 0 {
		return m, unusableMissingRange
	}
	if m.MinSeq > m.MaxSeq {
		// A reversed range is untrustworthy as declared: never swap or guess.
		return m, unusableInvalidRange
	}
	if m.Records == 0 {
		return m, unusableEmptyRange
	}
	return m, ""
}

// mergeCoverageIntervals sorts by MinSeq ascending and unions overlapping or
// adacent CLOSED intervals on the SEQ axis (never by snapshot time — R181/B).
func mergeCoverageIntervals(in []CoverageInterval) []CoverageInterval {
	if len(in) == 0 {
		return nil
	}
	sorted := make([]CoverageInterval, len(in))
	copy(sorted, in)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Min != sorted[j].Min {
			return sorted[i].Min < sorted[j].Min
		}
		return sorted[i].Max < sorted[j].Max
	})
	out := make([]CoverageInterval, 0, len(sorted))
	out = append(out, sorted[0])
	for _, cur := range sorted[1:] {
		last := &out[len(out)-1]
		if coverageMergeable(last.Max, cur.Min) {
			if cur.Max > last.Max {
				last.Max = cur.Max
			}
			continue
		}
		out = append(out, cur)
	}
	return out
}

// coverageMergeable reports whether a closed interval beginning at nextMin
// merges with one ending at curMax. The comparison is written to be
// overflow-safe at math.MaxInt64: it never evaluates curMax+1 unless that
// addition is provably in range (frozen implementation MUST, R184/R185).
func coverageMergeable(curMax, nextMin int64) bool {
	return nextMin <= curMax || (curMax < math.MaxInt64 && nextMin == curMax+1)
}

// publicationGaps derives the indeterminate holes of an observed publication_id
// sequence (101→102→105 ⇒ [[103,104]]). Duplicates collapse; a hole is NEVER
// reported as a deletion or a loss (R182/C3).
func publicationGaps(ids []int64) []CoverageInterval {
	out := []CoverageInterval{}
	if len(ids) < 2 {
		return out
	}
	uniq := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) < 2 {
		return out
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })
	for i := 1; i < len(uniq); i++ {
		// uniq[i-1] < uniq[i] <= MaxInt64 ⇒ uniq[i-1]+1 cannot overflow.
		lo, hi := uniq[i-1]+1, uniq[i]-1
		if lo <= hi {
			out = append(out, CoverageInterval{Min: lo, Max: hi})
		}
	}
	return out
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// coverageDebugString renders a compact human summary (diagnostics only; not
// part of any wire contract).
func (r *CoverageResult) coverageDebugString() string {
	return fmt.Sprintf("window=[%d,%d] considered=%d covered=%v gaps=%v out_of_scope=%v completeness=%s",
		r.Window.Since, r.Window.Until, r.SnapshotsConsidered, r.Covered, r.Gaps, r.OutOfScope, r.Completeness)
}
