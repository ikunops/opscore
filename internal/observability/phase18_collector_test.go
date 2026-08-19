package observability

import (
	"fmt"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/sandbox"
)

// ---------------------------------------------------------------------------
// Phase 18 — bounded collector + completeness accounting (ADR-040 §3.3)
//
// ADR-039 §2 F-4: the collector appended without bound. Unbounded memory is a
// liveness problem, but the integrity problem is subtler — once a bound is
// introduced, a consumer that cannot tell "I have everything" from "I have the
// tail" will read a windowed sample as if it were the whole record. The
// contract therefore ships WITH the bound, never after it.
// ---------------------------------------------------------------------------

func ingestSandbox(c *Collector, n int) {
	for i := 0; i < n; i++ {
		c.ObserveSandbox(sandbox.Decision{
			Allowed:   true,
			Source:    "plugin:p",
			Operation: fmt.Sprintf("op-%d", i),
		})
	}
}

// TestCollectorDefaultCapacity: the default constructor is bounded. An
// unbounded default would make every other guarantee here optional.
func TestCollectorDefaultCapacity(t *testing.T) {
	c := NewCollector()
	if c.Capacity() != DefaultCollectorCapacity {
		t.Errorf("Capacity() = %d, want %d", c.Capacity(), DefaultCollectorCapacity)
	}
	if !c.Complete() {
		t.Error("a fresh collector must be Complete()")
	}
	if c.DroppedCount() != 0 {
		t.Errorf("DroppedCount() = %d, want 0", c.DroppedCount())
	}
}

// TestCollectorEvictsFIFOAndCountsDropped: at capacity the OLDEST observation
// is evicted, the retained window stays in ingest order, and the loss is
// counted rather than hidden.
func TestCollectorEvictsFIFOAndCountsDropped(t *testing.T) {
	c := NewCollectorWithCapacity(3)
	ingestSandbox(c, 5) // op-0 .. op-4

	if c.Count() != 3 {
		t.Fatalf("Count() = %d, want 3 (the capacity)", c.Count())
	}
	if c.DroppedCount() != 2 {
		t.Errorf("DroppedCount() = %d, want 2", c.DroppedCount())
	}

	got := c.Query(Query{Source: SourceSandbox})
	if len(got) != 3 {
		t.Fatalf("Query returned %d, want 3", len(got))
	}
	want := []string{"op-2", "op-3", "op-4"}
	for i, w := range want {
		if got[i].Operation != w {
			t.Errorf("retained[%d] = %q, want %q — eviction must be FIFO and the window ingest-ordered",
				i, got[i].Operation, w)
		}
	}
}

// TestCollectorCountersExactAfterEviction is the aggregate/sample split, and
// the reason counters are bumped at ingest instead of derived from the buffer:
// a counter recomputed from an evicting window would silently under-report
// forever. Counters are exact for all time; Query/Count are a window.
func TestCollectorCountersExactAfterEviction(t *testing.T) {
	c := NewCollectorWithCapacity(3)
	ingestSandbox(c, 10)

	if got := c.Counter("observations_total", map[string]string{"source": string(SourceSandbox)}); got != 10 {
		t.Errorf("observations_total = %d, want 10 — counters must be EXACT despite eviction", got)
	}
	if got := c.Counter("verdict_total", map[string]string{
		"source": string(SourceSandbox), "verdict": "allow",
	}); got != 10 {
		t.Errorf("verdict_total{allow} = %d, want 10", got)
	}
	if c.Count() != 3 {
		t.Errorf("Count() = %d, want 3 — Count is the retained SAMPLE, not the aggregate", c.Count())
	}
}

// TestCollectorCompleteReflectsDrops: Complete() is the one-bit answer to
// "may I read an absence as evidence?".
func TestCollectorCompleteReflectsDrops(t *testing.T) {
	c := NewCollectorWithCapacity(2)
	ingestSandbox(c, 2)
	if !c.Complete() {
		t.Fatal("Complete() = false at exactly capacity with no eviction, want true")
	}
	ingestSandbox(c, 1)
	if c.Complete() {
		t.Error("Complete() = true after an eviction — a windowed sample must not claim completeness")
	}
	if c.DroppedCount() != 1 {
		t.Errorf("DroppedCount() = %d, want 1", c.DroppedCount())
	}
}

// TestCollectorNonPositiveCapacityFallsBackToDefault: a misconfiguration must
// not silently produce a zero-capacity collector that drops everything and
// reports an empty, healthy-looking store.
func TestCollectorNonPositiveCapacityFallsBackToDefault(t *testing.T) {
	for _, n := range []int{0, -1} {
		c := NewCollectorWithCapacity(n)
		if c.Capacity() != DefaultCollectorCapacity {
			t.Errorf("NewCollectorWithCapacity(%d).Capacity() = %d, want the default %d",
				n, c.Capacity(), DefaultCollectorCapacity)
		}
	}
}

// TestCollectorQueryOrderAcrossWrap: the ring wraps internally, but Query must
// keep presenting ingest order. A wrap-order bug is invisible until the buffer
// laps, which is exactly when it matters.
func TestCollectorQueryOrderAcrossWrap(t *testing.T) {
	c := NewCollectorWithCapacity(4)
	ingestSandbox(c, 11) // laps the ring twice, ends mid-buffer

	got := c.Query(Query{Source: SourceSandbox})
	if len(got) != 4 {
		t.Fatalf("Query returned %d, want 4", len(got))
	}
	want := []string{"op-7", "op-8", "op-9", "op-10"}
	for i, w := range want {
		if got[i].Operation != w {
			t.Errorf("window[%d] = %q, want %q", i, got[i].Operation, w)
		}
	}
}
