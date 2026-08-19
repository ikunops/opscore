// Package governance is the Phase 8.4 Capability (ADR-018): GOVERNANCE /
// POLICY EVALUATION — a deterministic Rule Evaluation Capability, not Policy
// Storage (Enterprise owns that, ADR-017), and not an Enterprise Workflow. It
// is the fourth and final Platform Operations capability to be implemented.
//
// Design invariant (ADR-018 MUST-1..5):
//   - EVALUATES, NOT EXECUTES: it owns evaluate(policy, state) and emits a
//     Verdict. It never runs a command and never opens its own execution path.
//     Execution still enters the Runtime via its existing interface; Governance
//     only produces a decision the Runtime (or its caller) consults.
//   - VERDICT ONLY: its single output is a Verdict (Allow / Deny /
//     RequireApproval / MaintenanceBlocked). It never emits an Action (Run /
//     Retry / Rollback / Kill / Schedule).
//   - REFERENCES IDS, OWNS NOTHING: every input is keyed by an identity the
//     frozen/accepted systems already own (PluginID / RuntimeID / Group /
//     ExecutionID / Tenant). Governance holds no copy of, and never re-records,
//     their state.
//   - DETERMINISTIC: same policy + same state ⇒ same verdict. No hidden state,
//     no clock dependence, no side-effecting evaluation. The engine is a pure
//     function over its inputs (MUST-4).
//   - COMPOSES, NEVER REPLICATES: it may consult Observability signals and
//     scope by Enterprise policy / Cluster membership, but holds no copy of
//     their behavior. An AST guard forbids Governance from importing
//     internal/plugin/runtime, internal/plugin/isolation,
//     internal/controlplane/hostregistry, internal/cluster,
//     internal/observability, or internal/enterprise. The AST guard in
//     governance_test.go enforces this.
//
// The Enterprise / Governance split (ADR-017 SHOULD-1, ADR-018 MUST-1) is
// enforced by shape: Enterprise owns *attachment* (policy metadata bound to an
// ID); Governance owns *evaluation* (compute the verdict from policy + state).
//
// Architecture (ADR-018 §3):
//
//	Policy metadata (Enterprise, ADR-017)  │ reads
//	Observed State (Runtime/Ecosystem/Cluster via Observability) │ reads
//	                     ▼
//	          Governance: evaluate(policy, state) → Verdict
//	                     │ Verdict only
//	                     ▼
//	          Runtime Existing Execution Interface → Execution
//
// Governance is a pure, deterministic evaluator; it imports no frozen
// subsystem. The Verdict is a frozen Value Object (ADR-018 SHOULD-2).
package governance

// VerdictCode is the stable output of the evaluation engine (ADR-018 MUST-2,
// SHOULD-2). It is a closed enum — a verdict is one of these four codes, never
// an action or a Command.
type VerdictCode string

const (
	// Allow: the evaluated policy permits the action.
	Allow VerdictCode = "allow"
	// Deny: the evaluated policy forbids the action.
	Deny VerdictCode = "deny"
	// RequireApproval: the action may proceed only after explicit approval.
	RequireApproval VerdictCode = "require-approval"
	// MaintenanceBlocked: the action is blocked by an active maintenance window.
	MaintenanceBlocked VerdictCode = "maintenance-blocked"
)

// Verdict is the single, frozen output of Governance (ADR-018 MUST-2,
// SHOULD-2 — Stable Verdict Contract). It is a Value Object: it is emitted,
// never mutated; its shape is fixed from day one and must never evolve
// string → interface → Action, or it would itself become a new Runtime
// Contract.
//
// Field-by-field (SHOULD-1 — Explainable Verdict):
//   - Code: the decision (Allow/Deny/RequireApproval/MaintenanceBlocked).
//   - Reason: a human-readable explanation of why this verdict was produced.
//   - PolicyID: the policy that was evaluated.
//   - RuleID: the specific rule that matched (empty when the default Allow
//     applies — i.e. no rule matched).
//   - MatchedPolicy / MatchedRule: the policy + rule that produced the verdict,
//     directly referenceable by Observability / Audit / Enterprise without
//     re-interpreting the verdict.
//   - Priority: the priority of the matched rule (0 for the default Allow).
//   - Evidence: free-form, deterministic key/value context for audit/Explain.
type Verdict struct {
	Code          VerdictCode
	Reason        string
	PolicyID      string
	RuleID        string
	MatchedPolicy string
	MatchedRule   string
	Priority      int
	Evidence      map[string]string
}

// RuleKind enumerates the deterministic rule types the engine understands. Each
// rule maps a single observed fact (or scoped ID) to a VerdictCode. Rules are
// intentionally tiny and pure — complexity lives in how policies compose them,
// not in each rule.
type RuleKind string

const (
	// RuleMaintenanceWindow: if State.InMaintenanceWindow, emit
	// MaintenanceBlocked.
	RuleMaintenanceWindow RuleKind = "maintenance-window"
	// RuleChangeFreeze: if State.ChangeFreeze, emit Deny.
	RuleChangeFreeze RuleKind = "change-freeze"
	// RuleRequireApproval: if State.RequiresApproval, emit RequireApproval.
	RuleRequireApproval RuleKind = "require-approval"
	// RuleTenantScope: if State.Tenant != Rule.Param, emit Deny (out of scope).
	RuleTenantScope RuleKind = "tenant-scope"
	// RuleGroupAllow: if State.Group == Rule.Param, emit Allow (explicit allow).
	RuleGroupAllow RuleKind = "group-allow"
)

// Rule is one deterministic evaluation clause within a Policy. It has no side
// effects and no hidden state (ADR-018 MUST-4).
type Rule struct {
	// RuleID is an opaque, policy-local handle used for Explain/Evidence.
	RuleID string
	// Priority orders rules within a policy. Higher priority wins; ties are
	// broken by RuleID ascending for stable, deterministic evaluation.
	Priority int
	// Kind selects the evaluation logic.
	Kind RuleKind
	// Param is the rule's single parameter (e.g. the required tenant or group).
	// It is empty for parameter-less rules.
	Param string
}

// Policy is a set of deterministic rules evaluated together. Governance reads
// policies (authored/owned by Enterprise, ADR-017) but does not store them.
type Policy struct {
	// PolicyID is an existing identity the frozen/accepted systems already own
	// (ADR-018 MUST-3). Governance never invents a new ID.
	PolicyID string
	// Rules are evaluated in deterministic priority order (see Evaluate).
	Rules []Rule
}

// State is the observed input Governance evaluates against. It references only
// existing IDs and observed facts — it owns nothing (ADR-018 MUST-3). State is
// the read model Governance consumes; it is supplied by the caller (typically
// assembled from Observability readings + Cluster membership).
type State struct {
	// Existing identity references (owned by the frozen/accepted systems).
	PluginID    string
	RuntimeID   string
	Group       string
	ExecutionID string
	Tenant      string

	// Observed facts (read-only signals, never execution state Governance owns).
	InMaintenanceWindow bool
	ChangeFreeze        bool
	RequiresApproval    bool
}
