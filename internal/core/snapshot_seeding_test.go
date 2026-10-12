package core

// T392 (ADR-085/086, Phase 53 defect D): Build() must never cache the
// auto-detected LOCAL observations under a remote (non-zero) target's key.
// Explicitly supplied snapshots describe the TARGET and still seed it; the
// local (zero) target keeps its auto-detected facts unchanged.

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

func TestT392BuildDoesNotSeedLocalSnapshotForRemoteTarget(t *testing.T) {
	remote := TargetHost{Address: "10.0.0.9", Port: 22, User: "root"}

	ctx := NewContext().WithTarget(remote).Build()
	if snap := ctx.CapabilitySnapshot(); snap != nil {
		t.Fatalf("T392: remote target must start with NO capability snapshot, got source=%s", snap.Source)
	}
	if hs := ctx.HostSnapshot(); hs != nil {
		t.Fatalf("T392: remote target must start with NO host snapshot, got id=%s", hs.ID)
	}

	// An explicitly supplied snapshot describes the TARGET (projection /
	// read-only attachment) and must still seed the target's slot.
	explicit := &snapshot.CapabilitySnapshot{Items: map[string]snapshot.CapabilityInfo{}, Source: snapshot.SourceCache}
	ctx2 := NewContext().WithTarget(remote).WithCapabilitySnapshot(explicit).Build()
	if ctx2.CapabilitySnapshot() != explicit {
		t.Fatal("T392: an explicitly supplied snapshot must seed the remote target's slot")
	}

	// The local (zero) target keeps its auto-detected snapshots — unchanged.
	ctx3 := NewContext().Build()
	if ctx3.CapabilitySnapshot() == nil || ctx3.HostSnapshot() == nil {
		t.Fatal("T392: local target must keep its auto-detected snapshots")
	}
}
