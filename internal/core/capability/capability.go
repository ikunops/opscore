// Package capability implements Capability Discovery for OpsCore — the
// "know what the target can do" half of the control plane (the other half is
// executing Operations).
//
// Design note (per architecture review): Capability is KERNEL STATE, not a
// business operation. It describes the environment so Handlers and the UI can
// decide "can this Operation run here?". It must NOT flow through the
// Operation -> Handler -> Plan -> Executor (shell) chain — a capability probe
// is not a command to change the system. Hence CollectStep is a builtin
// ExecutionStep that computes a snapshot directly, not a CommandStep.
package capability

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// CapabilityInfo is a single discovered capability of the host where OpsCore
// runs (or, in a future slice, the host addressed by Context.Target).
// It is intentionally richer than the boolean CapabilityContext used by
// Handlers for fast decisioning: it carries a version and free-form details.
//
// It is an alias of snapshot.CapabilityInfo so the observation surface has a
// single canonical type (ADR-009) without forcing the core/snapshot package
// to depend on this probe package (which would create an import cycle).
type CapabilityInfo = snapshot.CapabilityInfo

// Probe discovers one capability. Probes are stateless and cheap; Snapshot
// fans them out and collects the results.
type Probe interface {
	Name() string
	Probe(ctx context.Context) CapabilityInfo
}

// allProbes is the default probe set (Linux-centric; extend per platform
// without touching the Snapshot contract).
var allProbes = []Probe{
	systemdProbe{},
	ufwProbe{},
	firewalldProbe{},
	iptablesProbe{},
	dockerProbe{},
	sshClientProbe{},
	sshServerProbe{},
}

// Snapshot runs every registered probe and returns the discovered capabilities,
// sorted by name for deterministic output (important for tests and diffs).
func Snapshot(ctx context.Context) []CapabilityInfo {
	out := make([]CapabilityInfo, 0, len(allProbes))
	for _, p := range allProbes {
		out = append(out, p.Probe(ctx))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Collect builds the frozen, map-keyed CapabilitySnapshot (ADR-009 observation
// surface) from the same stateless probes used by Snapshot. SourceLocal marks
// it as collected in-process on the control plane host.
func Collect(ctx context.Context) *snapshot.CapabilitySnapshot {
	infos := Snapshot(ctx)
	items := make(map[string]snapshot.CapabilityInfo, len(infos))
	for _, info := range infos {
		items[info.Name] = info
	}
	host := "local"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return &snapshot.CapabilitySnapshot{
		HostID:      host,
		Version:     1,
		Items:       items,
		CollectedAt: time.Now(),
		Source:      snapshot.SourceLocal,
	}
}

// HostSnapshotLocal builds the local host's identity snapshot (ADR-009) from
// runtime info plus a best-effort uname. uname is absent on some platforms
// (e.g. bare Windows), in which case OS/Arch come from runtime and the kernel
// field is left empty rather than failing.
func HostSnapshotLocal(ctx context.Context) (*snapshot.HostSnapshot, error) {
	host, err := os.Hostname()
	if err != nil {
		host = "local"
	}
	user := "root"
	if os.Geteuid() != 0 {
		if u := os.Getenv("USER"); u != "" {
			user = u
		} else if u := os.Getenv("USERNAME"); u != "" {
			user = u
		}
	}

	hs := &snapshot.HostSnapshot{
		ID:          host,
		Name:        host,
		Address:     host,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		User:        user,
		CollectedAt: time.Now(),
		Source:      snapshot.SourceLocal,
	}

	// Best-effort kernel / platform enrichment via uname. Not fatal if missing.
	if avail, out := probeVersion(ctx, "uname", "-srm"); avail && out != "" {
		// e.g. "Linux 6.2.0-39-generic x86_64"
		fields := strings.Fields(out)
		if len(fields) >= 1 {
			hs.OS = strings.ToLower(fields[0])
		}
		if len(fields) >= 3 {
			hs.Arch = fields[len(fields)-1]
		}
	}
	if avail, out := probeVersion(ctx, "uname", "-r"); avail && out != "" {
		hs.Kernel = out
	}
	if avail, out := probeVersion(ctx, "sh", "-c", "cat /etc/os-release 2>/dev/null | grep -E '^(ID|VERSION_ID)=' | tr '\\n' ' '"); avail && out != "" {
		parseOSRelease(out, hs)
	}
	return hs, nil
}

// parseOSRelease fills Platform/Version from the ID= / VERSION_ID= lines of an
// /etc/os-release dump. Defensive: ignores malformed input.
func parseOSRelease(dump string, hs *snapshot.HostSnapshot) {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if eq := strings.Index(line, "="); eq > 0 {
			key := line[:eq]
			val := strings.Trim(strings.TrimPrefix(line[eq+1:], "\""), "\"")
			switch key {
			case "ID":
				hs.Platform = val
			case "VERSION_ID":
				hs.Version = val
			}
		}
	}
}

// CollectStep is the builtin ExecutionStep used by the
// system.host.capability.list Operation. It computes the snapshot in-process
// and returns it as JSON — no shell, no remote hop, honoring the rule that
// capability discovery is kernel state, not a system mutation.
type CollectStep struct{}

func (CollectStep) Describe() string { return "collect_capability" }

func (CollectStep) Execute(ctx core.Context) core.StepResult {
	snap := Snapshot(ctx)
	b, err := json.Marshal(snap)
	if err != nil {
		return core.StepResult{
			StepName: "collect_capability",
			Success:  false,
			Error:    err,
		}
	}
	return core.StepResult{
		StepName: "collect_capability",
		Success:  true,
		Output:   string(b),
	}
}

// probeVersion returns (available, version) for a binary. It only runs the
// command when the binary is on PATH, so on hosts without the tool (or on
// non-Linux dev/sandbox machines) it returns quickly with available=false and
// never shells out to a missing executable.
func probeVersion(ctx context.Context, exe string, args ...string) (bool, string) {
	if _, err := exec.LookPath(exe); err != nil {
		return false, ""
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Binary exists but the version probe failed (e.g. daemon down).
		return true, ""
	}
	return true, strings.TrimSpace(string(out))
}
