package harness

import (
	"context"
	"time"

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/enterprise"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/platformview"
)

// observabilityAdapter satisfies both platformview.ObservabilityReader and
// correlation.ObservabilityReader. It delegates truthfully to the real Collector query backend
// (SHOULD-2 — real Reader wiring, not nil).
type observabilityAdapter struct{ c *observability.Collector }

func (a *observabilityAdapter) QueryObservations(_ context.Context, executionID string) ([]platformview.ObservationView, error) {
	obs := a.c.Query(observability.Query{ExecutionID: executionID})
	out := make([]platformview.ObservationView, 0, len(obs))
	for _, o := range obs {
		out = append(out, platformview.ObservationView{
			ObsID:       o.ObsID,
			Kind:        string(o.Kind),
			TraceID:     o.TraceID,
			ExecutionID: o.ExecutionID,
			RequestID:   o.RequestID,
			PluginID:    o.PluginID,
			AuditID:     o.AuditID,
			Meta: platformview.Meta{
				SourceCapability: "observability",
				SourceID:         o.ObsID,
				CollectedAt:      o.Timestamp,
			},
		})
	}
	return out, nil
}

func (a *observabilityAdapter) QueryObservationRefs(_ context.Context, scope correlation.Scope) ([]string, error) {
	if scope.Kind != correlation.ScopeExecution {
		return nil, nil
	}
	obs := a.c.Query(observability.Query{ExecutionID: scope.Ref})
	refs := make([]string, 0, len(obs))
	for _, o := range obs {
		refs = append(refs, o.ObsID)
	}
	return refs, nil
}

// enterpriseAdapter satisfies both platformview.EnterpriseReader and correlation.EnterpriseReader.
// It delegates truthfully to the real Service backend (SHOULD-2).
type enterpriseAdapter struct{ s *enterprise.Service }

func (a *enterpriseAdapter) QueryAttachments(_ context.Context, targetRef string) ([]platformview.AttachmentView, error) {
	all := a.s.All()
	out := make([]platformview.AttachmentView, 0, len(all))
	for _, att := range all {
		if att.TargetRef != targetRef {
			continue
		}
		out = append(out, platformview.AttachmentView{
			AttachID:   att.AttachID,
			TargetKind: string(att.TargetKind),
			TargetRef:  att.TargetRef,
			Kind:       string(att.Kind),
			CreatedAt:  att.CreatedAt.Format(time.RFC3339),
			Meta: platformview.Meta{
				SourceCapability: "enterprise",
				SourceID:         att.AttachID,
			},
		})
	}
	return out, nil
}

func (a *enterpriseAdapter) QueryPolicyRefs(_ context.Context, scope correlation.Scope) ([]string, error) {
	all := a.s.All()
	refs := make([]string, 0, len(all))
	for _, att := range all {
		if att.TargetRef == scope.Ref || string(att.TargetKind) == scope.Kind {
			refs = append(refs, att.AttachID)
		}
	}
	return refs, nil
}
