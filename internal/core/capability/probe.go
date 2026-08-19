package capability

import (
	"context"
	"os/exec"
	"strings"
)

// --- systemd ----------------------------------------------------------------

type systemdProbe struct{}

func (systemdProbe) Name() string { return "systemd" }

func (systemdProbe) Probe(ctx context.Context) CapabilityInfo {
	avail, out := probeVersion(ctx, "systemctl", "--version")
	info := CapabilityInfo{Name: "systemd", Available: avail}
	if fields := strings.Fields(out); len(fields) >= 2 {
		info.Version = fields[1]
	}
	return info
}

// --- ufw --------------------------------------------------------------------

type ufwProbe struct{}

func (ufwProbe) Name() string { return "ufw" }

func (ufwProbe) Probe(ctx context.Context) CapabilityInfo {
	avail, out := probeVersion(ctx, "ufw", "--version")
	info := CapabilityInfo{Name: "ufw", Available: avail}
	if fields := strings.Fields(out); len(fields) >= 2 {
		info.Version = fields[1]
	}
	return info
}

// --- firewalld --------------------------------------------------------------

type firewalldProbe struct{}

func (firewalldProbe) Name() string { return "firewalld" }

func (firewalldProbe) Probe(ctx context.Context) CapabilityInfo {
	avail, out := probeVersion(ctx, "firewall-cmd", "--version")
	info := CapabilityInfo{Name: "firewalld", Available: avail}
	if out != "" {
		info.Version = strings.TrimSpace(out)
	}
	if avail {
		// Separate probe for runtime state (binary presence != running).
		if _, err := exec.LookPath("systemctl"); err == nil {
			if b, err := exec.CommandContext(ctx, "systemctl", "is-active", "firewalld").CombinedOutput(); err == nil {
				info.Details = map[string]string{"running": strings.TrimSpace(string(b))}
			}
		}
	}
	return info
}

// --- iptables ---------------------------------------------------------------

type iptablesProbe struct{}

func (iptablesProbe) Name() string { return "iptables" }

func (iptablesProbe) Probe(ctx context.Context) CapabilityInfo {
	avail, out := probeVersion(ctx, "iptables", "-V")
	info := CapabilityInfo{Name: "iptables", Available: avail}
	info.Version = strings.TrimPrefix(out, "iptables v")
	return info
}

// --- docker -----------------------------------------------------------------

type dockerProbe struct{}

func (dockerProbe) Name() string { return "docker" }

func (dockerProbe) Probe(ctx context.Context) CapabilityInfo {
	// Prefer the SERVER version (daemon reachable) over the client binary.
	avail, out := probeVersion(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	info := CapabilityInfo{Name: "docker", Available: avail}
	if out == "" && avail {
		// Daemon not reachable; fall back to client version.
		if _, co := probeVersion(ctx, "docker", "version", "--format", "{{.Client.Version}}"); co != "" {
			out = co
		}
	}
	info.Version = out
	return info
}

// --- ssh.client -------------------------------------------------------------

type sshClientProbe struct{}

func (sshClientProbe) Name() string { return "ssh.client" }

func (sshClientProbe) Probe(ctx context.Context) CapabilityInfo {
	// `ssh -V` writes its banner to stderr, so probeVersion (CombinedOutput)
	// already captures it.
	avail, out := probeVersion(ctx, "ssh", "-V")
	info := CapabilityInfo{Name: "ssh.client", Available: avail}
	if out != "" {
		// "OpenSSH_9.0p1, ... (...)" -> take the version token.
		if idx := strings.Index(out, "_"); idx >= 0 {
			rest := out[idx+1:]
			if comma := strings.Index(rest, " "); comma >= 0 {
				rest = rest[:comma]
			}
			if comma := strings.Index(rest, ","); comma >= 0 {
				rest = rest[:comma]
			}
			info.Version = rest
		}
	}
	return info
}

// --- ssh.server -------------------------------------------------------------

type sshServerProbe struct{}

func (sshServerProbe) Name() string { return "ssh.server" }

func (sshServerProbe) Probe(ctx context.Context) CapabilityInfo {
	avail, _ := probeVersion(ctx, "sshd")
	info := CapabilityInfo{Name: "ssh.server", Available: avail}
	if avail {
		if _, err := exec.LookPath("systemctl"); err == nil {
			if b, err := exec.CommandContext(ctx, "systemctl", "is-enabled", "sshd").CombinedOutput(); err == nil {
				info.Details = map[string]string{"enabled": strings.TrimSpace(string(b))}
			}
		}
	}
	return info
}
