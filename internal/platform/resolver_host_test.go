package platform

import (
	"errors"
	"runtime"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// mkHost builds a Context whose HostSnapshot is the given identity. Mirrors how
// a control plane would attach an observed (local baked-in, SSH-probed, or
// registry-hinted) host identity before dispatch.
func mkHost(h *snapshot.HostSnapshot) core.Context {
	return core.NewContext().WithHostSnapshot(h).Build()
}

// TestResolver_Host_Local verifies Phase 2.7 unification: for local execution
// the Resolver exposes the HostSnapshot baked in at Build() as the single
// source of host identity.
func TestResolver_Host_Local(t *testing.T) {
	h, ok := New(core.NewContext().Build()).Host()
	if !ok || h == nil {
		t.Fatal("local Host() expected a non-nil HostSnapshot")
	}
	if h.Source != snapshot.SourceLocal {
		t.Fatalf("local host source = %q, want local", h.Source)
	}
	if h.OS != runtime.GOOS {
		t.Fatalf("local host OS = %q, want %q", h.OS, runtime.GOOS)
	}
}

// TestResolver_Host_ConsumesAttachedSnapshot verifies a handler reads the
// attached (remote/registry) host identity through Resolver.Host().
func TestResolver_Host_ConsumesAttachedSnapshot(t *testing.T) {
	h := &snapshot.HostSnapshot{
		ID: "10.0.0.9", Name: "edge-9", Address: "10.0.0.9",
		OS: "linux", Arch: "x86_64", Platform: "ubuntu", Version: "22.04",
		Kernel: "5.15.0", User: "ops", Source: snapshot.SourceSSH,
	}
	got, ok := New(mkHost(h)).Host()
	if !ok || got == nil {
		t.Fatal("Host() expected the attached snapshot")
	}
	if got.Platform != "ubuntu" || got.OS != "linux" {
		t.Fatalf("Host() = %+v, want ubuntu/linux identity", got)
	}
}

// TestResolver_HostFor_ArbitraryTarget verifies the host-identity counterpart
// of HostSnapshotFor: an arbitrary (e.g. Batch child or Inventory) target's
// identity is readable without disturbing the current target.
func TestResolver_HostFor_ArbitraryTarget(t *testing.T) {
	local := core.NewContext().Build()
	r := New(local)

	if _, ok := r.Host(); !ok {
		t.Fatal("local Host() expected non-nil")
	}
	remote := core.TargetHost{Address: "10.0.0.5", User: "ops"}
	rsnap := &snapshot.HostSnapshot{
		ID: "10.0.0.5", Platform: "centos", OS: "linux", Source: snapshot.SourceSSH,
	}
	local.SetHostSnapshot(remote, rsnap)

	rh, ok := r.HostFor(remote)
	if !ok || rh == nil || rh.Platform != "centos" {
		t.Fatalf("HostFor(remote) = %+v ok=%v, want centos", rh, ok)
	}
	// Current target (local) is untouched.
	if lh, _ := r.Host(); lh.Platform != "" {
		t.Fatalf("local identity leaked remote platform: %+v", lh)
	}
}

// TestResolver_PackageManager verifies the canonical HostSnapshot consumer:
// the package manager is derived from the OBSERVED host identity, not a
// hard-coded distro assumption in the handler.
func TestResolver_PackageManager(t *testing.T) {
	cases := []struct {
		os, platform, want string
	}{
		{"linux", "ubuntu", "apt-get"},
		{"linux", "debian", "apt-get"},
		{"linux", "centos", "dnf"},
		{"linux", "fedora", "dnf"},
		{"linux", "alpine", "apk"},
		{"linux", "opensuse", "zypper"},
		{"linux", "arch", "pacman"},
		{"darwin", "", "brew"},
		{"freebsd", "", "pkg"},
	}
	for _, tc := range cases {
		h := &snapshot.HostSnapshot{OS: tc.os, Platform: tc.platform}
		got, err := New(mkHost(h)).PackageManager()
		if err != nil || got != tc.want {
			t.Fatalf("PackageManager(os=%q platform=%q) = %q err %v, want %q",
				tc.os, tc.platform, got, err, tc.want)
		}
	}
}

// TestResolver_PackageManager_Unknown verifies an unknown/empty identity fails
// fast with ErrCapabilityMissing rather than guessing a tool.
func TestResolver_PackageManager_Unknown(t *testing.T) {
	cases := []struct {
		os, platform string
	}{
		{"linux", ""},   // no platform -> cannot decide apt vs dnf vs ...
		{"windows", ""}, // no native ops package manager
	}
	for _, tc := range cases {
		h := &snapshot.HostSnapshot{OS: tc.os, Platform: tc.platform}
		if _, err := New(mkHost(h)).PackageManager(); err == nil || !errors.Is(err, core.ErrCapabilityMissing) {
			t.Fatalf("PackageManager(os=%q platform=%q) expected ErrCapabilityMissing, got %v",
				tc.os, tc.platform, err)
		}
	}
}

// TestResolver_PackageManager_NoIdentity verifies a context with no host
// identity (e.g. a remote probe failed and nothing was attached) reports the
// missing capability cleanly.
func TestResolver_PackageManager_NoIdentity(t *testing.T) {
	// A bare Context with capability set but no HostSnapshot attached.
	ctx := core.NewContext().WithCapability(core.CapabilityContext{OS: "linux"}).Build()
	// Strip the baked-in local snapshot so Host() is nil.
	ctx.SetHostSnapshot(core.TargetHost{}, nil)
	if _, err := New(ctx).PackageManager(); err == nil || !errors.Is(err, core.ErrCapabilityMissing) {
		t.Fatalf("PackageManager with no identity expected ErrCapabilityMissing, got %v", err)
	}
}
