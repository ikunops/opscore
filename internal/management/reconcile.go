package management

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/storage"
)

// defaultScanHistoryCap bounds the in-memory scan-history ring. It is a memory
// ceiling, not a retention policy: it records that scans happened, not their
// findings (which live in the audit store). Absence from the ring means "this
// many scans back", never "the trail is clean".
const defaultScanHistoryCap = 16

// Reconciler is the read-only observer over the management audit trail
// (ADR-038 §3.2). It detects orphaned INTENT rows and reports them; it NEVER
// mutates policy or audit state. The single write seam, ReconcileForward, is
// defined but NOT invoked in Phase 17.3 (ADR-021: no code until the
// attribution schema exists).
type Reconciler struct {
	audit storage.AuditStore
	repo  governancepolicy.Repository

	// scanHistory is the Phase 19 (S-4, R19-8) bounded, in-memory,
	// NON-authoritative ring of recent Scan reports. The reader is the source
	// of truth for reconciliation findings; the ring only answers "has a scan
	// run recently" so the history surface can reflect the startup pass
	// without a request. It is guarded by mu and never touches the audit store.
	mu                   sync.Mutex
	scanHistory          []ScanReport
	scanHistoryCap       int
	scanHistoryTruncated bool
}

func newReconciler(audit storage.AuditStore, repo governancepolicy.Repository) *Reconciler {
	return &Reconciler{audit: audit, repo: repo, scanHistoryCap: defaultScanHistoryCap}
}

// ReportEntry is the stable, parseable reconciliation result (ADR-038 §3.2).
type ReportEntry struct {
	CorrelationID    string `json:"correlation_id"`
	PolicyID         string `json:"policy_id"`
	IntentRevision   int    `json:"intent_revision"`
	ObservedRevision int    `json:"observed_revision"`
	// Status is one of reportClosed / reportUnresolved / reportNoMatch /
	// reportUnexaminable.
	Status string `json:"status"`
	// Resolution is a human-readable note explaining the verdict.
	Resolution string `json:"resolution"`
}

// ScanReport is the reconciliation envelope (Phase 18, ADR-040 §3.2).
//
// It exists because a bare []ReportEntry cannot answer the only question that
// matters about an empty result: was there nothing to find, or was the search
// unable to look? Status makes failure INEXPRESSIBLE as health; Window makes an
// empty Entries list interpretable.
//
//	verified  — Entries is a real answer: there are no orphaned intents.
//	truncated — Entries covers only the newest Window.Scanned rows. It says
//	            nothing whatsoever about older ones.
//	unknown   — Entries carries NO information and MUST NOT be read as evidence.
type ScanReport struct {
	Status  string        `json:"status"`
	Window  ScanWindow    `json:"window"`
	Entries []ReportEntry `json:"entries"`
}

// ScanWindow states the scope of the search. "Absence of a finding is only
// meaningful when the scope of the search is stated" is the whole of Phase 18,
// and this struct is where that sentence is enforced.
type ScanWindow struct {
	Scanned      int    `json:"scanned"`          // audit rows actually examined
	Cap          int    `json:"cap"`              // ceiling applied
	Truncated    bool   `json:"truncated"`        // matching rows exist beyond the window
	Unexaminable int    `json:"unexaminable"`     // rows whose evidence could not be read
	Reason       string `json:"reason,omitempty"` // set only when Status == scanUnknown
}

// Reconciliation status values (ADR-038 MUST-17.3-B, extended by ADR-040 §3.2).
const (
	reportClosed     = "closed"
	reportUnresolved = "unresolved"
	reportNoMatch    = "no_match"
	// reportUnexaminable marks an intent whose correlated evidence could not be
	// read. Phase 17.3 `continue`d here and the intent simply vanished from the
	// report (ADR-039 §2 F-1); the row is now REPORTED, never dropped.
	reportUnexaminable = "unexaminable"

	// Scan-level status. These are never collapsed into one another: a failed
	// read that reports "verified" is a fabricated all-clear, which is the
	// single defect this phase exists to make unrepresentable.
	scanVerified  = "verified"
	scanTruncated = "truncated"
	scanUnknown   = "unknown"

	// scanCap bounds the single-node full scan. The audit store is small and
	// operator-triggered; this is a defensive ceiling, not a paging contract.
	// Phase 18 does NOT raise it — it makes it visible. Raising it is a
	// capacity decision, not an integrity one.
	scanCap = 1000

	// correlationTailDepth is how many rows of a correlation chain are fetched
	// to look for a terminal outcome. Unchanged from Phase 17.3.
	correlationTailDepth = 4
)

// Scan is PURE READ. It never calls audit.Append and never mutates the
// repository (MUST-17.3-B / ADR-038 §3.2). It returns a ScanReport carrying one
// ReportEntry per detected condition, plus the SCOPE of the search.
//
// For every intent row it looks for a paired terminal outcome sharing the same
// correlation id. If one exists the intent is "closed" and reported as such.
// Otherwise the intent is orphaned and resolved by EVIDENCE, never inference
// (see classifyIntent).
//
// Phase 18 (ADR-040 §3.2) changes the failure contract, not the detection
// logic. Three states, never collapsed:
//
//   - the audit read fails      → scanUnknown + a non-nil error, Entries empty.
//     The error is returned IN ADDITION to the status so neither a structured
//     consumer nor a Go caller can miss it. Phase 17.3 returned a nil slice
//     here, which the handler rendered as `200 []` — a fabricated all-clear.
//   - the window hits the cap    → scanTruncated. Entries then means "nothing
//     found IN THE NEWEST Scanned ROWS", a strictly weaker claim.
//   - the window is complete     → scanVerified. Only here does an empty
//     Entries list mean "there is nothing wrong".
//
// A per-row evidence failure is NOT a scan failure: the scan itself completed,
// so Status stays verified and the affected row is reported as unexaminable.
// Per-item truth belongs on the item.
func (r *Reconciler) Scan(ctx context.Context) (ScanReport, error) {
	report := ScanReport{
		Status: scanVerified,
		Window: ScanWindow{Cap: scanCap},
		// Non-nil so an empty report marshals as [] rather than null: "I looked
		// and found nothing" must not be expressible as "I have no list".
		Entries: []ReportEntry{},
	}

	// Read one row PAST the cap so truncation is detected rather than assumed.
	// Asking for exactly scanCap makes "the trail is 1000 long" and "the trail
	// is longer than I can see" the same observation.
	events, err := r.audit.List(scanCap + 1)
	if err != nil {
		report.Status = scanUnknown
		report.Window.Reason = "audit trail unreadable: " + err.Error()
		return report, fmt.Errorf("management: reconciliation scan: %w", err)
	}
	if len(events) > scanCap {
		events = events[:scanCap]
		report.Status = scanTruncated
		report.Window.Truncated = true
	}
	report.Window.Scanned = len(events)

	for _, e := range events {
		if e.Result != resultIntent {
			continue
		}
		entry, unexaminable := r.classifyIntent(e)
		if unexaminable {
			report.Window.Unexaminable++
		}
		report.Entries = append(report.Entries, entry)
	}
	// Phase 19 S-4 (R19-8): every completed Scan is recorded in the bounded
	// ring so the history surface reflects it (including the startup pass run
	// by ScanAtStartup). This is the reconciler's OWN state — it reads only and
	// never mutates the audit store or repo, so TestReconciliationDoesNotMutate
	// stays satisfied.
	r.pushScanHistory(report)
	return report, nil
}

// ScanHistory is the Phase 19 S-4 read surface: the bounded, non-authoritative
// ring of recent Scan reports. Absence from the ring is NOT absence from
// history — a ring that has lapped (Truncated == true) has simply evicted older
// scans; the audit store still holds the findings.
type ScanHistory struct {
	Reports   []ScanReport `json:"reports"`
	Truncated bool         `json:"truncated"`
	Capacity  int          `json:"capacity"`
}

// SetScanHistoryCap resizes the ring. A reduced cap evicts the oldest reports
// and flags the ring as truncated, because older scans are now gone — the flag
// distinguishes "history evicted" from "no scan yet" (TestScanHistory*).
func (r *Reconciler) SetScanHistoryCap(n int) {
	if n < 0 {
		n = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanHistoryCap = n
	if n == 0 {
		r.scanHistory = nil
		return
	}
	if len(r.scanHistory) > n {
		r.scanHistory = r.scanHistory[len(r.scanHistory)-n:]
		r.scanHistoryTruncated = true
	}
}

// ScanHistory returns a copy of the ring so callers cannot mutate it.
func (r *Reconciler) ScanHistory() ScanHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	reports := make([]ScanReport, len(r.scanHistory))
	copy(reports, r.scanHistory)
	return ScanHistory{
		Reports:   reports,
		Truncated: r.scanHistoryTruncated,
		Capacity:  r.scanHistoryCap,
	}
}

// pushScanHistory appends a report to the ring under the lock, evicting the
// oldest (FIFO) once it laps and flagging truncation permanently.
func (r *Reconciler) pushScanHistory(rep ScanReport) {
	if r.scanHistoryCap <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanHistory = append(r.scanHistory, rep)
	if len(r.scanHistory) > r.scanHistoryCap {
		r.scanHistory = r.scanHistory[1:]
		r.scanHistoryTruncated = true
	}
}

// classifyIntent resolves ONE intent row into a report entry. The second return
// value reports whether the row's evidence was unreadable.
//
// It is a separate function on purpose. The loop in Scan must not contain an
// `if err != nil { continue }` — that shape is how an unreadable row silently
// disappears, and TestNoErrorSwallowInEvidencePath rejects it as syntax. Moving
// the per-row decision behind a value-returning call makes "every intent
// produces exactly one entry" structurally true instead of merely intended.
//
// Resolution is by EVIDENCE, never inference:
//
//   - correlated evidence unreadable → "unexaminable" (reported, not dropped)
//   - terminal outcome present       → "closed"
//   - target policy gone             → "no_match"
//   - policy still present           → "unresolved": the current schema records
//     no field linking a specific committed mutation to this correlation id, so
//     observed revision movement is NEVER taken as proof of this intent's
//     commit. The forbidden inference `observed >= intent ⇒ committed` would
//     fabricate a success the trail cannot support.
func (r *Reconciler) classifyIntent(e storage.AuditEvent) (ReportEntry, bool) {
	entry := ReportEntry{
		CorrelationID:  e.CorrelationID,
		PolicyID:       e.Target,
		IntentRevision: e.Revision,
	}

	tail, terr := r.audit.ListByCorrelation(e.CorrelationID, correlationTailDepth)
	if terr != nil {
		entry.Status = reportUnexaminable
		entry.Resolution = "correlated evidence could not be read — this intent is REPORTED as unexaminable, " +
			"never silently skipped; no conclusion may be drawn about it (R18-1)"
		return entry, true
	}

	if hasTerminalOutcome(tail) {
		// A terminal outcome already exists for this chain → not orphaned.
		entry.Status = reportClosed
		entry.Resolution = "terminal outcome already recorded"
		return entry, false
	}

	// Orphaned intent: resolve by EVIDENCE, not inference.
	rec, ok, gerr := r.repo.Get(e.Target)
	if gerr != nil || !ok {
		// Cannot attribute: target gone or lookup failed. No synthesis.
		entry.Status = reportNoMatch
		entry.Resolution = "target policy unavailable — cannot attribute; no synthesis"
		return entry, false
	}

	entry.ObservedRevision = rec.Revision
	entry.Status = reportUnresolved
	entry.Resolution = "orphaned intent; revision movement observed but not attributable to this intent — no auto-synthesis (R17-3 fail-safe, R81)"
	return entry, false
}

// ReconcileForward is the WRITE seam for future reconciliation. Phase 17.3 does
// NOT invoke it (ADR-038 §3.2 / MUST-17.3-B): the current schema has no field
// linking a committed mutation to a specific correlation id, so no outcome can
// be provably attributed and nothing is synthesized. It is therefore a no-op
// that reports, leaving the audit store byte-for-byte unchanged.
//
// When a future schema records per-revision correlation attribution, this is
// where a distinguishing success outcome (detail marker "reconciled:true")
// would be appended — and it would be covered by the same
// TestReconciliationDoesNotMutate / TestNoCASBypass AST guards as the rest of
// the surface.
func (r *Reconciler) ReconcileForward(ctx context.Context, correlationID string) ReportEntry {
	// No provable attribution exists in the Phase 17.3 schema. Report
	// unresolved and append nothing.
	return ReportEntry{
		CorrelationID: correlationID,
		Status:        reportUnresolved,
		Resolution:    "ReconcileForward is a defined-but-uninvoked seam in Phase 17.3; no attribution available",
	}
}

// ScanAtStartup runs the reconciliation scanner once, non-blocking and
// best-effort (ADR-038 §3.5). It never blocks the management listener from
// binding and never fails the process; errors are logged. It is invoked as a
// goroutine by the composition root at startup. There is no scheduler, worker,
// or controller — reconciliation is on-demand (GET) or this one startup pass.
//
// The name deliberately avoids the "Start*" prefix: TestNoExecMethod forbids
// execution-verb method names on this package (MUST, ADR-036 §3.6 discipline).
// The log line is chosen by STATUS (Phase 18, ADR-040 §3.2). A failed scan must
// never produce a clean-looking line: the startup log is the one place where a
// fabricated all-clear is guaranteed to be read by a human and then never
// questioned again.
func (s *Server) ScanAtStartup(ctx context.Context) {
	report, err := s.reconciler.Scan(ctx)
	if err != nil {
		log.Printf("management: startup reconciliation scan UNKNOWN: audit unreadable: %v — no conclusion may be drawn", err)
		return
	}
	var orphaned int
	for _, e := range report.Entries {
		if e.Status != reportClosed {
			orphaned++
		}
	}
	switch report.Status {
	case scanTruncated:
		log.Printf("management: startup reconciliation scan TRUNCATED (cap=%d): %d entries, %d non-closed — older rows NOT examined",
			report.Window.Cap, len(report.Entries), orphaned)
	default:
		log.Printf("management: startup reconciliation scan verified: %d entries, %d non-closed (unresolved/no_match/unexaminable), %d unexaminable",
			len(report.Entries), orphaned, report.Window.Unexaminable)
	}
}
