// Package inventory aggregates the observed identity and capability of a target
// host into a single, unified view.
//
// It is the canonical CONSUMER of the snapshot observation surface — the
// "统一消费" Phase 2.7 called for. Before 2.7, host identity was read
// scatter-gunned across ctx.Host(), ctx.Target() and ctx.Capability().OS inside
// individual handlers. Phase 2.7 made platform.Resolver.Host()/HostFor() the
// single entry point; this package is the first caller that consumes BOTH the
// HostSnapshot (identity) and the CapabilitySnapshot (capabilities) through that
// one surface, so the rest of the control plane has one place to ask "what is
// this host and what can it do?".
package inventory

import (
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
	"github.com/YuDong999/opscore/internal/platform"
)

// View is the unified, read-only projection of a host's observed state.
type View struct {
	// Host is the static identity ("who am I?"). Nil when no identity is
	// available — e.g. an unreachable remote target with no local snapshot —
	// so callers can decide how to degrade instead of guessing.
	Host *snapshot.HostSnapshot `json:"host,omitempty"`
	// Capability is the observed capability matrix ("what can I do?").
	Capability *snapshot.CapabilitySnapshot `json:"capability,omitempty"`
	// PackageManager is the resolved package-manager executable for the host,
	// or "" when the identity is unknown. Derived via platform.Resolver — never
	// a hard-coded distro assumption (the Phase 2.7 unification contract).
	PackageManager string `json:"package_manager,omitempty"`
	// Capabilities is the capability matrix flattened (name-sorted) for easy
	// UI rendering. Empty when no CapabilitySnapshot is present.
	Capabilities []snapshot.CapabilityInfo `json:"capabilities,omitempty"`
	// Detail is the aggregated read-only observation data (Phase 2.9 closure):
	// the real output of the whitelisted read-only ops (mounts, packages,
	// users, services, processes, host telemetry). Nil when the control plane
	// has no Execution runtime wired, or when the target is unreachable and
	// every op degrades. Populated by the server via inventory.Collect.
	Detail *Detail `json:"detail,omitempty"`
}

// Source is the data surface Inventory projects into a View. It is the
// extension point (SHOULD polish, GPT review): Inventory has become the
// canonical projection of host state, so its input should be pluggable rather
// than hard-wired to the live Context. The default implementation reads
// directly from the operation Context (the existing behavior); future sources
// (a cached registry read-back, an external CMDB adapter, a replay fixture)
// can be swapped in without touching the projection logic.
type Source interface {
	// Host returns the observed identity, or nil when unavailable.
	Host(ctx core.Context) *snapshot.HostSnapshot
	// Capability returns the observed capability matrix, or nil when unavailable.
	Capability(ctx core.Context) *snapshot.CapabilitySnapshot
	// PackageManager returns the resolved package-manager executable, or "".
	PackageManager(ctx core.Context) string
}

// DefaultSource reads observations directly from the operation Context — the
// behavior Inventory had before the Source abstraction existed.
type DefaultSource struct{}

func (DefaultSource) Host(ctx core.Context) *snapshot.HostSnapshot {
	return ctx.HostSnapshot()
}

func (DefaultSource) Capability(ctx core.Context) *snapshot.CapabilitySnapshot {
	return ctx.CapabilitySnapshot()
}

func (DefaultSource) PackageManager(ctx core.Context) string {
	if pm, err := platform.New(ctx).PackageManager(); err == nil {
		return pm
	}
	return ""
}

// Build is the canonical entry point: it projects the live Context into a View
// via the DefaultSource. Build never fails; a context with no observed identity
// yields an empty View (Host=nil, PackageManager="").
func Build(ctx core.Context) View {
	return BuildFrom(ctx, DefaultSource{})
}

// BuildFrom projects ctx through an explicit Source. It is the seam that lets
// callers (tests, a cached inventory read-back, a replay fixture) supply a
// different observation surface while reusing the same projection.
func BuildFrom(ctx core.Context, src Source) View {
	v := View{}
	if h := src.Host(ctx); h != nil {
		v.Host = h
	}
	if cs := src.Capability(ctx); cs != nil {
		v.Capability = cs
		v.Capabilities = cs.Sorted()
	}
	if pm := src.PackageManager(ctx); pm != "" {
		v.PackageManager = pm
	}
	return v
}
