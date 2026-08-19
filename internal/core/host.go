package core

import "fmt"

// TargetHost describes the machine where an operation's commands execute.
//
// A zero TargetHost means "run locally on the machine running OpsCore"
// (the Phase 0 behaviour). A non-zero TargetHost switches CommandStep to the
// SSH transport — this is the "control plane manages a remote host" path.
//
// This is intentionally a flat connection spec, not a host registry. A proper
// Host Registry (multi-host, grouped, capability-cached) is a Phase 2/3 item;
// for now a single target per request is enough to exercise the transport.
type TargetHost struct {
	Address string // IP or hostname (required)
	Port    int    // SSH port; 0 means 22
	User    string // SSH user

	// Auth: provide exactly one of the following.
	Password string // password auth
	KeyPath  string // path to a PEM private key file
	KeyBytes []byte // inline PEM private key (tests / inline config)

	// InsecureIgnoreHostKey bypasses known_hosts verification.
	// TEST/DEV ONLY — production must verify host keys.
	InsecureIgnoreHostKey bool

	// Sudo, when true, runs the remote command via `sudo -n` (non-interactive).
	// Needed when the SSH user is unprivileged (most servers disable direct
	// root login). Requires NOPASSWD sudoers for the relevant commands; a
	// missing password fails fast with a clear stderr message.
	// Never set for the in-process demo host (it does not emulate sudo).
	Sudo bool
}

// TargetKey is the stable identity of a target used for per-target caching of
// discovered snapshots (Phase 2.6). It is protocol-agnostic: local, ssh,
// future winrm/agent all produce a distinct key, so the Resolver never needs to
// know the transport. A zero TargetHost maps to the sentinel "local".
type TargetKey string

const localTargetKey TargetKey = "local"

// Key returns the TargetKey for this host. The local sentinel is used when no
// target is set; remote keys are "ssh://user@address:port".
func (t TargetHost) Key() TargetKey {
	if t.IsZero() {
		return localTargetKey
	}
	return TargetKey(fmt.Sprintf("ssh://%s@%s:%d", t.User, t.Address, t.PortOrDefault()))
}

// IsZero reports whether no target was specified (i.e. execute locally).
func (t TargetHost) IsZero() bool { return t.Address == "" }

// PortOrDefault returns the configured port or 22 when unset.
func (t TargetHost) PortOrDefault() int {
	if t.Port == 0 {
		return 22
	}
	return t.Port
}
