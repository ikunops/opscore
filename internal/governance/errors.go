package governance

import "errors"

// Governance emits verdicts, not errors — evaluation never fails. These errors
// are reserved for callers passing malformed input (e.g. an empty policy id),
// and are distinct from execution failures (which never originate here,
// ADR-018 MUST-2).
var (
	// ErrPolicyEmpty is returned when a policy carries no PolicyID. The engine
	// tolerates this at evaluate time (default Allow), but callers may validate
	// eagerly before attaching a verdict to an audit trail.
	ErrPolicyEmpty = errors.New("governance: policy has empty PolicyID")
)

// ValidatePolicy checks that a policy carries the existing ID it references
// (ADR-018 MUST-3). It performs no evaluation and has no side effects.
func ValidatePolicy(p Policy) error {
	if p.PolicyID == "" {
		return ErrPolicyEmpty
	}
	return nil
}
