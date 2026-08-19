package core

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// TestProbeRemoteCapability_CompleteMatrix exercises the Phase 2.6 remote
// capability probe end-to-end against the embedded SSH server. It asserts the
// probe produces a well-formed snapshot (a COMPLETE capability matrix: every
// known key present with its availability flag), collects the host-identity
// snapshot alongside, and is deterministic across repeated probes (same Hash).
func TestProbeRemoteCapability_CompleteMatrix(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := TargetHost{
		Address:               host,
		Port:                  port,
		User:                  "tester",
		Password:              "testpw",
		InsecureIgnoreHostKey: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caps, hostSnap, err := probeRemoteCapability(ctx, target)
	if err != nil {
		t.Fatalf("probeRemoteCapability: %v", err)
	}
	if caps == nil {
		t.Fatal("nil capability snapshot")
	}
	if caps.HostID != host {
		t.Fatalf("HostID = %q, want %q", caps.HostID, host)
	}
	// The snapshot is a COMPLETE matrix, not just the available tools. This
	// lets the Resolver / UI diff capabilities across hosts and over time.
	for _, name := range []string{"systemd", "service", "firewalld", "ufw", "iptables", "docker", "journalctl"} {
		if _, ok := caps.Get(name); !ok {
			t.Fatalf("snapshot missing capability %q", name)
		}
	}
	if hostSnap == nil || hostSnap.Address != host {
		t.Fatalf("bad host snapshot: %+v", hostSnap)
	}

	// A second probe must be deterministic: identical content => identical hash.
	caps2, _, err := probeRemoteCapability(ctx, target)
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if caps2.Hash() != caps.Hash() {
		t.Fatalf("probe not deterministic: hash %q vs %q", caps2.Hash(), caps.Hash())
	}
	if caps2.Source != snapshot.SourceSSH {
		t.Fatalf("expected SourceSSH, got %q", caps2.Source)
	}
}

// TestContext_SnapshotCacheIsolation verifies the Phase 2.6 observation surface
// is context-bound and per-target isolated (Round 5): snapshots are cached on
// the Context keyed by TargetKey, a WithTarget child starts with a FRESH empty
// cache (no parent leakage), and SetSnapshot on the current target also
// refreshes the fast decisioning CapabilityContext.
func TestContext_SnapshotCacheIsolation(t *testing.T) {
	base := NewContext().Build()

	local := TargetHost{}
	remote := TargetHost{Address: "10.0.0.5", Port: 22, User: "ops"}

	ls := &snapshot.CapabilitySnapshot{
		HostID: "local",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
	}
	rs := &snapshot.CapabilitySnapshot{
		HostID: "10.0.0.5",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: false}},
	}

	base.SetSnapshot(local, ls)
	base.SetSnapshot(remote, rs)

	// Different targets resolve to their own snapshot.
	gotL, okL := base.Snapshot(local)
	if !okL || gotL != ls {
		t.Fatal("local snapshot not cached on base")
	}
	gotR, okR := base.Snapshot(remote)
	if !okR || gotR != rs {
		t.Fatal("remote snapshot not cached on base")
	}

	// A WithTarget child gets a FRESH cache — it must NOT see the parent's
	// snapshots (isolation across Batch fan-out).
	child := WithTarget(base, remote)
	if _, ok := child.Snapshot(remote); ok {
		t.Fatal("child must start with an empty cache (no parent leakage)")
	}
	if child.CapabilitySnapshot() != nil {
		t.Fatal("child must not inherit a cached capability snapshot")
	}

	// SetSnapshot on the child's current target also refreshes Capability().
	child.SetSnapshot(remote, rs)
	if child.Capability().HasSystemctl {
		t.Fatal("remote has no systemd; CapabilityContext must reflect snapshot")
	}
}

// TestEnrichContextForTarget_NoTargetNoop verifies enrichment is a no-op for a
// local (zero) target and for an already-enriched context.
func TestEnrichContextForTarget_NoTargetNoop(t *testing.T) {
	base := NewContext().Build()
	if EnrichContextForTarget(base, TargetHost{}) != base {
		t.Fatal("enrich on zero target must return the same context")
	}
	// Already enriched: must not re-probe / reallocate.
	enriched := NewContext().WithCapabilitySnapshot(&snapshot.CapabilitySnapshot{
		Items:  map[string]snapshot.CapabilityInfo{},
		Source: snapshot.SourceCache,
	}).Build()
	if EnrichContextForTarget(enriched, TargetHost{Address: "1.2.3.4"}).CapabilitySnapshot() != enriched.CapabilitySnapshot() {
		t.Fatal("enrich must not overwrite an existing snapshot")
	}
}
