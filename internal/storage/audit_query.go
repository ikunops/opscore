package storage

// ---------------------------------------------------------------------------
// Phase 18 — the audit query model (ADR-040 §3.1)
//
// One sentence governs this file:
//
//	Absence of a finding is only meaningful when the scope of the search is
//	stated.
//
// Phase 17.3 read the newest N rows and THEN filtered them in the handler, so a
// predicate whose only matches lived outside that window came back as an empty
// list indistinguishable from "no such events" (ADR-039 §2, defect F-3). The
// fix is not a bigger N: it is moving the predicate into the store and shipping
// the window alongside the result.
// ---------------------------------------------------------------------------

// AuditQuery is a predicate for AuditStore.Query. An empty field is a wildcard;
// every non-empty field must match exactly (conjunctive, no LIKE, no prefix, no
// regex — matching stays boring and predictable).
//
// Limit means "maximum rows RETURNED" — the ordinary meaning of the word, and
// the one Phase 17.3 violated by treating it as "rows SCANNED".
type AuditQuery struct {
	Target        string // AuditEvent.Target (policy id); "" = any
	Result        string // "success" | "failure" | "intent"; "" = any
	Action        string // frozen policy.* vocabulary; "" = any
	CorrelationID string // "" = any
	Limit         int    // <=0 → DefaultAuditQueryLimit; clamped to MaxAuditQueryLimit
	// After is the Phase 19 (ADR-042 §3.2) additive cursor. It is an EXCLUSIVE
	// upper bound on AuditEvent.ID: only rows with id < After are returned.
	// After == 0 is a wildcard (no cursor), so every existing caller that omits
	// it sees identical behaviour and NO offset paging is introduced — After is
	// stable under append, whereas OFFSET shifts as the trail grows and would
	// page a client past rows it already saw.
	After int64 // EXCLUSIVE upper bound on AuditEvent.ID; 0 = no cursor
}

// AuditPage is a query result plus the window metadata that makes a NEGATIVE
// result interpretable (R18-2). A bare slice cannot carry this, which is why
// Query does not return one.
type AuditPage struct {
	// Events is newest-first. It is always non-nil so it marshals as [] and a
	// JSON consumer never has to distinguish `null` from "found nothing".
	Events []AuditEvent `json:"events"`
	// Limit is the EFFECTIVE limit applied, after defaulting and clamping. A
	// clamp the caller cannot see is a silent truncation.
	Limit int `json:"limit"`
	// Truncated reports that more matching rows exist beyond this page. It is
	// the difference between "there are none" and "there are none HERE".
	Truncated bool `json:"truncated"`
}

// Limit bounds. Default keeps an unparameterised read cheap; Max keeps a
// hostile or careless one from turning a read into a table scan the process
// must materialise in memory.
const (
	DefaultAuditQueryLimit = 100
	MaxAuditQueryLimit     = 1000
)

// EffectiveAuditLimit resolves a requested limit to the one actually applied.
// It is exported and shared by every AuditStore implementation so the two
// stores cannot drift on the defaulting/clamping rule — the conformance test
// asserts the behaviour, this function makes the assertion cheap to keep true.
func EffectiveAuditLimit(requested int) int {
	switch {
	case requested <= 0:
		return DefaultAuditQueryLimit
	case requested > MaxAuditQueryLimit:
		return MaxAuditQueryLimit
	default:
		return requested
	}
}

// Matches reports whether e satisfies every non-empty predicate of q.
//
// This is the in-memory twin of the SQL WHERE clause built by
// sqlite.buildAuditQuery. Keeping it as one small exported-by-package function
// (rather than an inline chain of `continue`s) is what lets the conformance
// test treat "the SQL and the Go filter agree" as a property of two named
// things instead of a coincidence of two copies.
func (q AuditQuery) Matches(e AuditEvent) bool {
	if q.Target != "" && e.Target != q.Target {
		return false
	}
	if q.Result != "" && e.Result != q.Result {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if q.CorrelationID != "" && e.CorrelationID != q.CorrelationID {
		return false
	}
	return true
}
