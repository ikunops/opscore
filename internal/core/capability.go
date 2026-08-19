package core

import (
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// CapabilityContext describes what the target system can do.
// The Executor and Handlers consult this to decide HOW to execute
// (e.g. systemctl vs service vs nothing). It is the live, boolean
// DECISIONING surface — kept small and cheap on purpose (ADR-009).
type CapabilityContext struct {
	OS             string
	Arch           string
	ServiceManager string // "systemctl", "service", or ""
	HasSystemctl   bool
	HasFirewalld   bool
	HasUFW         bool
	HasIptables    bool
	HasDocker      bool
	HasJournalctl  bool
	IsRoot         bool
}

// NewCapabilityContext derives the fast decisioning surface from an observed
// CapabilitySnapshot. One-way: Snapshot -> Context. The Context (booleans) must
// never feed back into the Snapshot — see architecture review / ADR-009. When
// s is nil, a zero CapabilityContext (local GOOS/GOARCH) is returned.
func NewCapabilityContext(s *snapshot.CapabilitySnapshot) CapabilityContext {
	cap := CapabilityContext{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	if s == nil || s.Items == nil {
		cap.IsRoot = os.Geteuid() == 0
		return cap
	}
	if info, ok := s.Items["systemd"]; ok && info.Available {
		cap.HasSystemctl = true
		cap.ServiceManager = "systemctl"
	} else if info, ok := s.Items["service"]; ok && info.Available {
		cap.ServiceManager = "service"
	}
	if info, ok := s.Items["firewalld"]; ok && info.Available {
		cap.HasFirewalld = true
	}
	if info, ok := s.Items["ufw"]; ok && info.Available {
		cap.HasUFW = true
	}
	if info, ok := s.Items["iptables"]; ok && info.Available {
		cap.HasIptables = true
	}
	if info, ok := s.Items["docker"]; ok && info.Available {
		cap.HasDocker = true
	}
	if info, ok := s.Items["journalctl"]; ok && info.Available {
		cap.HasJournalctl = true
	}
	cap.IsRoot = os.Geteuid() == 0
	return cap
}

// DetectCapability probes the current (local) machine and returns the
// decisioning surface. Kept for backward compatibility and tests; new code
// should prefer Context.Build(), which also attaches a CapabilitySnapshot
// alongside the derived CapabilityContext.
func DetectCapability() CapabilityContext {
	return NewCapabilityContext(detectLocalCapabilitySnapshot())
}

// detectLocalCapabilitySnapshot probes the local machine and returns a
// SourceLocal CapabilitySnapshot. The boolean CapabilityContext used for
// decisioning is derived from this via NewCapabilityContext, keeping a single
// source of truth for "what the local host can do".
func detectLocalCapabilitySnapshot() *snapshot.CapabilitySnapshot {
	items := map[string]snapshot.CapabilityInfo{}
	mark := func(name string, avail bool) {
		items[name] = snapshot.CapabilityInfo{Name: name, Available: avail}
	}

	if _, err := exec.LookPath("systemctl"); err == nil {
		mark("systemd", true)
	} else if _, err := exec.LookPath("service"); err == nil {
		mark("service", true)
	}
	mark("firewalld", lookpath("firewall-cmd"))
	mark("ufw", lookpath("ufw"))
	mark("iptables", lookpath("iptables"))
	mark("docker", lookpath("docker"))
	mark("journalctl", lookpath("journalctl"))

	return &snapshot.CapabilitySnapshot{
		HostID:      localHostID(),
		Version:     1,
		Items:       items,
		CollectedAt: time.Now(),
		Source:      snapshot.SourceLocal,
	}
}

func lookpath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func localHostID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "local"
}
