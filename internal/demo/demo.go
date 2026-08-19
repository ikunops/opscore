// Package demo provides an in-process SSH server that emulates a Linux host
// running systemd. It exists so the control plane's service UI can be exercised
// end-to-end on machines without a real systemd (e.g. a Windows dev box or this
// project's CI), with zero external dependencies.
//
// The fake host speaks just enough of `systemctl` for the builtin service
// operations: list-units, is-active, restart, start, stop. State is held in
// memory so a restart/start/stop is reflected by a later list-units — exactly
// like a real host would behave, just without touching anything.
package demo

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"strings"

	"github.com/YuDong999/opscore/internal/core"
	"golang.org/x/crypto/ssh"
)

type svc struct {
	load, active, sub, desc string
}

// order preserves a stable display order across requests.
var order = []string{
	"nginx.service", "sshd.service", "docker.service", "redis.service",
	"cron.service", "fail2ban.service", "mysql.service", "postgresql.service",
}

var table = map[string]*svc{
	"nginx.service":      {"loaded", "active", "running", "A high performance web server"},
	"sshd.service":       {"loaded", "active", "running", "OpenSSH server daemon"},
	"docker.service":     {"loaded", "active", "running", "Docker Application Container Engine"},
	"redis.service":      {"loaded", "active", "running", "Redis In-Memory Data Store"},
	"cron.service":       {"loaded", "active", "running", "Periodic Command Scheduler"},
	"fail2ban.service":   {"loaded", "active", "running", "Fail2Ban Service"},
	"mysql.service":      {"loaded", "inactive", "dead", "MySQL Community Server"},
	"postgresql.service": {"loaded", "inactive", "dead", "PostgreSQL RDBMS"},
}

// StartFakeHost launches the in-process SSH server on a random loopback port and
// returns a TargetHost pointing at it plus a stop function. password is the only
// accepted SSH password (user is "root"). The server is meant for local demos
// only and trusts the host key blindly.
func StartFakeHost(password string) (core.TargetHost, func(), error) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return core.TargetHost{}, nil, fmt.Errorf("demo: gen key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		return core.TargetHost{}, nil, fmt.Errorf("demo: signer: %w", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return core.TargetHost{}, nil, fmt.Errorf("demo: listen: %w", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
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
								// exec payload = uint32(len) + command
								cmd := string(req.Payload[4:])
								out, code := fakeSystemctl(parseQuotedArgs(cmd))
								_, _ = ch.SendRequest("exit-status", false, exitStatus(code))
								_ = req.Reply(true, nil)
								_, _ = ch.Write([]byte(out))
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

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := core.TargetHost{
		Address:               host,
		Port:                  port,
		User:                  "root",
		Password:              password,
		InsecureIgnoreHostKey: true,
	}
	stop := func() { _ = ln.Close() }
	return target, stop, nil
}

// parseQuotedArgs splits a shell command into arguments, honouring single
// quotes the way RunOverSSH emits them (each arg wrapped in '...').
func parseQuotedArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// fakeSystemctl emulates the subset of systemctl the builtin service ops use.
func fakeSystemctl(args []string) (string, int) {
	if len(args) < 2 || args[0] != "systemctl" {
		return "", 0
	}
	switch args[1] {
	case "list-units":
		var b strings.Builder
		for _, name := range order {
			s := table[name]
			fmt.Fprintf(&b, "%s %s %s %s %s\n", name, s.load, s.active, s.sub, s.desc)
		}
		return b.String(), 0
	case "is-active":
		if len(args) < 3 {
			return "unknown\n", 3
		}
		s, ok := table[args[2]]
		if !ok {
			return "unknown\n", 3
		}
		if s.active == "active" {
			return "active\n", 0
		}
		return "inactive\n", 3
	case "restart", "start":
		if len(args) >= 3 {
			if s, ok := table[args[2]]; ok {
				s.active = "active"
				s.sub = "running"
			}
		}
		return "", 0
	case "stop":
		if len(args) >= 3 {
			if s, ok := table[args[2]]; ok {
				s.active = "inactive"
				s.sub = "dead"
			}
		}
		return "", 0
	}
	return "", 0
}

// exitStatus encodes an int as the 4-byte big-endian payload SSH expects.
func exitStatus(code int) []byte {
	return []byte{byte(code >> 24), byte(code >> 16), byte(code >> 8), byte(code)}
}
