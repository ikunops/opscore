package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer spins up a minimal in-process SSH server on a random
// loopback port, accepting a single fixed password. It is used to exercise
// RunOverSSH end-to-end without an external host.
func startTestSSHServer(t *testing.T, password string) (addr string, stop func()) {
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
								out, runErr := exec.Command("sh", "-c", cmd).CombinedOutput()
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

func TestRunOverSSH_Success(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	target := TargetHost{
		Address:               host,
		Port:                  port,
		User:                  "tester",
		Password:              "testpw",
		InsecureIgnoreHostKey: true,
	}

	stdout, stderr, err := RunOverSSH(context.Background(), target, "echo", []string{"hello", "world"}, nil, 10*time.Second)
	if err != nil {
		t.Fatalf("RunOverSSH err = %v (stderr=%q)", err, stderr)
	}
	if stdout != "hello world\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello world\n")
	}
}

func TestRunOverSSH_ArgsAreQuoted(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := TargetHost{Address: host, Port: port, User: "tester", Password: "testpw", InsecureIgnoreHostKey: true}
	// An argument containing spaces and a semicolon must stay a single argument.
	stdout, _, err := RunOverSSH(context.Background(), target, "printf", []string{"[%s]", "a b; rm -rf /"}, nil, 10*time.Second)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if stdout != "[a b; rm -rf /]" {
		t.Fatalf("quoting broken, stdout = %q", stdout)
	}
}

func TestRunOverSSH_NonZeroExit(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := TargetHost{Address: host, Port: port, User: "tester", Password: "testpw", InsecureIgnoreHostKey: true}
	_, _, err := RunOverSSH(context.Background(), target, "false", nil, nil, 10*time.Second)
	if err == nil {
		t.Fatal("expected non-zero exit error")
	}
}

func TestRunOverSSH_BadPassword(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := TargetHost{Address: host, Port: port, User: "tester", Password: "wrong", InsecureIgnoreHostKey: true}
	_, _, err := RunOverSSH(context.Background(), target, "echo", []string{"x"}, nil, 10*time.Second)
	if err == nil {
		t.Fatal("expected auth failure")
	}
}
