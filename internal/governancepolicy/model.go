// Package governancepolicy implements Phase 14.2 Governance Policy Persistence
// (ADR-029/030). It is the SINGLE owner of policy persistence (B6): the frozen
// governance.Engine owns evaluation, not storage (ADR-017/018), and this package
// supplies exactly the persistence the engine deliberately omitted.
//
// Invariants (ADR-029 B1–B9, ADR-030 MUST-0–6 / 13.x):
//   - Imports ONLY internal/governance (for the Rule VALUE TYPE), internal/platformview,
//     internal/correlation (reader contracts). It never imports the engine execution path.
//   - PolicyID reuses the EXISTING governance identity (B8) — no new global identity.
//   - Revision is a version attribute (B8), bumped on each Save of an existing PolicyID.
//   - Rules REUSE governance.Rule verbatim; this package copies no rule semantics.
//   - Lifecycle (lifecycle.go) manages Status WITHOUT calling Engine.Evaluate (B7).
//   - Reader (reader.go) projects persisted rules into the read contracts; Verdicts are
//     honestly empty (nil) because a Verdict needs observed State this layer never holds
//     (B2/B7).
//   - NO execution bridge: persistence never triggers Execute/Run/Apply/Schedule/Rollback
//     (B9). The mechanical guards in guards_test.go enforce this.
package governancepolicy

import (
	"time"

	"github.com/YuDong999/opscore/internal/governance"
)

// PolicyStatus is the lifecycle state of a persisted policy (ADR-030 B7:
// Lifecycle separated from Evaluation). It is a closed enum over the
// repository-owned lifecycle — distinct from, and never deriving, the engine's
// Verdict.
type PolicyStatus string

const (
	// StatusDraft: authored but not yet active; not consulted by evaluation.
	StatusDraft PolicyStatus = "draft"
	// StatusActive: currently in force; the engine may evaluate against it.
	StatusActive PolicyStatus = "active"
	// StatusArchived: retired; no longer evaluated, retained for audit only.
	StatusArchived PolicyStatus = "archived"
)

// PolicyRecord is the persisted governance policy entity (ADR-029/030, Phase 14).
// It is the SINGLE owner of policy persistence (B6). Field-by-field:
//   - PolicyID  reuses the EXISTING governance identity (B8 — no new global id).
//   - Revision  is a version attribute (B8), bumped on each Save of a PolicyID.
//   - Status    tracks the lifecycle (B7), managed by lifecycle.go without invoking
//     the engine.
//   - Rules     REUSE governance.Rule verbatim (no copy of rule semantics).
//   - CreatedAt/UpdatedAt/ActivatedAt are lifecycle timestamps (B7).
//   - It holds NO Verdict: a Verdict requires observed State the repository never
//     has, and Governance owns evaluation, not this store (B2/B7).
type PolicyRecord struct {
	PolicyID    string
	Revision    int
	Status      PolicyStatus
	Rules       []governance.Rule
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ActivatedAt time.Time
}
