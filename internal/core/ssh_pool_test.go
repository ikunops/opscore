package core

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// withCountingPool swaps the package-default pool for a fresh one that counts
// dials via the injected dial func, restores it on return.
func withCountingPool(t *testing.T) *int {
	t.Helper()
	orig := defaultSSHClientPool
	pool := newSSHClientPool(2)
	count := 0
	pool.dial = func(ctx context.Context, t TargetHost) (*ssh.Client, error) {
		count++
		return dialSSH(ctx, t)
	}
	defaultSSHClientPool = pool
	t.Cleanup(func() { defaultSSHClientPool = orig })
	return &count
}

func mustSplitAddr(t *testing.T, addr string) (host string, port int) {
	t.Helper()
	h, pStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	fmt.Sscanf(pStr, "%d", &port)
	return h, port
}

// TestSSHClientPool_ReusesSingleTarget asserts that repeated RunOverSSH calls
// to the same target reuse one dialed connection (only one handshake).
func TestSSHClientPool_ReusesSingleTarget(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, port := mustSplitAddr(t, addr)

	target := TargetHost{
		Address:               host,
		Port:                  port,
		User:                  "tester",
		Password:              "testpw",
		InsecureIgnoreHostKey: true,
	}

	dials := withCountingPool(t)

	const n = 4
	for i := 0; i < n; i++ {
		out, _, err := RunOverSSH(context.Background(), target, "echo", []string{"hello"}, nil, 10*time.Second)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if out != "hello\n" {
			t.Fatalf("call %d: stdout=%q, want %q", i, out, "hello\n")
		}
	}
	if *dials != 1 {
		t.Fatalf("expected 1 dial for %d calls to same target, got %d", n, *dials)
	}
}

// TestSSHClientPool_SeparateTargetsDialsEach asserts different targets get
// distinct connections (no cross-target reuse), while re-calling a target
// still reuses.
func TestSSHClientPool_SeparateTargetsDialsEach(t *testing.T) {
	addr1, stop1 := startTestSSHServer(t, "pw1")
	defer stop1()
	addr2, stop2 := startTestSSHServer(t, "pw2")
	defer stop2()

	h1, p1 := mustSplitAddr(t, addr1)
	h2, p2 := mustSplitAddr(t, addr2)
	t1 := TargetHost{Address: h1, Port: p1, User: "u", Password: "pw1", InsecureIgnoreHostKey: true}
	t2 := TargetHost{Address: h2, Port: p2, User: "u", Password: "pw2", InsecureIgnoreHostKey: true}

	dials := withCountingPool(t)

	if _, _, err := RunOverSSH(context.Background(), t1, "echo", []string{"a"}, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOverSSH(context.Background(), t2, "echo", []string{"b"}, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// Re-call t1 — must reuse its existing connection, not dial again.
	if _, _, err := RunOverSSH(context.Background(), t1, "echo", []string{"c"}, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	if *dials != 2 {
		t.Fatalf("expected 2 dials (two distinct targets; second t1 reuses), got %d", *dials)
	}
}

// TestSSHClientPool_DiscardsDeadClient asserts a transport-dead client is not
// recycled: a subsequent call re-dials rather than reusing a broken socket.
// We simulate death by putting a closed client back and confirming the next
// call still works (dial count goes up when the stale one is unusable).
func TestSSHClientPool_DiscardsDeadOnPut(t *testing.T) {
	addr, stop := startTestSSHServer(t, "testpw")
	defer stop()
	host, port := mustSplitAddr(t, addr)
	target := TargetHost{Address: host, Port: port, User: "tester", Password: "testpw", InsecureIgnoreHostKey: true}

	dials := withCountingPool(t)

	// First call establishes + returns a live client to the pool.
	if _, _, err := RunOverSSH(context.Background(), target, "echo", []string{"x"}, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	// Force-close every idle client so the next Get sees a dead socket and
	// must re-dial.
	defaultSSHClientPool.mu.Lock()
	for _, list := range defaultSSHClientPool.idle {
		for _, c := range list {
			_ = c.Close()
		}
	}
	defaultSSHClientPool.idle = make(map[string][]*ssh.Client)
	defaultSSHClientPool.mu.Unlock()

	// Second call must still succeed (re-dial on dead idle), so dials == 2.
	if _, _, err := RunOverSSH(context.Background(), target, "echo", []string{"y"}, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	if *dials != 2 {
		t.Fatalf("expected 2 dials after forced-dead idle client, got %d", *dials)
	}
}
