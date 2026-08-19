// Package clusterprojection implements Phase 13.2 Cluster Host-Centric Read
// Projection (ADR-028). It is a read-only adapter that turns the
// ClusterID-scoped, member-oriented metadata of internal/cluster into the
// host-centric ClusterReader contracts consumed by platformview and correlation.
//
// Invariants (ADR-027 A1–A6, ADR-028 MUST-13.1–13.4):
//   - Imports ONLY internal/cluster (metadata API). Never hostregistry / runtime / isolation.
//   - References cluster.HostRef opaquely; never owns or inspects the host.
//   - Reads → filters → sorts → projects existing state. Computes NO new fact.
//   - Deterministic: every unordered source is stable-sorted before output.
//   - No execution path: query methods only; no Run/Exec/Apply/Schedule/Dispatch.
//   - Does not modify external/v1, platformview, correlation, or the cluster contract.
package clusterprojection

import (
	"context"
	"sort"
	"time"

	"github.com/YuDong999/opscore/internal/cluster"
	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/platformview"
)

// Reader adapts *cluster.Manager to the host-centric ClusterReader contracts. It
// holds a reference to the (frozen) manager and owns no data of its own (no
// cache, no store, no background sync — MUST-13.4).
type Reader struct {
	m *cluster.Manager
}

// NewReader builds a projection over the given manager.
func NewReader(m *cluster.Manager) *Reader {
	return &Reader{m: m}
}

// clusterIDs returns all known cluster IDs in stable order (MUST-13.2).
func (r *Reader) clusterIDs() []cluster.ClusterID {
	ids := r.m.Clusters()
	out := make([]cluster.ClusterID, len(ids))
	copy(out, ids)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// findMember locates the cluster + member record for a host ref, scanning all
// clusters in stable ID order. Returns ok=false when the host is not a member of
// any known cluster (honest-empty per A6).
func (r *Reader) findMember(hostRef string) (cluster.ClusterID, *cluster.Member, bool) {
	for _, cid := range r.clusterIDs() {
		members := r.m.Members(cid)
		for i := range members {
			if string(members[i].HostRef) == hostRef {
				mm := members[i]
				return cid, &mm, true
			}
		}
	}
	return "", nil, false
}

// QueryMemberGroups returns the logical groups a host belongs to within its
// cluster, sorted and stable. Honest-empty (nil) when the host is unknown.
func (r *Reader) QueryMemberGroups(_ context.Context, hostRef string) ([]string, error) {
	_, mem, ok := r.findMember(hostRef)
	if !ok || mem == nil {
		return nil, nil
	}
	groups := append([]string(nil), mem.Groups...)
	sort.Strings(groups)
	return groups, nil
}

// QueryMemberLabels returns the coordination labels of a host as "k=v" pairs,
// sorted and stable. Honest-empty (nil) when the host is unknown.
func (r *Reader) QueryMemberLabels(_ context.Context, hostRef string) ([]string, error) {
	_, mem, ok := r.findMember(hostRef)
	if !ok || mem == nil {
		return nil, nil
	}
	labels := make([]string, 0, len(mem.Labels))
	for k, v := range mem.Labels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	return labels, nil
}

// QueryPlacement returns the host-centric placement view: the current
// active-membership placement projection of the cluster that contains the host,
// UNDER EMPTY CONSTRAINTS — i.e. it reuses the cluster's own pure
// ComputePlacement(cid, PlacementSpec{}) function (A3 — placement semantics
// unchanged; MUST-13.1 — no new decision, no recomputation). The returned
// Version/Targets are the cluster's EXISTING active-membership state; Reason is
// the cluster's own ComputePlacement explanation string, NOT a decision produced
// by clusterprojection. Honest-empty (nil) when the host is unknown.
//
// Precise semantic (GPT R63): this is a "current active-membership placement
// projection under empty constraints", NOT "recomputing the optimal placement
// for the host". The two have completely different architectural meaning.
func (r *Reader) QueryPlacement(_ context.Context, hostRef string) (*platformview.PlacementView, error) {
	cid, mem, ok := r.findMember(hostRef)
	if !ok || mem == nil {
		return nil, nil
	}
	p := r.m.ComputePlacement(cid, cluster.PlacementSpec{})
	targets := make([]string, len(p.Targets))
	for i, t := range p.Targets {
		targets[i] = string(t)
	}
	sort.Strings(targets)
	return &platformview.PlacementView{
		Version: p.Version,
		Targets: targets,
		Reason:  p.Reason, // cluster's own explanation, not a clusterprojection decision
		Meta: platformview.Meta{
			SourceCapability: "cluster",
			SourceID:         hostRef,
			CollectedAt:      time.Now(),
			RelatedIDs:       []string{string(cid)},
		},
	}, nil
}

// QueryPlacementRefs returns the host refs co-placed with the scoped host (active
// members of the same cluster). cluster only knows host refs, so non-host scopes
// return honest-empty (nil). Used by correlation for host-scoped joins.
func (r *Reader) QueryPlacementRefs(_ context.Context, scope correlation.Scope) ([]string, error) {
	if scope.Kind != correlation.ScopeHost {
		return nil, nil
	}
	_, mem, ok := r.findMember(scope.Ref)
	if !ok || mem == nil {
		return nil, nil
	}
	refs := make([]string, 0)
	for _, cm := range r.m.Members(mem.ClusterID) {
		if cm.State == cluster.MemberActive {
			refs = append(refs, string(cm.HostRef))
		}
	}
	sort.Strings(refs)
	return refs, nil
}
