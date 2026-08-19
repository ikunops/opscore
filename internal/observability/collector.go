package observability

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
)

// Collector is the in-memory read-model store. It ingests Observations derived
// from frozen-subsystem events and answers read queries. It holds NO execution
// state and never calls back into Runtime Core (ADR-015 MUST-1/2/5).
//
// It is safe for concurrent use: the host wires its adapter sinks into the live
// event buses, so Observe* may race with Query.
//
// # The aggregate/sample split (Phase 18, ADR-040 §3.3)
//
// The observation buffer is BOUNDED (a FIFO ring of Capacity entries). Counters
// are bumped at ingest and are never re-derived from that buffer. The two read
// surfaces therefore make different promises, and a consumer must not confuse
// them:
//
//   - Counter / Counters — EXACT for all time, unaffected by eviction.
//   - Query / Count      — a bounded WINDOW over the most recent Capacity
//     observations.
//
// This is stated here rather than left implicit because an undocumented split
// is precisely the silent false-clean this phase exists to forbid: a caller
// that reads a windowed sample as the whole record will conclude "it never
// happened" from "I no longer hold it". Complete() is the one-bit answer to
// "may I read an absence in Query as evidence?".
type Collector struct {
	mu sync.RWMutex
	// obs is a ring buffer. It grows by append until it reaches capacity, after
	// which record overwrites obs[head]. len(obs) <= capacity, always.
	obs []Observation
	// head indexes the OLDEST retained observation. It is meaningful only once
	// the ring is full; while len(obs) < capacity it stays 0 and the buffer is
	// a plain slice in ingest order.
	head int
	// capacity is the configured ceiling. Always > 0 (see NewCollectorWithCapacity).
	capacity int
	// dropped counts observations evicted since start. Loss is COUNTED, never
	// hidden — an unbounded buffer that silently became bounded would make
	// every other guarantee here optional.
	dropped int64
	// total counts observations ingested since start, BEFORE any eviction. It
	// is the EXACT lifetime figure the metrics surface must render (R19-7): a
	// window that has lapped must still report how many observations ever
	// arrived, never a silent zero that an operator reads as "nothing happened".
	total int64

	// counters are derived, label-keyed aggregate counts. Key format:
	// "name|labelA=va|labelB=vb" with labels sorted alphabetically.
	counters map[string]int64
}

// DefaultCollectorCapacity bounds the retained observation window. It is a
// memory ceiling, not a retention policy: durable evidence lives in the audit
// store, and the collector is explicitly in-memory by design (ADR-015).
const DefaultCollectorCapacity = 10000

// NewCollector builds an empty collector with the default capacity. The default
// constructor is BOUNDED on purpose — an unbounded default would leave the
// completeness contract as an opt-in, and the one collector that mattered would
// be the one nobody configured.
func NewCollector() *Collector {
	return NewCollectorWithCapacity(DefaultCollectorCapacity)
}

// NewCollectorWithCapacity builds an empty collector retaining at most n
// observations. A non-positive n falls back to DefaultCollectorCapacity: a
// misconfiguration must not produce a zero-capacity collector that drops
// everything and then reports an empty, healthy-looking store.
func NewCollectorWithCapacity(n int) *Collector {
	if n <= 0 {
		n = DefaultCollectorCapacity
	}
	// obs is grown lazily rather than pre-allocated: a 10k-entry Observation
	// array would be paid for by every process, including the ones that never
	// enable observability.
	return &Collector{capacity: n, counters: make(map[string]int64)}
}

// Capacity returns the configured retention ceiling for Query/Count.
func (c *Collector) Capacity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capacity
}

// DroppedCount returns how many observations have been evicted since start.
func (c *Collector) DroppedCount() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dropped
}

// Complete reports whether the retained window is the ENTIRE ingest history.
// When it is false, an absence in Query means "not in the window", never "did
// not happen" — the aggregate counters remain exact either way.
func (c *Collector) Complete() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dropped == 0
}

// --- canonical ingestion (invoked by the adapter sinks) ---

// ObserveExecution ingests an execution.ExecutionEvent (lifecycle trace).
func (c *Collector) ObserveExecution(ev execution.ExecutionEvent) {
	c.record(Observation{
		Timestamp:   ev.Timestamp,
		Source:      SourceExecution,
		Kind:        KindTrace,
		ExecutionID: ev.ID,
		RequestID:   ev.ID,
		Status:      string(ev.Status),
		Code:        string(ev.Type),
		Operation:   string(ev.Type),
	})
}

// ObserveSandbox ingests a sandbox.Decision (Phase 6.1 envelope verdict).
func (c *Collector) ObserveSandbox(d sandbox.Decision) {
	verdict := "deny"
	if d.Allowed {
		verdict = "allow"
	}
	c.record(Observation{
		Timestamp: time.Now(),
		Source:    SourceSandbox,
		Kind:      KindMetric,
		PluginID:  pluginIDFromSource(d.Source),
		Operation: d.Operation,
		Code:      string(d.Code),
		Verdict:   verdict,
		Reason:    d.Reason,
	})
}

// ObserveSignature ingests a manifest.SignatureResult (Phase 5 trust verdict).
func (c *Collector) ObserveSignature(r manifest.SignatureResult) {
	verdict := "unverified"
	if r.Verified {
		verdict = "verified"
	}
	c.record(Observation{
		Timestamp: time.Now(),
		Source:    SourceSignature,
		Kind:      KindMetric,
		PluginID:  r.SignerID,
		Code:      r.Code,
		Verdict:   verdict,
		Reason:    r.PolicyDecision,
	})
}

// ObserveAudit ingests a core.AuditEvent (compliance correlation view).
func (c *Collector) ObserveAudit(a core.AuditEvent) {
	c.record(Observation{
		Timestamp:   a.Timestamp,
		Source:      SourceAudit,
		Kind:        KindAuditCorrelation,
		TraceID:     a.TraceID,
		ExecutionID: a.ExecutionID,
		Operation:   a.OperationName,
		Status:      statusFromResult(a.Result.Success),
		Code:        a.Permission.ResourceType + "." + a.Permission.Action,
		Risk:        a.Risk.String(),
		DurationMs:  a.Duration.Milliseconds(),
	})
}

// record is the single ingestion point. It stamps an opaque ObsID, stores the
// observation in the bounded ring and bumps derived counters. It is the ONLY
// writer of c.obs / c.counters / c.head / c.dropped.
//
// Note the order of the two effects: the counter bump is unconditional and
// happens whether or not the sample was retained. That is the aggregate/sample
// split in three lines of code — eviction costs you the sample, never the
// count.
func (c *Collector) record(o Observation) {
	o.SchemaVersion = SchemaVersion
	o.ObsID = newObsID()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
	if len(c.obs) < c.capacity {
		c.obs = append(c.obs, o)
	} else {
		// Full: overwrite the oldest slot (FIFO) and advance the head.
		c.obs[c.head] = o
		c.head = (c.head + 1) % c.capacity
		c.dropped++
	}
	c.bumpCounters(o)
}

// Observations returns a snapshot of the retained observations in INGEST order.
// It is a read-only copy used by the tracing adapter (to verify TraceID
// population) and by tests; it never mutates the store.
func (c *Collector) Observations() []Observation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.obs) == 0 {
		return nil
	}
	out := make([]Observation, 0, len(c.obs))
	for i := 0; i < len(c.obs); i++ {
		out = append(out, c.at(i))
	}
	return out
}

// at returns the i-th retained observation in INGEST order (0 = oldest).
// Callers must hold at least the read lock. It exists so the ring's internal
// wrap never leaks into a reader: a wrap-order bug is invisible until the
// buffer laps, which is exactly when it starts mattering.
func (c *Collector) at(i int) Observation {
	return c.obs[(c.head+i)%len(c.obs)]
}

// bumpCounters derives label-keyed aggregate counts from one observation.
func (c *Collector) bumpCounters(o Observation) {
	c.counters[counterKey("observations_total", map[string]string{"source": string(o.Source)})]++
	if o.Verdict != "" {
		c.counters[counterKey("verdict_total", map[string]string{
			"source": string(o.Source), "verdict": o.Verdict,
		})]++
	}
	if o.Source == SourceExecution && o.Status != "" {
		c.counters[counterKey("execution_status_total", map[string]string{"status": o.Status})]++
	}
}

// --- helpers ---

// pluginIDFromSource extracts the plugin name from a "plugin:<name>" source
// string. It never invents an ID — an unrecognized form is returned unchanged
// so correlation stays honest.
func pluginIDFromSource(src string) string {
	const prefix = "plugin:"
	if strings.HasPrefix(src, prefix) {
		return strings.TrimPrefix(src, prefix)
	}
	return src
}

// statusFromResult maps an execution result to a stable status string.
func statusFromResult(success bool) string {
	if success {
		return "success"
	}
	return "failed"
}

// newObsID returns an opaque, observability-local handle. It is a random token
// used only to key the in-memory store; it is NOT a new identity system.
func newObsID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "obs-" + time.Now().Format("20060102150405.000000000")
	}
	return "obs-" + hex.EncodeToString(b)
}

// counterKey builds a stable, sortable counter key from a name + labels.
func counterKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb := strings.Builder{}
	sb.WriteString(name)
	for _, k := range keys {
		sb.WriteString("|")
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(labels[k])
	}
	return sb.String()
}
