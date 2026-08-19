package external

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/platformview"
)

// PlatformViewReader is the query contract external consumes from internal/platformview.
// *platformview.Facade satisfies it structurally (Dependency Inversion; MUST-1).
type PlatformViewReader interface {
	GetExecutionOverview(ctx context.Context, executionID string) (platformview.ExecutionOverview, error)
	GetHostPolicyStatus(ctx context.Context, hostRef string) (platformview.HostPolicyStatus, error)
	GetGovernanceSummary(ctx context.Context, policyID string) (platformview.GovernanceSummary, error)
	GetClusterPlacementView(ctx context.Context, hostRef string) (platformview.ClusterPlacementView, error)
}

// CorrelationReader is the query contract external consumes from internal/correlation.
// *correlation.Correlator satisfies it structurally.
type CorrelationReader interface {
	Correlate(ctx context.Context, scope correlation.Scope) (correlation.CorrelationView, error)
}

// Server is the read-only External Interface (MUST-0/1/3). It owns no data and holds no cached
// state. Each call hits the owning facade's query API at request time and returns a fresh, versioned
// DTO (SHOULD-3 lazy). The Server never executes, mutates, or reaches past the facades into the
// frozen systems (MUST-0; ADR-024 §1).
type Server struct {
	platform  PlatformViewReader
	correlate CorrelationReader
	authn     Authenticator
}

// NewServer constructs a Server. authn defaults to a single-tenant / no-auth stub if nil (MUST-5
// Authn seam). platform and correlate must be non-nil facades.
func NewServer(platform PlatformViewReader, correlate CorrelationReader, authn Authenticator) *Server {
	if authn == nil {
		authn = NoAuthAuthenticator{}
	}
	return &Server{platform: platform, correlate: correlate, authn: authn}
}

// GetExecution returns the external/v1 projection of one execution's overview (MUST-1 via
// platformview). It is a read query only.
func (s *Server) GetExecution(ctx context.Context, executionID string) (*ExecutionView, error) {
	if _, err := s.authn.Authenticate(ctx); err != nil {
		return nil, err
	}
	if executionID == "" {
		return nil, ErrInvalidInput
	}
	ov, err := s.platform.GetExecutionOverview(ctx, executionID)
	if err != nil {
		return nil, err
	}
	return mapExecution(ov), nil
}

// GetHost returns the external/v1 projection of a host's policy status (MUST-1 via platformview).
// Groups/Labels come from the cluster placement view; attachments/verdicts from the host policy
// status. Both are read-only facade calls composed into one DTO (MUST-4/5).
func (s *Server) GetHost(ctx context.Context, hostRef string) (*HostView, error) {
	if _, err := s.authn.Authenticate(ctx); err != nil {
		return nil, err
	}
	if hostRef == "" {
		return nil, ErrInvalidInput
	}
	st, err := s.platform.GetHostPolicyStatus(ctx, hostRef)
	if err != nil {
		return nil, err
	}
	pv, err := s.platform.GetClusterPlacementView(ctx, hostRef)
	if err != nil {
		return nil, err
	}
	return mapHost(st, pv), nil
}

// GetPolicy returns the external/v1 projection of a governance policy summary (MUST-1 via
// platformview).
func (s *Server) GetPolicy(ctx context.Context, policyID string) (*PolicyView, error) {
	if _, err := s.authn.Authenticate(ctx); err != nil {
		return nil, err
	}
	if policyID == "" {
		return nil, ErrInvalidInput
	}
	sum, err := s.platform.GetGovernanceSummary(ctx, policyID)
	if err != nil {
		if errors.Is(err, platformview.ErrPolicyNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mapPolicy(sum), nil
}

// GetCorrelation returns the external/v1 projection of a scope-bounded correlation view (MUST-1 via
// correlation). The scope must declare Kind+Ref (SHOULD-5 analogue; rejects unbounded queries).
func (s *Server) GetCorrelation(ctx context.Context, scope ScopeDTO) (*CorrelationView, error) {
	if _, err := s.authn.Authenticate(ctx); err != nil {
		return nil, err
	}
	if scope.Kind == "" || scope.Ref == "" {
		return nil, ErrInvalidInput
	}
	cv, err := s.correlate.Correlate(ctx, correlation.Scope{Kind: scope.Kind, Ref: scope.Ref})
	if err != nil {
		return nil, err
	}
	return mapCorrelation(cv), nil
}

// ---------------------------------------------------------------------------
// Mapping (facade view -> external/v1 DTO). Every slice is copied and re-sorted so the same input
// always yields the same DTO byte-for-byte (determinism, mirrors correlation SHOULD-4). No new
// fields are invented — only re-assembled references (MUST-4/5).
// ---------------------------------------------------------------------------

func mapExecution(ov platformview.ExecutionOverview) *ExecutionView {
	v := &ExecutionView{
		ExecutionID: ov.ExecutionID,
		Meta:        dtoMeta("platform/view/v1", ov.Meta.SourceCapability, ov.Meta.SourceID, ov.Meta.RelatedIDs, ""),
	}
	if ov.Observation != nil {
		o := ov.Observation
		v.Observation = &ObservationDTO{o.ObsID, o.Kind, o.TraceID, o.ExecutionID, o.RequestID, o.PluginID, o.AuditID}
	}
	if ov.Placement != nil {
		p := ov.Placement
		v.Placement = &PlacementDTO{Version: p.Version, Targets: sortedCopy(p.Targets), Reason: p.Reason}
	}
	if len(ov.Attachments) > 0 {
		atts := make([]AttachmentDTO, 0, len(ov.Attachments))
		for _, a := range ov.Attachments {
			atts = append(atts, AttachmentDTO{a.AttachID, a.TargetKind, a.TargetRef, a.Kind, a.CreatedAt})
		}
		sort.Slice(atts, func(i, j int) bool { return atts[i].AttachID < atts[j].AttachID })
		v.Attachments = atts
	}
	if ov.Verdict != nil {
		v.Verdict = mapVerdict(ov.Verdict)
	}
	return v
}

func mapHost(st platformview.HostPolicyStatus, pv platformview.ClusterPlacementView) *HostView {
	v := &HostView{
		HostID: st.HostID,
		Meta:   dtoMeta("platform/view/v1", st.Meta.SourceCapability, st.Meta.SourceID, st.Meta.RelatedIDs, ""),
	}
	if len(pv.Groups) > 0 {
		v.Groups = sortedCopy(pv.Groups)
	}
	if len(pv.Labels) > 0 {
		v.Labels = sortedCopy(pv.Labels)
	}
	if len(st.Attachments) > 0 {
		atts := make([]AttachmentDTO, 0, len(st.Attachments))
		for _, a := range st.Attachments {
			atts = append(atts, AttachmentDTO{a.AttachID, a.TargetKind, a.TargetRef, a.Kind, a.CreatedAt})
		}
		sort.Slice(atts, func(i, j int) bool { return atts[i].AttachID < atts[j].AttachID })
		v.Attachments = atts
	}
	if len(st.Verdicts) > 0 {
		vs := make([]VerdictDTO, 0, len(st.Verdicts))
		for i := range st.Verdicts {
			vs = append(vs, *mapVerdict(&st.Verdicts[i]))
		}
		v.Verdicts = vs
	}
	return v
}

func mapPolicy(sum platformview.GovernanceSummary) *PolicyView {
	v := &PolicyView{
		PolicyID: sum.PolicyID,
		Meta:     dtoMeta("platform/view/v1", sum.Meta.SourceCapability, sum.Meta.SourceID, sum.Meta.RelatedIDs, ""),
	}
	if len(sum.MatchedRules) > 0 {
		rs := make([]RuleDTO, 0, len(sum.MatchedRules))
		for _, r := range sum.MatchedRules {
			rs = append(rs, RuleDTO{r.RuleID, r.Kind, r.Priority})
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].RuleID < rs[j].RuleID })
		v.MatchedRules = rs
	}
	if len(sum.RecentVerdicts) > 0 {
		vs := make([]VerdictDTO, 0, len(sum.RecentVerdicts))
		for i := range sum.RecentVerdicts {
			vs = append(vs, *mapVerdict(&sum.RecentVerdicts[i]))
		}
		v.RecentVerdicts = vs
	}
	return v
}

func mapCorrelation(cv correlation.CorrelationView) *CorrelationView {
	v := &CorrelationView{
		Scope:        ScopeDTO{Kind: cv.Scope.Kind, Ref: cv.Scope.Ref},
		ExecutionRef: cv.ExecutionRef,
		Reason:       cv.Meta.Reason,
		CorrelatedAt: cv.Meta.CorrelatedAt.Format(time.RFC3339),
		Meta:         dtoMeta("correlation/view/v1", "", "", cv.Meta.SourceRefs, cv.Meta.CorrelatedAt.Format(time.RFC3339)),
	}
	if len(cv.ObservationRefs) > 0 {
		v.ObservationRefs = sortedCopy(cv.ObservationRefs)
	}
	if len(cv.VerdictRefs) > 0 {
		v.VerdictRefs = sortedCopy(cv.VerdictRefs)
	}
	if len(cv.PlacementRefs) > 0 {
		v.PlacementRefs = sortedCopy(cv.PlacementRefs)
	}
	if len(cv.PolicyRefs) > 0 {
		v.PolicyRefs = sortedCopy(cv.PolicyRefs)
	}
	return v
}

func mapVerdict(vv *platformview.VerdictView) *VerdictDTO {
	if vv == nil {
		return nil
	}
	dto := &VerdictDTO{
		Code:          vv.Code,
		Reason:        vv.Reason,
		PolicyID:      vv.PolicyID,
		RuleID:        vv.RuleID,
		MatchedPolicy: vv.MatchedPolicy,
		MatchedRule:   vv.MatchedRule,
		Priority:      vv.Priority,
	}
	if len(vv.Evidence) > 0 {
		dto.Evidence = sortedCopy(vv.Evidence)
	}
	return dto
}

// dtoMeta builds a DTOViewMeta. SourceRefs are copied and sorted so provenance ordering is
// deterministic regardless of facade call order.
func dtoMeta(sourceView, sourceCapability, sourceID string, related []string, correlatedAt string) DTOViewMeta {
	refs := sortedCopy(related)
	return DTOViewMeta{
		ViewVersion:  ContractVersion,
		SourceView:   sourceView,
		SourceRefs:   refs,
		CorrelatedAt: correlatedAt,
	}
}

// sortedCopy returns a sorted copy of s (nil if s is empty/nil).
func sortedCopy(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
