package platform

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// mk builds a Context carrying only the given decisioning CapabilityContext
// (no observed CapabilitySnapshot), mirroring how a Context is constructed for
// local execution when no snapshot enrichment has happened.
func mk(cap core.CapabilityContext) core.Context {
	return core.NewContext().WithCapability(cap).Build()
}

// buildCtxWithSnapshot builds a Context whose CapabilitySnapshot carries the
// given capability availability flags — the shape produced by an SSH probe or
// a host-registry hint (Phase 2.6).
func buildCtxWithSnapshot(avail map[string]bool) core.Context {
	items := map[string]snapshot.CapabilityInfo{}
	for k, v := range avail {
		items[k] = snapshot.CapabilityInfo{Name: k, Available: v}
	}
	snap := &snapshot.CapabilitySnapshot{
		HostID: "test", Items: items, Source: snapshot.SourceSSH,
	}
	return core.NewContext().WithCapabilitySnapshot(snap).Build()
}

func TestResolver_ServiceManager(t *testing.T) {
	// Local, systemctl present.
	r := New(mk(core.CapabilityContext{ServiceManager: "systemctl"}))
	if got, err := r.ServiceManager(false); err != nil || got != "systemctl" {
		t.Fatalf("local systemctl: got %q err %v", got, err)
	}
	// Local, service (OpenRC/BusyBox) present.
	r = New(mk(core.CapabilityContext{ServiceManager: "service"}))
	if got, err := r.ServiceManager(false); err != nil || got != "service" {
		t.Fatalf("local service: got %q err %v", got, err)
	}
	// Local, no manager -> error.
	r = New(mk(core.CapabilityContext{}))
	if _, err := r.ServiceManager(false); err == nil || !errors.Is(err, core.ErrCapabilityMissing) {
		t.Fatalf("local no-manager expected ErrCapabilityMissing, got %v", err)
	}
	// Remote -> dominant default regardless of local capability (no snapshot
	// enriched; resolver falls back to the dominant Linux tool).
	r = New(mk(core.CapabilityContext{}))
	if got, err := r.ServiceManager(true); err != nil || got != "systemctl" {
		t.Fatalf("remote default: got %q err %v", got, err)
	}
}

func TestResolver_PacketFilter(t *testing.T) {
	cases := []struct {
		name string
		cap  core.CapabilityContext
		want string
	}{
		{"firewalld", core.CapabilityContext{HasFirewalld: true}, "firewall-cmd"},
		{"ufw", core.CapabilityContext{HasUFW: true}, "ufw"},
		{"iptables", core.CapabilityContext{HasIptables: true}, "iptables"},
	}
	for _, tc := range cases {
		r := New(mk(tc.cap))
		if got, err := r.PacketFilter(false); err != nil || got != tc.want {
			t.Fatalf("%s: got %q err %v", tc.name, got, err)
		}
	}
	// Local, nothing available -> error.
	r := New(mk(core.CapabilityContext{}))
	if _, err := r.PacketFilter(false); err == nil || !errors.Is(err, core.ErrCapabilityMissing) {
		t.Fatalf("local no-filter expected ErrCapabilityMissing, got %v", err)
	}
	// Remote -> iptables default.
	if got, err := r.PacketFilter(true); err != nil || got != "iptables" {
		t.Fatalf("remote filter default: got %q err %v", got, err)
	}
}

func TestResolver_LogReader(t *testing.T) {
	r := New(mk(core.CapabilityContext{HasJournalctl: true}))
	if got, err := r.LogReader(false); err != nil || got != "journalctl" {
		t.Fatalf("local journalctl: got %q err %v", got, err)
	}
	r = New(mk(core.CapabilityContext{}))
	if _, err := r.LogReader(false); err == nil || !errors.Is(err, core.ErrCapabilityMissing) {
		t.Fatalf("local no-journal expected ErrCapabilityMissing, got %v", err)
	}
	if got, err := r.LogReader(true); err != nil || got != "journalctl" {
		t.Fatalf("remote log default: got %q err %v", got, err)
	}
}

// TestResolver_RemoteConsumesSnapshot verifies the Phase 2.6 path: when a
// CapabilitySnapshot is present on the Context, the remote resolver prefers it
// over the dominant-tool fallback.
func TestResolver_RemoteConsumesSnapshot(t *testing.T) {
	// Remote host with firewalld (not iptables/ufw): PacketFilter must pick
	// firewall-cmd even though the fallback would be iptables.
	ctx := buildCtxWithSnapshot(map[string]bool{
		"firewalld": true, "iptables": false, "ufw": false,
	})
	if got, err := New(ctx).PacketFilter(true); err != nil || got != "firewall-cmd" {
		t.Fatalf("remote firewalld snapshot: got %q err %v", got, err)
	}

	// Remote host with only ufw.
	ctx = buildCtxWithSnapshot(map[string]bool{
		"firewalld": false, "iptables": false, "ufw": true,
	})
	if got, err := New(ctx).PacketFilter(true); err != nil || got != "ufw" {
		t.Fatalf("remote ufw snapshot: got %q err %v", got, err)
	}

	// Remote host with neither systemd nor service: ServiceManager still falls
	// back to the dominant tool (no hard error) so the plan builds.
	ctx = buildCtxWithSnapshot(map[string]bool{
		"systemd": false, "service": false,
	})
	if got, err := New(ctx).ServiceManager(true); err != nil || got != "systemctl" {
		t.Fatalf("remote no-manager fallback: got %q err %v", got, err)
	}
}
