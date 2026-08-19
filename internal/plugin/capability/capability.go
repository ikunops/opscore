// Package capability defines the Capability Negotiation contract (Phase 3.7 /
// GPT Round 16). Capabilities describe, negotiate, and authorize — they do NOT
// perform resource discovery or plugin distribution (out of scope: Marketplace,
// License, Remote Discovery, OCI).
//
// The contract is intentionally minimal and decoupled from the Runtime:
//   - Capability: a single capability DESCRIPTOR (name, namespace, version,
//     required flag).
//   - Provider: the HOST CAPABILITY PROVIDER seam — what the kernel exposes.
//   - Negotiate: resolves a plugin's requirements against the host.
//   - Result: per-capability outcome (Granted / Missing / OptionalMissing).
//
// A plugin declares its requirements as plain name tokens in
// manifest.Manifest.Capabilities; the Manager upgrades them to Capability
// descriptors (Required=true by default) and negotiates against the injected
// host Provider. The negotiation is name/namespace presence-based today; the
// Capability.Version field is reserved so the contract can later become
// version-aware WITHOUT a breaking change.
package capability

import (
	"fmt"
	"strings"
)

// State is the outcome of negotiating a single required Capability.
type State int

const (
	// StateGranted: the host provides the capability.
	StateGranted State = iota
	// StateMissing: a REQUIRED capability the host does not provide — the
	// plugin MUST refuse to load.
	StateMissing
	// StateOptionalMissing: an OPTIONAL capability the host does not provide —
	// allowed; the plugin must degrade gracefully.
	StateOptionalMissing
)

// String renders the State for audit / human display.
func (s State) String() string {
	switch s {
	case StateGranted:
		return "granted"
	case StateMissing:
		return "missing"
	case StateOptionalMissing:
		return "optional-missing"
	default:
		return "unknown"
	}
}

// Capability is a single capability declaration (Capability Descriptor,
// Phase 3.7). It is shared by both sides: a plugin REQUIRES capabilities and a
// host PROVIDES them, both using this same shape.
type Capability struct {
	// Name is the FULL capability token, e.g. "os.linux", "fs.zfs",
	// "net.tcp". The first "."-segment is its Namespace.
	Name string `json:"name"`
	// Namespace is the first "."-segment of Name (e.g. "os"). Derived on
	// construction; queried for namespace-based grouping / isolation.
	Namespace string `json:"namespace"`
	// Version is the capability's OWN version (Capability Version, Phase 3.7
	// future extension). NOT YET enforced by Negotiate — reserved so the
	// contract can evolve from "present/absent" to "version-aware" without a
	// breaking change.
	Version string `json:"version,omitempty"`
	// Required distinguishes a hard requirement (true: the plugin must refuse
	// to load if absent) from an optional enhancement (false: the plugin
	// degrades gracefully if absent). Defaults to true.
	Required bool `json:"required"`
}

// Parse builds a (required) Capability from a raw name token, deriving its
// Namespace. An empty name is rejected.
func Parse(name string) (Capability, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Capability{}, fmt.Errorf("capability: empty name")
	}
	return Capability{Name: name, Namespace: namespaceOf(name), Required: true}, nil
}

// ParseOptional builds an OPTIONAL Capability from a raw name token.
func ParseOptional(name string) (Capability, error) {
	c, err := Parse(name)
	if err != nil {
		return c, err
	}
	c.Required = false
	return c, nil
}

func namespaceOf(name string) string {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i]
	}
	return name
}

// Provider exposes the capabilities a host (kernel) PROVIDES. This is the Host
// Capability Provider seam (Phase 3.7): the kernel injects an implementation so
// negotiation never reaches into host internals.
type Provider interface {
	// Capabilities returns what the host currently provides.
	Capabilities() []Capability
}

// HostProvider is the default Provider backed by a static capability list (what
// the kernel reads from its environment at boot). It implements Provider so the
// Manager can negotiate without knowing the source.
type HostProvider struct {
	caps []Capability
}

// NewHostProvider builds a HostProvider from raw capability name tokens. Tokens
// that fail to parse are skipped (a malformed host capability is a config bug,
// not a plugin fault).
func NewHostProvider(names []string) *HostProvider {
	caps := make([]Capability, 0, len(names))
	for _, n := range names {
		if c, err := Parse(n); err == nil {
			caps = append(caps, c)
		}
	}
	return &HostProvider{caps: caps}
}

// Capabilities implements Provider.
func (h *HostProvider) Capabilities() []Capability { return h.caps }

// Has reports whether the host provides the named capability (exact token).
func (h *HostProvider) Has(name string) bool {
	for _, c := range h.caps {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Outcome is the per-capability result of a Negotiation.
type Outcome struct {
	Cap    Capability
	State  State
	Reason string
}

// Result is the aggregate outcome of negotiating a set of required capabilities
// against a host Provider.
type Result struct {
	Outcomes        []Outcome
	AllGranted      bool
	Missing         []string // required capabilities the host lacks
	OptionalMissing []string // optional capabilities the host lacks
}

// Negotiate resolves each required capability against the host Provider.
//
//   - A required capability the host lacks -> StateMissing (added to Missing).
//   - An optional capability the host lacks -> StateOptionalMissing (added to
//     OptionalMissing).
//   - AllGranted is true only if EVERY required capability is present. Optional
//     Missing capabilities do NOT block the plugin.
//
// Capability Version is NOT yet enforced (reserved for future extension);
// negotiation is currently name/namespace presence-based.
func Negotiate(required []Capability, host Provider) Result {
	res := Result{Outcomes: make([]Outcome, 0, len(required))}
	have := make(map[string]Capability, 0)
	if host != nil {
		for _, c := range host.Capabilities() {
			have[c.Name] = c
		}
	}
	allGranted := true
	for _, req := range required {
		out := Outcome{Cap: req}
		if _, ok := have[req.Name]; ok {
			out.State = StateGranted
			out.Reason = "host provides " + req.Name
		} else if req.Required {
			out.State = StateMissing
			out.Reason = req.Name + " required but not provided by host"
			res.Missing = append(res.Missing, req.Name)
			allGranted = false
		} else {
			out.State = StateOptionalMissing
			out.Reason = req.Name + " optional and not provided by host"
			res.OptionalMissing = append(res.OptionalMissing, req.Name)
		}
		res.Outcomes = append(res.Outcomes, out)
	}
	res.AllGranted = allGranted
	return res
}
