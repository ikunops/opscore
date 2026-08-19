// Package compat defines the Compatibility Gate: the LAST safety boundary a
// plugin must cross before it enters the runtime (GPT Round 12 / Phase 3.5).
//
// The Gate is deliberately decoupled from the Loader (it does NOT live in
// FileLoader) and from the Descriptor identity (Phase 3.4.1) so the contract
// stays stable. The Manager injects a Gate + the running KernelInfo; the Gate
// decides whether a parsed, validated Manifest may load against this kernel.
//
// Flow (frozen, GPT Round 12):
//
//	Provider.Read -> Manifest.Validate -> Compatibility.Check -> Descriptor -> Load
//
// i.e. a plugin is rejected BEFORE a Descriptor is built and BEFORE Load, so
// an incompatible plugin never reaches the core Registry or Storage.
package compat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// KernelInfo is the runtime's SELF-DESCRIPTION that a plugin must be
// compatible with. It is injected into the Manager (not hardcoded) so the
// compatibility contract stays decoupled from any specific Loader.
type KernelInfo struct {
	// Version is the running kernel's semantic version, e.g. "0.2.0".
	Version string
	// SupportedAPIs lists the PluginAPI contracts this kernel can load
	// (e.g. "opscore.plugin/v1"). A plugin whose PluginAPI is not in this
	// list is rejected.
	SupportedAPIs []string
}

// Result is the outcome of a compatibility check. It is returned alongside
// any error so callers can present a HUMAN-READABLE reason in logs/UI
// (GPT Round 12: prefer a structured Result over a bare error — the console
// must be able to say "Plugin: mysql@1.2.0 / Status: Rejected / Reason:
// requires kernel >= 0.2, current kernel 0.1").
//
// The two channels are intentional (GPT Round 13):
//   - Result holds the BUSINESS judgment (Compatible + Reason + Code +
//     Warnings). Result=false, error=nil means "I checked, it's incompatible."
//   - error holds a GATE FAILURE (bad config, parser crash, future remote
//     gate network error). error!=nil means "the gate did not finish its job."
type Result struct {
	// Compatible is the business verdict.
	Compatible bool
	// Reason is a human-readable explanation for a rejection (or "ok").
	Reason string
	// Code is a STABLE, machine-readable rejection category (e.g.
	// "min_kernel", "plugin_api", "invalid_kernel", "nil_manifest") so UIs
	// and audit dashboards can filter/group without parsing Reason text
	// (GPT Round 13 SHOULD).
	Code string
	// Warnings are non-fatal advisories surfaced even when Compatible is
	// true, e.g. "deprecated api", "schema v1 will be removed". Lets the UI
	// warn operators without blocking load (GPT Round 13 SHOULD).
	Warnings []string
}

// Gate decides whether a plugin manifest may load against the running kernel.
// It is an INTERFACE (not a hardcoded check) so tests and future policy (e.g.
// a remote compatibility service, or a strict "deny-by-default" gate) can
// substitute their own implementation without touching the Loader or Manager.
type Gate interface {
	Check(m *manifest.Manifest, kernel KernelInfo) (*Result, error)
}

// DefaultGate is the production compatibility gate. It enforces two
// constraints, both OPTIONAL on the manifest (an unset constraint is a pass):
//
//   - MinKernel: the running kernel Version must be >= the plugin's MinKernel.
//   - PluginAPI: the plugin's PluginAPI (if set) must be in kernel.SupportedAPIs.
type DefaultGate struct{}

// Check implements Gate. It returns a *Result describing the outcome and a
// non-nil error when the plugin is rejected (or the inputs are invalid), so
// callers can use either `res.Compatible` for display or `err != nil` for
// control flow.
func (DefaultGate) Check(m *manifest.Manifest, kernel KernelInfo) (*Result, error) {
	if m == nil {
		return &Result{Compatible: false, Reason: "nil manifest", Code: "nil_manifest"}, fmt.Errorf("compat: nil manifest")
	}

	// MinKernel constraint: the running kernel must be new enough.
	if m.MinKernel != "" {
		cmp, err := compareSemver(kernel.Version, m.MinKernel)
		if err != nil {
			return &Result{Compatible: false, Reason: fmt.Sprintf("invalid kernel version %q: %v", kernel.Version, err), Code: "invalid_kernel"}, err
		}
		if cmp < 0 {
			return &Result{
				Compatible: false,
				Reason:     fmt.Sprintf("plugin %q requires kernel >= %s, current kernel is %s", m.Name, m.MinKernel, kernel.Version),
				Code:       "min_kernel",
			}, fmt.Errorf("plugin %q incompatible: kernel %s < required %s", m.Name, kernel.Version, m.MinKernel)
		}
	}

	// PluginAPI constraint: the kernel must support the API contract the
	// plugin was built against.
	if m.PluginAPI != "" {
		supported := false
		for _, api := range kernel.SupportedAPIs {
			if api == m.PluginAPI {
				supported = true
				break
			}
		}
		if !supported {
			return &Result{
				Compatible: false,
				Reason:     fmt.Sprintf("plugin %q requires PluginAPI %q not supported by kernel %s", m.Name, m.PluginAPI, kernel.Version),
				Code:       "plugin_api",
			}, fmt.Errorf("plugin %q incompatible: PluginAPI %q unsupported", m.Name, m.PluginAPI)
		}
	}

	return &Result{Compatible: true, Reason: "ok"}, nil
}

// compareSemver compares two dot-separated semantic versions numerically,
// segment by segment, padding the shorter side with zeros. A leading "v" is
// tolerated (v0.2.0 == 0.2.0). Returns -1/0/1, or an error if a segment is
// not a non-negative integer. An empty version is treated as "0.0.0".
func compareSemver(a, b string) (int, error) {
	as, err := splitVersion(a)
	if err != nil {
		return 0, fmt.Errorf("version %q: %w", a, err)
	}
	bs, err := splitVersion(b)
	if err != nil {
		return 0, fmt.Errorf("version %q: %w", b, err)
	}
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
	return 0, nil
}

func splitVersion(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return []int{0}, nil
	}
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid segment %q", p)
		}
		if n < 0 {
			return nil, fmt.Errorf("negative segment %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}
