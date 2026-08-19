package governancepolicy

import "github.com/YuDong999/opscore/internal/governance"

// Rule value-type re-exports (Phase 17.2, ADR-036 §3.6 / §4.5.2).
//
// WHY THIS FILE EXISTS — a conflict found while implementing 17.2, stated
// plainly rather than worked around silently.
//
// ADR-036 §3.6 requires the AST forbidden-import guard to be "extended to
// include internal/governance (the Engine entry) so a Management -> Engine ->
// Execution import is a compile-time failure". Read literally as an IMPORT ban
// it is unimplementable: PolicyRecord.Rules is []governance.Rule, so any
// package that constructs a record — which internal/management must, to call
// CompareAndSave — has to name that type, and therefore has to import
// internal/governance. The ban and the mutation contract could not both hold.
//
// These aliases resolve it in the direction the ADR actually wants. They are
// ALIASES, not new types: `type Rule = governance.Rule` is the same type, the
// same values, with no conversion, no second definition and no possibility of
// divergence. Nothing about rule semantics is copied here (the package contract
// in model.go forbids that, and an alias copies nothing). What changes is
// reachability: internal/management now imports only this package, so
// governance.NewEngine and Engine.Evaluate are not merely forbidden by a test —
// they cannot be named at all. That is the compile-time failure §3.6 asked for,
// achieved literally.
//
// The alternative — letting management import internal/governance "just for the
// value type" and policing the Engine with a symbol allowlist — was rejected:
// it downgrades a compile-time guarantee to a test-time one, and §3.6 (per R75
// §7) is MUST-level architecture, not an ordinary test.
//
// Flagged for review: this adds seven exported symbols to a Phase 14 package.
// They are additive and semantics-free — no behaviour, no lifecycle, no
// storage shape changes — but they ARE new surface in a package Phase 17 was
// otherwise only supposed to extend with the two CAS primitives.
type (
	// Rule is governance.Rule, re-exported verbatim (ADR-030: this package
	// REUSES the rule value type; it never redefines it).
	Rule = governance.Rule
	// RuleKind is governance.RuleKind, re-exported verbatim. The enum stays
	// closed and owned by internal/governance.
	RuleKind = governance.RuleKind
)

// The five admitted RuleKind values, re-exported so a caller can validate
// against the CANONICAL constants instead of duplicating their string literals.
// Duplicated literals are how a closed enum quietly stops being closed.
const (
	RuleMaintenanceWindow = governance.RuleMaintenanceWindow
	RuleChangeFreeze      = governance.RuleChangeFreeze
	RuleRequireApproval   = governance.RuleRequireApproval
	RuleTenantScope       = governance.RuleTenantScope
	RuleGroupAllow        = governance.RuleGroupAllow
)
