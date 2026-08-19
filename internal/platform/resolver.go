// Package platform bridges business intent and concrete commands. Builtins
// declare WHAT they want ("restart a service", "open a port", "read logs");
// the Resolver decides HOW based on the target's capability. Builtins never
// hard-code a distro tool; Executors never know business semantics.
//
// Remote resolution (Phase 2.6): when the Context carries a CapabilitySnapshot
// (populated by the control plane — e.g. EnrichContextForTarget after an SSH
// probe, or a host-registry hint), the Resolver consumes it directly instead of
// guessing the "dominant Linux tool". When no snapshot is present (e.g. a unit
// test, or an unreachable host the control plane chose not to probe), it falls
// back to the dominant tool so plans stay buildable — the SSH transport
// validates the real host at run time. This closes deviation B: guesses become
// observed facts wherever a snapshot is available.
//
// Host identity (Phase 2.7 — "统一消费 snapshot.HostSnapshot"): the Resolver is
// also the single, unified consumer of host identity. Handlers must NOT scatter
// lookups across ctx.Host(), ctx.Target() and ctx.Capability().OS — they read
// target identity through Resolver.Host() / HostFor(). PackageManager() is the
// canonical consumer: it maps the observed host identity (OS + Platform) to a
// concrete package-manager executable, seeding the Phase 2.9 package builtins
// without hard-coding a distro anywhere in the builtin handlers.
//
// Facade structure (SHOULD polish, GPT review): Resolver is a thin FACADE that
// preserves the public API; the actual resolution lives in three focused
// sub-resolvers — serviceResolver (service/firewall/log executables),
// packageResolver (package-manager + package verb → argv), and hostResolver
// (host identity). The split is internal only; callers see no change, and it
// keeps each concern small before the Resolver grows further.
package platform

import (
	"fmt"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// Resolver is the public facade mapping capability requirements to concrete
// executables. Its methods delegate to the focused sub-resolvers below; the
// external API is stable and callers are unaffected by the internal split.
type Resolver struct {
	pkg  *packageResolver
	svc  *serviceResolver
	host *hostResolver
}

// New builds a Resolver from the operation Context. The local decisioning
// surface (ctx.Capability()) is used for local execution; the observed
// CapabilitySnapshot (ctx.CapabilitySnapshot(), when present) is used for
// remote execution.
func New(ctx core.Context) *Resolver {
	return &Resolver{
		pkg:  &packageResolver{ctx: ctx, cap: ctx.Capability()},
		svc:  &serviceResolver{ctx: ctx, cap: ctx.Capability()},
		host: &hostResolver{ctx: ctx},
	}
}

// ServiceManager returns the executable for service lifecycle operations.
func (r *Resolver) ServiceManager(remote bool) (string, error) { return r.svc.ServiceManager(remote) }

// PacketFilter returns the executable for firewall rule management.
func (r *Resolver) PacketFilter(remote bool) (string, error) { return r.svc.PacketFilter(remote) }

// LogReader returns the executable for reading system logs.
func (r *Resolver) LogReader(remote bool) (string, error) { return r.svc.LogReader(remote) }

// Host returns the observed identity of the CURRENT target (local or remote).
// For local execution this is the HostSnapshot baked in at Context.Build(); for
// remote execution it is the snapshot populated by EnrichContextForTarget.
func (r *Resolver) Host() (*snapshot.HostSnapshot, bool) { return r.host.Host() }

// HostFor returns the observed identity for an ARBITRARY target (not just the
// current one) — e.g. a Batch fan-out child, or an Inventory read-back.
func (r *Resolver) HostFor(target core.TargetHost) (*snapshot.HostSnapshot, bool) {
	return r.host.HostFor(target)
}

// PackageManager maps the observed host identity (OS + Platform) to a concrete
// package-manager executable. It is the canonical consumer of HostSnapshot
// (Phase 2.7) and seeds the Phase 2.9 package builtins.
func (r *Resolver) PackageManager() (string, error) { return r.pkg.Manager() }

// PackageCommand returns the (executable, args) tuple for a package verb on the
// current target's host.
func (r *Resolver) PackageCommand(verb PackageVerb, pkgs []string) (string, []string, error) {
	return r.pkg.Command(verb, pkgs)
}

// ---------------------------------------------------------------------------
// serviceResolver — service / firewall / log executables
// ---------------------------------------------------------------------------

type serviceResolver struct {
	ctx core.Context
	cap core.CapabilityContext
}

// ServiceManager returns the executable for service lifecycle operations.
func (r *serviceResolver) ServiceManager(remote bool) (string, error) {
	if !remote {
		if r.cap.ServiceManager != "" {
			return r.cap.ServiceManager, nil
		}
		return "", fmt.Errorf("%w: no service manager (need systemctl or service)", core.ErrCapabilityMissing)
	}
	if snap := r.ctx.CapabilitySnapshot(); snap != nil {
		if snap.Available("systemd") {
			return "systemctl", nil
		}
		if snap.Available("service") {
			return "service", nil
		}
		// Snapshot present but host reports no manager: fall back to the
		// dominant tool so the plan still builds; the SSH transport validates
		// the real host at run time.
	}
	return "systemctl", nil
}

// PacketFilter returns the executable for firewall rule management.
func (r *serviceResolver) PacketFilter(remote bool) (string, error) {
	if !remote {
		switch {
		case r.cap.HasFirewalld:
			return "firewall-cmd", nil
		case r.cap.HasUFW:
			return "ufw", nil
		case r.cap.HasIptables:
			return "iptables", nil
		}
		return "", fmt.Errorf("%w: no packet filter (need firewalld, ufw, or iptables)", core.ErrCapabilityMissing)
	}
	if snap := r.ctx.CapabilitySnapshot(); snap != nil {
		switch {
		case snap.Available("firewalld"):
			return "firewall-cmd", nil
		case snap.Available("ufw"):
			return "ufw", nil
		case snap.Available("iptables"):
			return "iptables", nil
		}
	}
	return "iptables", nil
}

// LogReader returns the executable for reading system logs.
func (r *serviceResolver) LogReader(remote bool) (string, error) {
	if !remote {
		if r.cap.HasJournalctl {
			return "journalctl", nil
		}
		return "", fmt.Errorf("%w: no log reader (journalctl) available", core.ErrCapabilityMissing)
	}
	if snap := r.ctx.CapabilitySnapshot(); snap != nil && snap.Available("journalctl") {
		return "journalctl", nil
	}
	return "journalctl", nil
}

// ---------------------------------------------------------------------------
// hostResolver — unified host identity consumer (Phase 2.7)
// ---------------------------------------------------------------------------

type hostResolver struct {
	ctx core.Context
}

// Host returns the observed identity of the CURRENT target. The second return is
// false when no identity is available, so callers can decide how to degrade.
func (r *hostResolver) Host() (*snapshot.HostSnapshot, bool) {
	h := r.ctx.HostSnapshot()
	return h, h != nil
}

// HostFor returns the observed identity for an ARBITRARY target.
func (r *hostResolver) HostFor(target core.TargetHost) (*snapshot.HostSnapshot, bool) {
	return r.ctx.HostSnapshotFor(target)
}

// ---------------------------------------------------------------------------
// packageResolver — package-manager selection + verb → argv
// ---------------------------------------------------------------------------

type packageResolver struct {
	ctx core.Context
	cap core.CapabilityContext
}

// Manager resolves the package-manager executable from the observed host
// identity. When the identity is unknown or empty, it returns
// ErrCapabilityMissing so the plan fails fast instead of guessing.
func (r *packageResolver) Manager() (string, error) {
	h := r.ctx.HostSnapshot()
	if h == nil {
		return "", fmt.Errorf("%w: no host identity available to select a package manager", core.ErrCapabilityMissing)
	}
	if pm := packageManagerFor(h.OS, h.Platform); pm != "" {
		return pm, nil
	}
	return "", fmt.Errorf("%w: no package manager for os=%q platform=%q", core.ErrCapabilityMissing, h.OS, h.Platform)
}

// Command returns the (executable, args) tuple for a package verb on the
// current target's host. ALL distro-specific verbs live here in the platform
// bridge; the package builtin stays pure intent. ErrCapabilityMissing is
// returned when the host identity or the verb is unsupported.
func (r *packageResolver) Command(verb PackageVerb, pkgs []string) (string, []string, error) {
	// MUST fix (GPT review): never let a package token be reinterpreted as a
	// flag by the package manager (e.g. "apt-get install --allow-unauthenticated"
	// or "rpm --nodeps"). Inputs are argv elements, so a leading '-' is the only
	// injection vector — reject it at the bridge.
	for _, p := range pkgs {
		if err := core.SafeToken(p); err != nil {
			return "", nil, fmt.Errorf("%w: %v", core.ErrCapabilityMissing, err)
		}
	}
	pm, err := r.Manager()
	if err != nil {
		return "", nil, err
	}
	args, err := packageArgs(pm, verb, pkgs)
	if err != nil {
		return "", nil, err
	}
	return pm, args, nil
}

// PackageVerb is a package-management action expressed as business intent. The
// Resolver turns it into the distro-specific invocation (PackageCommand), so a
// builtin handler never names a distro tool or its flags.
type PackageVerb string

const (
	PkgInstall PackageVerb = "install"
	PkgRemove  PackageVerb = "remove"
	PkgUpdate  PackageVerb = "update"
	PkgList    PackageVerb = "list"
)

// packageManagerFor resolves a package-manager executable from the host OS and
// distro Platform string. Preference order: Platform (precise) overrides OS.
func packageManagerFor(os, platform string) string {
	switch platform {
	case "debian", "ubuntu", "kali", "linuxmint", "pop", "raspbian":
		return "apt-get"
	case "fedora":
		return "dnf"
	case "rhel", "centos", "rocky", "almalinux", "ol", "amzn":
		return "dnf"
	case "alpine":
		return "apk"
	case "opensuse", "sles", "suse":
		return "zypper"
	case "arch", "manjaro", "endeavouros":
		return "pacman"
	}
	// Fall back to OS for the obvious single-tool platforms.
	switch os {
	case "darwin":
		return "brew"
	case "freebsd":
		return "pkg"
	}
	return ""
}

// packageArgs maps a package verb to the distro-specific argument vector for
// the resolved package manager. Pure data: no IO, no exec.
func packageArgs(pm string, verb PackageVerb, pkgs []string) ([]string, error) {
	switch verb {
	case PkgInstall:
		switch pm {
		case "apt-get", "dnf", "yum", "zypper":
			return append([]string{"install", "-y"}, pkgs...), nil
		case "apk":
			return append([]string{"add"}, pkgs...), nil
		case "pacman":
			return append([]string{"-S", "--noconfirm"}, pkgs...), nil
		case "brew":
			return append([]string{"install"}, pkgs...), nil
		}
	case PkgRemove:
		switch pm {
		case "apt-get", "dnf", "yum", "zypper":
			return append([]string{"remove", "-y"}, pkgs...), nil
		case "apk":
			return append([]string{"del"}, pkgs...), nil
		case "pacman":
			return append([]string{"-R", "--noconfirm"}, pkgs...), nil
		case "brew":
			return append([]string{"uninstall"}, pkgs...), nil
		}
	case PkgUpdate:
		switch pm {
		case "apt-get", "apk":
			return []string{"update"}, nil
		case "dnf", "yum":
			return []string{"makecache"}, nil
		case "zypper":
			return []string{"refresh"}, nil
		case "pacman":
			return []string{"-Sy"}, nil
		case "brew":
			return []string{"update"}, nil
		}
	case PkgList:
		switch pm {
		case "apt-get":
			return []string{"list", "--installed"}, nil
		case "dnf", "yum":
			return []string{"list", "installed"}, nil
		case "apk":
			return []string{"info"}, nil
		case "zypper":
			return []string{"search", "--installed-only"}, nil
		case "pacman":
			return []string{"-Q"}, nil
		case "brew":
			return []string{"list"}, nil
		}
	}
	return nil, fmt.Errorf("%w: unsupported package verb %q for %q", core.ErrCapabilityMissing, verb, pm)
}
