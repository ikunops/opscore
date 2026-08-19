package capability

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

// expectedProbeNames is the deterministic, alphabetically-sorted probe set.
// Tests assert the SNAPSHOT contains exactly these names (independent of
// whether the tool is installed on the test host), so the assertion holds on
// Linux, macOS, and Windows CI. Order must match Snapshot's sort by Name.
var expectedProbeNames = []string{
	"docker", "firewalld", "iptables", "ssh.client", "ssh.server", "systemd", "ufw",
}

func TestSnapshot_NamesCompleteAndSorted(t *testing.T) {
	snap := Snapshot(context.Background())
	if len(snap) != len(expectedProbeNames) {
		t.Fatalf("expected %d capabilities, got %d: %+v", len(expectedProbeNames), len(snap), snap)
	}
	for i, name := range expectedProbeNames {
		if snap[i].Name != name {
			t.Fatalf("capability[%d].Name = %q, want %q (snapshot must be sorted)", i, snap[i].Name, name)
		}
		if snap[i].Available != false && snap[i].Available != true {
			t.Fatalf("capability %q: Available must be a bool", name)
		}
	}
}

func TestSnapshot_Deterministic(t *testing.T) {
	a := Snapshot(context.Background())
	b := Snapshot(context.Background())
	if len(a) != len(b) {
		t.Fatal("snapshot length changed between calls")
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Fatalf("non-deterministic ordering: %q vs %q", a[i].Name, b[i].Name)
		}
	}
}

func TestCollectStep_ReturnsValidJSON(t *testing.T) {
	step := CollectStep{}
	res := step.Execute(core.NewContext().Build())
	if !res.Success {
		t.Fatalf("CollectStep failed: %v", res.Error)
	}
	if step.Describe() != "collect_capability" {
		t.Fatalf("unexpected describe: %q", step.Describe())
	}
	var snap []CapabilityInfo
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, res.Output)
	}
	if len(snap) != len(expectedProbeNames) {
		t.Fatalf("collected %d capabilities, want %d", len(snap), len(expectedProbeNames))
	}
}
