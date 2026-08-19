package governancepolicy

import (
	"time"

	"github.com/YuDong999/opscore/internal/governance"
)

// Lifecycle management (ADR-030 B7 — separated from Evaluation). Every function
// below operates ONLY on the Repository (persistence). None of them invoke
// governance.Engine.Evaluate, and this file imports only the governance.Rule
// VALUE TYPE (never the engine execution path). This keeps the persistence
// lifecycle a pure repository write — forming NO execution bridge (B9).
//
// The mechanical guard TestNoEngineEval enforces that this package never
// constructs or calls the engine.

// Create authors a brand-new policy as a draft and persists its first revision.
// rules REUSE governance.Rule verbatim (no copy of rule semantics). It is a pure
// repository write; it never evaluates.
func Create(repo Repository, policyID string, rules []governance.Rule) (PolicyRecord, error) {
	if policyID == "" {
		return PolicyRecord{}, ErrInvalidID
	}
	rev, err := repo.NextRevision(policyID)
	if err != nil {
		return PolicyRecord{}, err
	}
	rec := PolicyRecord{
		PolicyID:  policyID,
		Revision:  rev,
		Status:    StatusDraft,
		Rules:     append([]governance.Rule(nil), rules...),
		CreatedAt: time.Now(),
	}
	return repo.Save(rec)
}

// Activate sets a policy active (in force). Lifecycle only — no engine call.
func Activate(repo Repository, policyID string) (PolicyRecord, error) {
	if err := repo.Activate(policyID); err != nil {
		return PolicyRecord{}, err
	}
	rec, _, err := repo.Get(policyID)
	return rec, err
}

// Deactivate demotes a policy to draft (not currently evaluated).
func Deactivate(repo Repository, policyID string) (PolicyRecord, error) {
	if err := repo.Deactivate(policyID); err != nil {
		return PolicyRecord{}, err
	}
	rec, _, err := repo.Get(policyID)
	return rec, err
}

// Archive retires a policy, retaining it for audit only.
func Archive(repo Repository, policyID string) (PolicyRecord, error) {
	if err := repo.Archive(policyID); err != nil {
		return PolicyRecord{}, err
	}
	rec, _, err := repo.Get(policyID)
	return rec, err
}
