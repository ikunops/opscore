package governance

import "sort"

// Engine is the deterministic policy-evaluation engine (ADR-018 MUST-1/4). It
// is intentionally stateless: it holds no policy, no state, no cache. Every
// verdict is a pure function of (Policy, State), which is what makes
// evaluation reproducible (MUST-4) and auditable.
type Engine struct{}

// NewEngine returns a stateless evaluation engine.
func NewEngine() *Engine { return &Engine{} }

// Evaluate is the single capability of Governance (ADR-018 MUST-1/2): given a
// policy and an observed state, produce a Verdict. It executes nothing and owns
// nothing — the verdict is a decision the Runtime (or its caller) consults.
//
// Determinism (MUST-4): rules are evaluated in a stable total order — by
// Priority descending, then RuleID ascending. The first matching rule wins. If
// no rule matches, the default verdict is Allow. Because the comparator is a
// total order and the rule logic is pure, identical (Policy, State) always
// yields an identical Verdict, with no hidden state, clock dependence, or side
// effects.
func (e *Engine) Evaluate(policy Policy, state State) Verdict {
	if policy.PolicyID == "" {
		// Defensive: an empty policy yields a default Allow verdict rather than
		// a panic. This is itself deterministic.
		return Verdict{
			Code:     Allow,
			Reason:   "empty policy",
			PolicyID: "",
			Evidence: map[string]string{"default": "allow", "note": "empty-policy"},
		}
	}

	rules := append([]Rule(nil), policy.Rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].RuleID < rules[j].RuleID
	})

	for _, r := range rules {
		if code, ok, reason := evalRule(r, state); ok {
			return Verdict{
				Code:          code,
				Reason:        reason,
				PolicyID:      policy.PolicyID,
				RuleID:        r.RuleID,
				MatchedPolicy: policy.PolicyID,
				MatchedRule:   r.RuleID,
				Priority:      r.Priority,
				Evidence:      buildEvidence(r, state),
			}
		}
	}

	// No rule matched → default Allow. Documented for explainability.
	return Verdict{
		Code:     Allow,
		Reason:   "no rule matched",
		PolicyID: policy.PolicyID,
		Evidence: map[string]string{"default": "allow"},
	}
}

// evalRule applies a single rule to the state. It returns the verdict code the
// rule implies, whether it matched, and a reason string. Pure function.
func evalRule(r Rule, s State) (VerdictCode, bool, string) {
	switch r.Kind {
	case RuleMaintenanceWindow:
		if s.InMaintenanceWindow {
			return MaintenanceBlocked, true, "maintenance window active"
		}
	case RuleChangeFreeze:
		if s.ChangeFreeze {
			return Deny, true, "change freeze active"
		}
	case RuleRequireApproval:
		if s.RequiresApproval {
			return RequireApproval, true, "action requires approval"
		}
	case RuleTenantScope:
		if r.Param != "" && s.Tenant != r.Param {
			return Deny, true, "tenant out of scope"
		}
	case RuleGroupAllow:
		if r.Param != "" && s.Group == r.Param {
			return Allow, true, "group allowed"
		}
	}
	return Allow, false, ""
}

// buildEvidence collects deterministic, reference-only context for the verdict.
// It copies only existing IDs and the matched rule kind/param — never any
// execution state Governance would own (ADR-018 MUST-3).
func buildEvidence(r Rule, s State) map[string]string {
	ev := map[string]string{"ruleKind": string(r.Kind)}
	if r.Param != "" {
		ev["param"] = r.Param
	}
	if s.PluginID != "" {
		ev["plugin"] = s.PluginID
	}
	if s.RuntimeID != "" {
		ev["runtime"] = s.RuntimeID
	}
	if s.Group != "" {
		ev["group"] = s.Group
	}
	if s.Tenant != "" {
		ev["tenant"] = s.Tenant
	}
	if s.ExecutionID != "" {
		ev["execution"] = s.ExecutionID
	}
	return ev
}
