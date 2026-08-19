package inventory

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// TestBuild_ConsumesSnapshots verifies the Phase 2.7 unified consumer reads
// BOTH the HostSnapshot (identity) and CapabilitySnapshot (capabilities) through
// the Context, and resolves the package manager from identity — with no
// handler-side distro hard-coding.
func TestBuild_ConsumesSnapshots(t *testing.T) {
	host := &snapshot.HostSnapshot{ID: "h1", OS: "linux", Platform: "ubuntu", Version: "22.04"}
	caps := &snapshot.CapabilitySnapshot{
		HostID: "h1",
		Items: map[string]snapshot.CapabilityInfo{
			"systemd": {Name: "systemd", Available: true},
			"ufw":     {Name: "ufw", Available: false},
		},
	}
	ctx := core.NewContext().WithHostSnapshot(host).WithCapabilitySnapshot(caps).Build()

	v := Build(ctx)
	if v.Host == nil || v.Host.Platform != "ubuntu" {
		t.Fatalf("host identity not consumed: %+v", v.Host)
	}
	if v.Capability == nil {
		t.Fatal("capability snapshot not consumed")
	}
	if v.PackageManager != "apt-get" {
		t.Fatalf("PackageManager = %q, want apt-get", v.PackageManager)
	}
	if len(v.Capabilities) != 2 {
		t.Fatalf("expected 2 flattened capabilities, got %d", len(v.Capabilities))
	}
}

// TestBuild_OtherDistros verifies the package manager resolves per identity
// (the canonical Resolver consumer behavior, no hard-coded distro). Platform is
// preferred; OS is the fallback for single-tool platforms like darwin.
func TestBuild_OtherDistros(t *testing.T) {
	cases := []struct {
		os, platform, wantPM string
	}{
		{"linux", "centos", "dnf"},
		{"linux", "alpine", "apk"},
		{"linux", "arch", "pacman"},
		{"darwin", "", "brew"},
	}
	for _, c := range cases {
		host := &snapshot.HostSnapshot{ID: "x", OS: c.os, Platform: c.platform}
		ctx := core.NewContext().WithHostSnapshot(host).Build()
		if v := Build(ctx); v.PackageManager != c.wantPM {
			t.Fatalf("os=%s platform=%s PackageManager=%q want %q", c.os, c.platform, v.PackageManager, c.wantPM)
		}
	}
}

// TestBuild_UnknownIdentity_EmptyPackageManager verifies graceful degradation
// when the observed OS is not a recognized package-manager target: the identity
// is still reported, but PackageManager is empty (callers degrade, never guess).
func TestBuild_UnknownIdentity_EmptyPackageManager(t *testing.T) {
	host := &snapshot.HostSnapshot{ID: "x", OS: "plan9", Platform: ""}
	ctx := core.NewContext().WithHostSnapshot(host).Build()
	v := Build(ctx)
	if v.Host == nil {
		t.Fatal("expected host identity present")
	}
	if v.PackageManager != "" {
		t.Fatalf("expected empty package manager for unknown OS, got %q", v.PackageManager)
	}
}

// fakeSource is a Source implementation used to verify the BuildFrom seam:
// a caller can supply a DIFFERENT observation surface than the live Context and
// Inventory will project it without changing the projection logic.
type fakeSource struct {
	host *snapshot.HostSnapshot
	cap  *snapshot.CapabilitySnapshot
	pm   string
}

func (f fakeSource) Host(core.Context) *snapshot.HostSnapshot             { return f.host }
func (f fakeSource) Capability(core.Context) *snapshot.CapabilitySnapshot { return f.cap }
func (f fakeSource) PackageManager(core.Context) string                   { return f.pm }

// TestBuildFrom_PlugabbleSource verifies the v0.1 SHOULD polish: BuildFrom
// projects through an explicit Source, so a cached read-back or replay fixture
// can be swapped in without touching the canonical projection.
func TestBuildFrom_PlugabbleSource(t *testing.T) {
	src := fakeSource{
		host: &snapshot.HostSnapshot{ID: "cached", OS: "linux", Platform: "debian"},
		cap: &snapshot.CapabilitySnapshot{
			HostID: "cached",
			Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
		},
		pm: "apt-get",
	}
	// A Context with NO own identity — proves BuildFrom uses src, not ctx.
	ctx := core.NewContext().Build()
	v := BuildFrom(ctx, src)
	if v.Host == nil || v.Host.ID != "cached" {
		t.Fatalf("BuildFrom did not use the custom source host: %+v", v.Host)
	}
	if v.Capability == nil || v.Capability.HostID != "cached" {
		t.Fatal("BuildFrom did not use the custom source capability")
	}
	if v.PackageManager != "apt-get" {
		t.Fatalf("PackageManager = %q, want apt-get", v.PackageManager)
	}
}
