package governancepolicy

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/platformview"
)

// Reader adapts the policy Repository to the governance read contracts consumed
// by platformview and correlation. It owns no data of its own (no cache, no
// store — the Repository is the owner, B6). It is the read surface only; the
// write/lifecycle surface (lifecycle.go + a future Management API) is separate
// (B7).
type Reader struct {
	repo Repository
	now  func() time.Time
}

// NewReader builds a read adapter over the given repository. now defaults to
// time.Now; tests may override it for determinism.
func NewReader(repo Repository) *Reader {
	return &Reader{repo: repo, now: time.Now}
}

// QueryRules projects the persisted rules of a policy into RuleViews, sorted
// stably by (priority desc, RuleID asc) for determinism. Honest-empty (nil) when
// the policy is unknown.
func (r *Reader) QueryRules(_ context.Context, policyID string) ([]platformview.RuleView, error) {
	rec, ok, err := r.repo.Get(policyID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, platformview.ErrPolicyNotFound
	}
	views := make([]platformview.RuleView, 0, len(rec.Rules))
	for _, rule := range rec.Rules {
		views = append(views, platformview.RuleView{
			RuleID:   rule.RuleID,
			Kind:     string(rule.Kind),
			Priority: rule.Priority,
			Meta: platformview.Meta{
				SourceCapability: "governance",
				SourceID:         policyID,
				CollectedAt:      r.now(),
				RelatedIDs:       []string{rec.PolicyID, fmt.Sprintf("rev-%d", rec.Revision)},
			},
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Priority != views[j].Priority {
			return views[i].Priority > views[j].Priority
		}
		return views[i].RuleID < views[j].RuleID
	})
	return views, nil
}

// QueryVerdict is honestly empty (nil): a Verdict requires observed State this
// read layer never holds, and Governance owns evaluation, not storage (B2/B7).
func (r *Reader) QueryVerdict(_ context.Context, _ string) (*platformview.VerdictView, error) {
	return nil, nil
}

// QueryVerdictRefs returns the persisted PolicyIDs relevant to the scope
// (read-only, existing identities only — MUST-2). For a policy-scoped query it
// returns just that policy; otherwise it lists all persisted policy IDs.
// Honest-empty when none exist. It never fabricates a verdict reference.
func (r *Reader) QueryVerdictRefs(_ context.Context, scope correlation.Scope) ([]string, error) {
	recs, err := r.repo.List()
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(recs))
	for _, rec := range recs {
		if scope.Kind == correlation.ScopePolicy && scope.Ref != rec.PolicyID {
			continue
		}
		refs = append(refs, rec.PolicyID)
	}
	sort.Strings(refs)
	return refs, nil
}
