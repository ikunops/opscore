package runtime

// Soak test for the Watcher's poll ∥ reload interleaving under the race
// detector (ADR-079's FileLoader fix regression shape).
//
// The data race ADR-079 fixed — FileLoader.lastErrors written by BOTH the
// poller goroutine and the debounced reload goroutine — was caught by the
// ordinary watcher tests, but only because they happened to drive both paths.
// This test makes the interleaving SUSTAINED: rapid manifest rewrites for
// SEVERAL plugins (each rewrite is a poll-discovered change that triggers a
// debounced reload) while the poller keeps running, long enough for -race to
// see any unsynchronized access that only shows in specific interleavings. It
// also pins the end state: every plugin is still loaded under its own id, and
// the manager counted at least one reload per plugin.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

func TestADR079WatcherPollReloadSoakRaceFree(t *testing.T) {
	dir := t.TempDir()
	const plugins = 4
	const rewrites = 8

	// Seed every plugin BEFORE the watcher starts: the baseline poll records
	// their content hashes without triggering reloads.
	for p := 0; p < plugins; p++ {
		writeManifest(t, dir, soakName(p), nOpManifest(soakName(p), 1))
	}
	mgr, _, _ := newTestManager()
	base := NewFileLoader(manifest.NewFileProvider(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for p := 0; p < plugins; p++ {
		loadEnable(t, mgr, base, soakID(p))
	}

	w := NewWatcher(mgr, base, []string{"linux"}, WatcherOptions{
		Interval: 5 * time.Millisecond,
		Debounce: 10 * time.Millisecond,
	})
	w.Start(ctx)

	// Concurrent rewriters: one goroutine per plugin, each rewriting its
	// manifest `rewrites` times with a DIFFERENT operation count (the content
	// hash must change so the poller discovers it). Up to `plugins` reloads in
	// flight at once, all sharing the ONE FileLoader — the exact shape that
	// raced before the fix.
	var wg sync.WaitGroup
	for p := 0; p < plugins; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			name := soakName(p)
			for gen := 0; gen < rewrites; gen++ {
				writeManifest(t, dir, name, nOpManifest(name, 2+((p+gen)%5)))
				time.Sleep(3 * time.Millisecond)
			}
		}(p)
	}
	wg.Wait()

	// Let the poller/debounce catch the last rewrites, then stop and settle.
	time.Sleep(300 * time.Millisecond)
	w.Stop()

	// End-state invariants: every plugin is still loaded under its own id (a
	// failed/lost reload must never drop a plugin) and the manager counted at
	// least one reload per plugin (the watcher saw and applied the changes).
	for p := 0; p < plugins; p++ {
		id := soakID(p)
		if _, ok := mgr.Get(id); !ok {
			t.Fatalf("ADR-079 soak: plugin %q vanished — a soak must never drop a loaded plugin", id)
		}
		if n := mgr.ReloadCount(id); n < 1 {
			t.Fatalf("ADR-079 soak: plugin %q was never reloaded (reload count %d) — the poller missed the rewrites", id, n)
		}
	}
}

func soakName(p int) string { return "soak" + itoa(p) }

func soakID(p int) string { return soakName(p) + "@1.0.0" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
