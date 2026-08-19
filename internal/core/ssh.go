package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
	"golang.org/x/crypto/ssh"
)

// defaultSSHClientPool reuses established *ssh.Client connections across
// RunOverSSH calls. Dialing + the SSH handshake is expensive (TCP + key
// exchange + auth); a remote plan runs several CommandSteps and Batch fans
// out, so a fresh dial per call is a major latency/resource penalty. See
// sshClientPool for the mechanics.
var defaultSSHClientPool = newSSHClientPool(2)

// RunOverSSH executes a command on a remote host over SSH.
//
// SECURITY: the command is built as `executable arg0 arg1 …` where each arg is
// single-quoted individually. We never hand a pre-built shell string to the
// remote shell, so operation-supplied arguments cannot break out of their token
// (no word-splitting / injection). This mirrors the local CommandStep's
// "exec.Command(args...) — never sh -c" invariant.
//
// ctx provides cancellation; timeout bounds a single command. If both fire, the
// remote session is killed. The underlying SSH connection is returned to the
// pool afterwards (it is reusable — only the session died), so a follow-up
// CommandStep on the same target reuses the handshake.
func RunOverSSH(ctx context.Context, target TargetHost, executable string, args []string, env map[string]string, timeout time.Duration) (stdout, stderr string, err error) {
	if target.IsZero() {
		return "", "", fmt.Errorf("ssh: empty target host")
	}
	client, err := defaultSSHClientPool.Get(ctx, target)
	if err != nil {
		return "", "", err
	}
	var putDead bool
	// Return the connection to the pool. A killed session / cancelled ctx does
	// NOT kill the connection, so we only mark dead on transport-level errors.
	defer defaultSSHClientPool.Put(client, putDead)

	session, err := client.NewSession()
	if err != nil {
		putDead = true
		return "", "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Servers may reject env requests (AcceptEnv); best-effort only.
	for k, v := range env {
		_ = session.Setenv(k, v)
	}

	var so, se bytes.Buffer
	session.Stdout = &so
	session.Stderr = &se

	remoteCmd := buildRemoteCommand(target, executable, args)

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	done := make(chan error, 1)
	go func() { done <- session.Run(remoteCmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return so.String(), se.String(), ctx.Err()
	case runErr := <-done:
		if runErr != nil {
			if isTransportDeath(runErr) {
				putDead = true
			}
			return so.String(), se.String(), runErr
		}
		return so.String(), se.String(), nil
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return so.String(), se.String(), fmt.Errorf("ssh command timed out after %s", timeout)
	}
}

// dialSSH establishes a fresh SSH connection for the target. Extracted from
// RunOverSSH so the pool can dial on a cache miss. cfg.Timeout bounds the
// handshake (a fixed, sane value — a command timeout of 0 must not make the
// dial block forever).
func dialSSH(ctx context.Context, t TargetHost) (*ssh.Client, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("ssh: empty target host")
	}
	auth, err := sshAuthMethods(t)
	if err != nil {
		return nil, err
	}

	var hostKeyCB ssh.HostKeyCallback
	if t.InsecureIgnoreHostKey {
		hostKeyCB = ssh.InsecureIgnoreHostKey()
	} else {
		hostKeyCB = func(string, net.Addr, ssh.PublicKey) error {
			return fmt.Errorf("ssh: host key verification required; enable InsecureIgnoreHostKey for dev/test only")
		}
	}

	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCB,
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(t.Address, strconv.Itoa(t.PortOrDefault()))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return client, nil
}

// sshClientPool reuses established *ssh.Client connections keyed by target
// identity. It keeps a bounded number of idle clients per target (LIFO) and
// vends them on Get.
//
// A client is returned via Put. On transport death (the underlying connection
// dropped, e.g. io.EOF from session.Run) call Put with dead=true to discard it
// instead of recycling — a dead client must never be handed to another caller.
//
// A popped idle client is probed with a keepalive request before reuse: servers
// may close idle connections (TCP keepalive / auth timeout), and reusing a dead
// socket would fail obscurely. The keepalive round-trip is far cheaper than a
// full handshake, so the trade-off is worth it.
type sshClientPool struct {
	mu      sync.Mutex
	maxIdle int
	idle    map[string][]*ssh.Client // key -> idle clients (LIFO)
	known   map[*ssh.Client]string   // client -> key, so Put needs no key arg
	dial    func(ctx context.Context, t TargetHost) (*ssh.Client, error)
}

func newSSHClientPool(maxIdle int) *sshClientPool {
	if maxIdle <= 0 {
		maxIdle = 2
	}
	return &sshClientPool{
		maxIdle: maxIdle,
		idle:    make(map[string][]*ssh.Client),
		known:   make(map[*ssh.Client]string),
		dial:    dialSSH,
	}
}

// key derives a stable identity for a target: user@address:port plus a
// fingerprint of the auth material (password / key path / inline key bytes).
// Two targets that would yield an identical *ssh.ClientConfig share a slot, so
// reusing a connection between them is safe.
func (p *sshClientPool) key(t TargetHost) string {
	var auth string
	switch {
	case t.Password != "":
		auth = "pw:" + t.Password
	case t.KeyPath != "":
		auth = "path:" + t.KeyPath
	case len(t.KeyBytes) > 0:
		sum := sha256.Sum256(t.KeyBytes)
		auth = "bytes:" + hex.EncodeToString(sum[:])
	default:
		auth = "none"
	}
	return fmt.Sprintf("%s@%s:%d#%s", t.User, t.Address, t.PortOrDefault(), auth)
}

// Get returns a usable SSH client for the target, reusing an idle one when
// possible.
func (p *sshClientPool) Get(ctx context.Context, t TargetHost) (*ssh.Client, error) {
	k := p.key(t)

	p.mu.Lock()
	var reused *ssh.Client
	if list := p.idle[k]; len(list) > 0 {
		reused = list[len(list)-1]
		p.idle[k] = list[:len(list)-1]
	}
	p.mu.Unlock()

	if reused != nil {
		if sshClientAlive(reused, 3*time.Second) {
			p.mu.Lock()
			p.known[reused] = k
			p.mu.Unlock()
			return reused, nil
		}
		// Dead on arrival: discard and dial fresh.
		_ = reused.Close()
	}

	c, err := p.dial(ctx, t)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.known[c] = k
	p.mu.Unlock()
	return c, nil
}

// Put returns a client to the pool. When dead is true (or the client is
// unknown — e.g. a double Put) the connection is closed instead of recycled.
func (p *sshClientPool) Put(c *ssh.Client, dead bool) {
	if c == nil {
		return
	}
	p.mu.Lock()
	k, ok := p.known[c]
	if ok {
		delete(p.known, c)
	}
	if dead || !ok {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	list := p.idle[k]
	if len(list) >= p.maxIdle {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.idle[k] = append(list, c)
	p.mu.Unlock()
}

// sshClientAlive sends a global keepalive request and waits (bounded) for the
// reply. Servers that implement SSH keepalive (OpenSSH does) reply; a closed
// transport returns an error or never replies within the timeout.
func sshClientAlive(c *ssh.Client, timeout time.Duration) bool {
	done := make(chan bool, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		done <- err == nil
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(timeout):
		return false
	}
}

// isTransportDeath reports whether a session.Run error means the *connection*
// (not just the command) is unusable. A non-zero exit is an *ssh.ExitError /
// *ssh.ExitMissingError — the command ran, so the connection is reusable.
// Anything else (io.EOF, "connection closed", handshake drop) means the
// transport died and the client must be discarded.
func isTransportDeath(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	var missingErr *ssh.ExitMissingError
	if errors.As(err, &missingErr) {
		return false
	}
	return true
}

func sshAuthMethods(t TargetHost) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if t.Password != "" {
		methods = append(methods, ssh.Password(t.Password))
	}
	if t.KeyPath != "" {
		key, err := os.ReadFile(t.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", t.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", t.KeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if t.KeyBytes != nil {
		signer, err := ssh.ParsePrivateKey(t.KeyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse inline key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("ssh: no auth method (provide Password or KeyPath/KeyBytes)")
	}
	return methods, nil
}

// singleQuote wraps s in single quotes, escaping embedded single quotes as
// the POSIX-shell-safe sequence '\”.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildRemoteCommand assembles the single remote shell command string sent over
// SSH. Each arg is single-quoted individually (no word-splitting / injection),
// matching the local CommandStep's "exec.Command(args...) — never sh -c"
// invariant. When target.Sudo is set, the command is prefixed with `sudo -n`
// (non-interactive) so unprivileged SSH users can still manage system services.
func buildRemoteCommand(target TargetHost, executable string, args []string) string {
	remoteCmd := executable
	for _, a := range args {
		remoteCmd += " " + singleQuote(a)
	}
	if target.Sudo {
		remoteCmd = "sudo -n " + remoteCmd
	}
	return remoteCmd
}

// ---------------------------------------------------------------------------
// Remote Capability & Host Discovery (Phase 2.6)
//
// probeRemoteCapability dials (or reuses) an SSH connection to a target and
// runs a set of read-only discovery commands, returning a frozen, observed
// CapabilitySnapshot + HostSnapshot (ADR-009). The result is cached on the
// Context — NOT in a global singleton — keyed by TargetKey, so each operation
// (and each Batch fan-out child) owns an isolated cache and the Kernel stays
// free of shared mutable state. EnrichContextForTarget is the single place
// that performs discovery and attaches the result; the platform Resolver then
// just reads ctx.CapabilitySnapshot() without ever dialing SSH itself.
// ---------------------------------------------------------------------------

// probeRemoteCapability dials (or reuses) an SSH connection to the target and
// runs a set of read-only discovery commands. Each command is a single,
// argument-free probe (or a trivial pipeline) so it maps cleanly onto the
// existing "exec.Command(args...) — never sh -c" invariant of the SSH
// transport. A probe that returns non-zero (tool absent) yields an
// unavailable capability rather than an error, so a partially-probed host still
// produces a usable snapshot.
func probeRemoteCapability(ctx context.Context, t TargetHost) (*snapshot.CapabilitySnapshot, *snapshot.HostSnapshot, error) {
	client, err := defaultSSHClientPool.Get(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	var dead bool
	defer defaultSSHClientPool.Put(client, dead)

	run := func(cmd string) string {
		if ctx.Err() != nil {
			return ""
		}
		sess, err := client.NewSession()
		if err != nil {
			dead = true
			return ""
		}
		defer sess.Close()
		var out bytes.Buffer
		sess.Stdout = &out
		if err := sess.Run(cmd); err != nil {
			// Command not found / non-zero exit: treat as "not available".
			return ""
		}
		return strings.TrimSpace(out.String())
	}

	items := map[string]snapshot.CapabilityInfo{}
	// A capability snapshot is a COMPLETE matrix: every known capability is
	// present with its Availability flag, not just the available ones. This
	// matches the local capability.Snapshot contract and lets the Resolver /
	// UI diff capabilities across hosts and over time.
	addCap := func(name, verCmd string) {
		if run("command -v "+name) != "" {
			ver := ""
			if verCmd != "" {
				ver = run(verCmd)
			}
			items[name] = snapshot.CapabilityInfo{Name: name, Available: true, Version: ver}
		} else {
			items[name] = snapshot.CapabilityInfo{Name: name, Available: false}
		}
	}

	addCap("systemd", "systemctl --version")
	addCap("service", "")
	addCap("firewalld", "firewall-cmd --version")
	addCap("ufw", "")
	addCap("iptables", "iptables -V")
	addCap("docker", "docker version --format '{{.Server.Version}}'")
	addCap("journalctl", "")

	capSnap := &snapshot.CapabilitySnapshot{
		HostID:      t.Address,
		Version:     1,
		Items:       items,
		CollectedAt: time.Now(),
		Source:      snapshot.SourceSSH,
	}

	osArch := run("uname -srm") // e.g. "Linux 6.2.0 x86_64"
	kernel := run("uname -r")
	user := run("whoami")
	osrel := run("cat /etc/os-release 2>/dev/null")

	hostSnap := &snapshot.HostSnapshot{
		ID:          t.Address,
		Name:        t.Address,
		Address:     t.Address,
		Kernel:      kernel,
		User:        user,
		CollectedAt: time.Now(),
		Source:      snapshot.SourceSSH,
	}
	if osArch != "" {
		f := strings.Fields(osArch)
		if len(f) >= 1 {
			hostSnap.OS = strings.ToLower(f[0])
		}
		if len(f) >= 3 {
			hostSnap.Arch = f[len(f)-1]
		}
	}
	parseRemoteOSRelease(osrel, hostSnap)

	return capSnap, hostSnap, nil
}

// EnrichContextForTarget attaches a remote CapabilitySnapshot/HostSnapshot to a
// Context when the target is non-zero and not already enriched. It is
// best-effort: if the probe fails (host unreachable, no auth, timeout), the
// original context is returned unchanged and the Resolver falls back to its
// dominant-tool default. Discovery is bounded by a short timeout so an
// unreachable host degrades fast instead of hanging the request.
//
// This keeps Plan() pure (no network IO in the Handler): the control plane
// boundary enriches the context before dispatch, then the Resolver simply reads
// ctx.CapabilitySnapshot().
func EnrichContextForTarget(ctx Context, target TargetHost) Context {
	if target.IsZero() || ctx.CapabilitySnapshot() != nil {
		return ctx
	}
	pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	caps, host, err := probeRemoteCapability(pctx, target)
	if err != nil || caps == nil {
		return ctx
	}
	// Attach the observed snapshots to a child context scoped to the target.
	// WithTarget gives the child a FRESH, per-target snapshot cache (Round 5):
	// no shared global, so concurrent fan-out never cross-talks and a cached
	// value is never re-derived from the live host. SetSnapshot also refreshes
	// the fast decisioning surface (CapabilityContext) for the current target.
	child := WithTarget(ctx, target)
	child.SetSnapshot(target, caps)
	child.SetHostSnapshot(target, host)
	return child
}

// firstField returns the second whitespace-delimited token of s (most
// *-version tools print "<name> <version> ..."), falling back to the whole
// string when there is only one token. Empty input yields "".
func firstField(s string) string {
	if s == "" {
		return ""
	}
	f := strings.Fields(s)
	if len(f) >= 2 {
		return f[1]
	}
	if len(f) == 1 {
		return f[0]
	}
	return ""
}

// parseRemoteOSRelease fills Platform/Version from the ID=/VERSION_ID= lines
// of an /etc/os-release dump. Defensive: ignores malformed input.
func parseRemoteOSRelease(dump string, hs *snapshot.HostSnapshot) {
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
