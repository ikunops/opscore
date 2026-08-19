package cluster

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Manager is the in-memory coordination store. It holds ONLY membership /
// group / label / placement metadata keyed by ClusterID + HostRef. It owns no
// execution state and exposes NO method that runs a command (ADR-016 MUST-1).
//
// It is safe for concurrent use: the host may call Join/ComputePlacement from
// multiple goroutines while coordinating.
type Manager struct {
	mu      sync.RWMutex
	members map[ClusterID]map[HostRef]Member
}

// NewManager builds an empty coordination manager.
func NewManager() *Manager {
	return &Manager{members: make(map[ClusterID]map[HostRef]Member)}
}

// Join admits a host (by opaque ref) into a cluster as an ACTIVE member. It
// records membership metadata only — it does not provision, connect to, or
// execute anything on the host (MUST-1/2/3).
func (m *Manager) Join(cid ClusterID, host HostRef, groups []string, labels map[string]string) (Member, error) {
	if cid == "" {
		return Member{}, errClusterIDRequired
	}
	if host == "" {
		return Member{}, errHostRefRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cm, ok := m.members[cid]
	if !ok {
		cm = make(map[HostRef]Member)
		m.members[cid] = cm
	}
	cp := make(map[string]string, len(labels))
	for k, v := range labels {
		cp[k] = v
	}
	mem := Member{
		ClusterID: cid,
		HostRef:   host,
		Groups:    append([]string(nil), groups...),
		Labels:    cp,
		State:     MemberActive,
	}
	cm[host] = mem
	return mem, nil
}

// Leave drains a host out of a cluster (marks it leaving then removes it). It
// does not delete, power off, or reconfigure the underlying host — host
// lifecycle stays in the Runtime Inventory (MUST-2/3).
func (m *Manager) Leave(cid ClusterID, host HostRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cm, ok := m.members[cid]
	if !ok {
		return errClusterNotFound
	}
	if _, ok := cm[host]; !ok {
		return errMemberNotFound
	}
	delete(cm, host)
	if len(cm) == 0 {
		delete(m.members, cid)
	}
	return nil
}

// SetLabel attaches a coordination label to a member. Labels are metadata only.
func (m *Manager) SetLabel(cid ClusterID, host HostRef, key, value string) error {
	return m.mutate(cid, host, func(mem *Member) {
		if mem.Labels == nil {
			mem.Labels = make(map[string]string)
		}
		mem.Labels[key] = value
	})
}

// RemoveLabel detaches a coordination label.
func (m *Manager) RemoveLabel(cid ClusterID, host HostRef, key string) error {
	return m.mutate(cid, host, func(mem *Member) {
		delete(mem.Labels, key)
	})
}

// AddToGroup adds a host to a logical group (Cluster owns grouping, MUST-3).
func (m *Manager) AddToGroup(cid ClusterID, host HostRef, group string) error {
	return m.mutate(cid, host, func(mem *Member) {
		for _, g := range mem.Groups {
			if g == group {
				return // idempotent
			}
		}
		mem.Groups = append(mem.Groups, group)
	})
}

// RemoveFromGroup removes a host from a logical group.
func (m *Manager) RemoveFromGroup(cid ClusterID, host HostRef, group string) error {
	return m.mutate(cid, host, func(mem *Member) {
		out := mem.Groups[:0]
		for _, g := range mem.Groups {
			if g != group {
				out = append(out, g)
			}
		}
		mem.Groups = out
	})
}

// SetState updates a member's membership lifecycle state. The state is
// membership-only (joining/active/leaving/offline) — never a host-exec state.
func (m *Manager) SetState(cid ClusterID, host HostRef, state MemberState) error {
	return m.mutate(cid, host, func(mem *Member) { mem.State = state })
}

// mutate applies fn to a member under lock; returns an error if the cluster or
// member does not exist.
func (m *Manager) mutate(cid ClusterID, host HostRef, fn func(*Member)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cm, ok := m.members[cid]
	if !ok {
		return errClusterNotFound
	}
	mem, ok := cm[host]
	if !ok {
		return errMemberNotFound
	}
	fn(&mem)
	cm[host] = mem
	return nil
}

// Members returns the current membership of a cluster, sorted by HostRef for
// stable output. Returns nil for an unknown cluster (never errors).
func (m *Manager) Members(cid ClusterID) []Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cm := m.members[cid]
	if len(cm) == 0 {
		return nil
	}
	out := make([]Member, 0, len(cm))
	for _, mem := range cm {
		out = append(out, mem)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostRef < out[j].HostRef })
	return out
}

// Clusters returns all known cluster IDs, sorted for stable output. This is a
// read-only enumeration of existing coordination groups — it introduces no new
// entity, no side effect, no store/cache, and no host ownership. It exists so
// that host-centric readers (e.g. Phase 13.2 clusterprojection) can discover the
// ID set without any caller maintaining a second source of truth (ADR-028 §4;
// GPT Round 62: "至多一个只读 Clusters() 访问器", forbidden to duplicate
// Cluster inventory). It is the ONLY addition Phase 13 makes to this frozen
// package.
func (m *Manager) Clusters() []ClusterID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ClusterID, 0, len(m.members))
	for cid := range m.members {
		out = append(out, cid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ByGroup returns the active members of a cluster that belong to the named
// group, sorted by HostRef.
func (m *Manager) ByGroup(cid ClusterID, group string) []Member {
	var out []Member
	for _, mem := range m.Members(cid) {
		if mem.State != MemberActive {
			continue
		}
		for _, g := range mem.Groups {
			if g == group {
				out = append(out, mem)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostRef < out[j].HostRef })
	return out
}

// ComputePlacement selects member host references that satisfy spec. It is a
// PURE function over coordination metadata — it returns HostRefs, never a
// command, and never executes (MUST-1/4). The host is responsible for mapping
// the returned references onto the Runtime's existing execution interface.
func (m *Manager) ComputePlacement(cid ClusterID, spec PlacementSpec) Placement {
	matched := make([]Member, 0)
	for _, mem := range m.Members(cid) {
		if mem.State != MemberActive {
			continue
		}
		if !hasAllGroups(mem, spec.RequireGroups) {
			continue
		}
		if !hasAllLabels(mem, spec.RequireLabels) {
			continue
		}
		matched = append(matched, mem)
	}

	// Soft affinity: members matching affinity labels sort first, but are not
	// excluded (affinity is a preference, not a requirement).
	if len(spec.Affinity) > 0 {
		sort.SliceStable(matched, func(i, j int) bool {
			return affinityScore(matched[i], spec.Affinity) > affinityScore(matched[j], spec.Affinity)
		})
	}

	limit := spec.Limit
	if limit <= 0 || limit > len(matched) {
		limit = len(matched)
	}
	targets := make([]HostRef, 0, limit)
	for i := 0; i < limit; i++ {
		targets = append(targets, matched[i].HostRef)
	}
	reason := placementReason(spec, len(matched), limit)
	return Placement{Version: PlacementVersion, Targets: targets, Reason: reason}
}

// placementReason builds declarative explainability metadata (SHOULD-1): why
// these targets were chosen. It is purely descriptive — it never encodes an
// execution decision.
func placementReason(spec PlacementSpec, matched, chosen int) string {
	var parts []string
	if len(spec.RequireGroups) > 0 {
		parts = append(parts, fmt.Sprintf("groups=%v", spec.RequireGroups))
	}
	if len(spec.RequireLabels) > 0 {
		parts = append(parts, fmt.Sprintf("labels=%v", spec.RequireLabels))
	}
	if len(spec.Affinity) > 0 {
		parts = append(parts, fmt.Sprintf("affinity=%v", spec.Affinity))
	}
	if len(parts) == 0 {
		parts = append(parts, "no-constraints")
	}
	return fmt.Sprintf("matched=%d chosen=%d by %s", matched, chosen, strings.Join(parts, " "))
}

func hasAllGroups(mem Member, groups []string) bool {
	for _, g := range groups {
		found := false
		for _, mg := range mem.Groups {
			if mg == g {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasAllLabels(mem Member, labels map[string]string) bool {
	for k, v := range labels {
		if mem.Labels[k] != v {
			return false
		}
	}
	return true
}

func affinityScore(mem Member, affinity map[string]string) int {
	score := 0
	for k, v := range affinity {
		if mem.Labels[k] == v {
			score++
		}
	}
	return score
}
