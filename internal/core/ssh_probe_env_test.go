package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServerWithEnv is startTestSSHServer with an explicit environment
// for the session commands the stub executes (`sh -c <cmd>`). nil means
// "inherit the test process environment" (identical to startTestSSHServer).
// The env-variant exists so a probe test can put a controlled fake bin dir on
// PATH without mutating the test process's global environment (which would
// leak into other tests in the package).
func startTestSSHServerWithEnv(t *testing.T, password string, env []string) (addr string, stop func()) {
	t.Helper()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					if newChan.ChannelType() != "session" {
						_ = newChan.Reject(ssh.UnknownChannelType, "unknown")
						continue
					}
					ch, reqs, err := newChan.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range reqs {
							switch req.Type {
							case "exec":
								// payload = uint32(len) + command
								cmd := string(req.Payload[4:])
								out, runErr := runStubCommand(env, cmd)
								code := byte(0)
								if runErr != nil {
									code = 1
								}
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, code})
								_ = req.Reply(true, nil)
								_, _ = ch.Write(out)
								_ = ch.Close()
							default:
								_ = req.Reply(true, nil)
							}
						}
					}()
				}
			}()
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close() }
}

// runStubCommand executes one stub session command with the given environment.
func runStubCommand(env []string, cmd string) ([]byte, error) {
	c := exec.Command("sh", "-c", cmd)
	if env != nil {
		c.Env = env
	}
	return c.CombinedOutput()
}

// TestProbeRemoteCapability_SystemdBySystemctlBinary pins the systemd
// detection command of the SSH capability probe (P1-5 residual, quality
// baseline 2026-10-11 addendum): the "systemd" capability is detected via the
// systemctl CLIENT binary (`command -v systemctl`) — symmetric with the local
// probe (capability.go LookPath("systemctl")) and the rich probe
// (capability/probe.go systemdProbe runs `systemctl --version`) — NOT via
// `command -v systemd`, which fails on standard systemd distros (the PID-1
// daemon binary lives under /usr(-/lib)/systemd and is not on PATH).
//
// Discriminating shape: a host that has systemctl on PATH but no `systemd`
// binary anywhere — the standard distro shape, emulated with a fake bin dir
// prepended to the stub session's PATH. Red before the fix (the probe asked
// `command -v systemd` ⇒ Available=false); green after.
func TestProbeRemoteCapability_SystemdBySystemctlBinary(t *testing.T) {
	fakebin := t.TempDir()
	// A bare file is enough: the probe only asks `command -v systemctl`; the
	// version follow-up (`systemctl --version`) may exit 0 with no output.
	if err := os.WriteFile(filepath.Join(fakebin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	env := append(os.Environ(), "PATH="+fakebin+string(os.PathListSeparator)+os.Getenv("PATH"))

	addr, stop := startTestSSHServerWithEnv(t, "testpw", env)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	caps, _, err := probeRemoteCapability(ctx, target)
	if err != nil {
		t.Fatalf("probeRemoteCapability: %v", err)
	}
	if caps == nil {
		t.Fatal("nil capability snapshot")
	}
	info, ok := caps.Get("systemd")
	if !ok {
		t.Fatalf("snapshot missing capability \"systemd\" (complete-matrix contract broken): %+v", caps)
	}
	if !info.Available {
		t.Fatalf("systemd must be detected via the systemctl client binary (`command -v systemctl`), got Available=false (item: %+v)", info)
	}
}
