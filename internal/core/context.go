package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// Context is the unified runtime context passed through all operations.
// Single interface, Builder pattern — no type hierarchy.
// Phase 0: User is empty (anonymous). Phase 1 adds real users.
type Context interface {
	context.Context // embed std context for cancellation/deadline

	User() UserContext
	Host() HostContext
	Target() TargetHost // empty => run locally; set => run over SSH
	Capability() CapabilityContext

	// --- Phase 2.6 observation surface (ADR-009) -------------------------
	// Snapshots are cached PER TARGET on the Context (not a global singleton),
	// keyed by TargetKey. This gives per-operation isolation (a Batch fan-out
	// gives each child its own cache) and keeps the Kernel free of shared
	// mutable state. See the architecture discussion (Round 10 / Round 5).

	// CapabilitySnapshot returns the observed capability snapshot for the
	// CURRENT target (nil if none has been discovered/enriched yet). The
	// platform Resolver consumes this for remote execution.
	CapabilitySnapshot() *snapshot.CapabilitySnapshot
	// HostSnapshot returns the observed host identity for the CURRENT target.
	HostSnapshot() *snapshot.HostSnapshot
	// Snapshot returns the cached capability snapshot for an arbitrary target.
	Snapshot(target TargetHost) (*snapshot.CapabilitySnapshot, bool)
	// SetSnapshot caches a capability snapshot for a target (idempotent).
	SetSnapshot(target TargetHost, snap *snapshot.CapabilitySnapshot)
	// HostSnapshotFor returns the cached host identity for an arbitrary target.
	HostSnapshotFor(target TargetHost) (*snapshot.HostSnapshot, bool)
	// SetHostSnapshot caches a host identity for a target.
	SetHostSnapshot(target TargetHost, h *snapshot.HostSnapshot)

	Trace() TraceContext
	Logger() *slog.Logger

	// ExecutionID returns the id of the Execution that is driving this
	// context (empty for ad-hoc / synchronous runs). The ExecutionService
	// stamps it via WithExecutionID so the kernel's AuditSink can attach
	// it to every AuditEvent without the Executor ever touching Audit
	// directly (Phase 2.1 / Round 3 decision).
	ExecutionID() string

	// WithExecutionID returns a child context carrying the given execution
	// id. Implementations preserve all other fields (user/host/target/
	// capability/trace/logger and the cancellation chain).
	WithExecutionID(id string) Context
}

// UserContext holds the identity of whoever initiated the operation.
// Phase 0: all fields empty (anonymous).
type UserContext struct {
	ID   string
	Name string
	Role string
}

// HostContext holds information about the target machine.
type HostContext struct {
	Hostname string
	OS       string
	Arch     string
}

// TraceContext holds tracing/timing info for the operation.
type TraceContext struct {
	TraceID   string
	StartTime time.Time
}

// targetState holds the discovered observation data for one target. Kept in a
// map keyed by TargetKey on each Context so caches are per-context (isolated
// across Batch fan-out) and never a shared global.
type targetState struct {
	cap  *snapshot.CapabilitySnapshot
	host *snapshot.HostSnapshot
}

// contextImpl is the single implementation of Context.
type contextImpl struct {
	context.Context
	user        UserContext
	host        HostContext
	target      TargetHost
	capability  CapabilityContext
	mu          sync.RWMutex
	states      map[TargetKey]*targetState
	trace       TraceContext
	logger      *slog.Logger
	executionID string
}

func (c *contextImpl) User() UserContext             { return c.user }
func (c *contextImpl) Host() HostContext             { return c.host }
func (c *contextImpl) Target() TargetHost            { return c.target }
func (c *contextImpl) Capability() CapabilityContext { return c.capability }
func (c *contextImpl) Trace() TraceContext           { return c.trace }
func (c *contextImpl) Logger() *slog.Logger          { return c.logger }
func (c *contextImpl) ExecutionID() string           { return c.executionID }

// WithExecutionID returns a child context carrying the given execution id
// (used by ExecutionService.Submit to correlate the run's AuditEvents
// with its ExecutionRecord). Other fields are preserved; the embedded
// cancellation chain is also preserved.
func (c *contextImpl) WithExecutionID(id string) Context {
	return &contextImpl{
		Context:     c.Context,
		user:        c.user,
		host:        c.host,
		target:      c.target,
		capability:  c.capability,
		states:      c.states,
		trace:       c.trace,
		logger:      c.logger,
		executionID: id,
	}
}

func (c *contextImpl) CapabilitySnapshot() *snapshot.CapabilitySnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if st, ok := c.states[c.target.Key()]; ok {
		return st.cap
	}
	return nil
}

func (c *contextImpl) HostSnapshot() *snapshot.HostSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if st, ok := c.states[c.target.Key()]; ok {
		return st.host
	}
	return nil
}

func (c *contextImpl) Snapshot(target TargetHost) (*snapshot.CapabilitySnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.states[target.Key()]
	if !ok || st == nil {
		return nil, false
	}
	return st.cap, st.cap != nil
}

func (c *contextImpl) SetSnapshot(target TargetHost, snap *snapshot.CapabilitySnapshot) {
	c.mu.Lock()
	if c.states == nil {
		c.states = map[TargetKey]*targetState{}
	}
	st := c.states[target.Key()]
	if st == nil {
		st = &targetState{}
		c.states[target.Key()] = st
	}
	st.cap = snap
	c.mu.Unlock()
	// If this is the current target, keep the fast decisioning surface in sync.
	if target.Key() == c.target.Key() {
		c.capability = NewCapabilityContext(snap)
	}
}

func (c *contextImpl) HostSnapshotFor(target TargetHost) (*snapshot.HostSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.states[target.Key()]
	if !ok || st == nil {
		return nil, false
	}
	return st.host, st.host != nil
}

func (c *contextImpl) SetHostSnapshot(target TargetHost, h *snapshot.HostSnapshot) {
	c.mu.Lock()
	if c.states == nil {
		c.states = map[TargetKey]*targetState{}
	}
	st := c.states[target.Key()]
	if st == nil {
		st = &targetState{}
		c.states[target.Key()] = st
	}
	st.host = h
	c.mu.Unlock()
}

// ContextBuilder builds Context instances.
// Usage:
//
//	ctx := NewContext().WithCapability(cap).WithLogger(logger).Build()
type ContextBuilder struct {
	ctx         context.Context
	user        UserContext
	host        HostContext
	target      TargetHost
	capability  CapabilityContext
	capSnap     *snapshot.CapabilitySnapshot
	hostSnap    *snapshot.HostSnapshot
	trace       TraceContext
	logger      *slog.Logger
	executionID string
	autoDetect  bool
}

// NewContext creates a Builder with sensible defaults.
func NewContext() *ContextBuilder {
	return &ContextBuilder{
		ctx:    context.Background(),
		logger: slog.Default(),
		trace: TraceContext{
			TraceID:   generateTraceID(),
			StartTime: time.Now(),
		},
		autoDetect: true,
	}
}

func (b *ContextBuilder) WithUser(u UserContext) *ContextBuilder  { b.user = u; return b }
func (b *ContextBuilder) WithHost(h HostContext) *ContextBuilder  { b.host = h; return b }
func (b *ContextBuilder) WithTarget(t TargetHost) *ContextBuilder { b.target = t; return b }
func (b *ContextBuilder) WithCapability(c CapabilityContext) *ContextBuilder {
	b.capability = c
	b.autoDetect = false
	return b
}

// WithCapabilitySnapshot attaches an observed CapabilitySnapshot (ADR-009) and
// derives the decisioning CapabilityContext from it (one-way Snapshot ->
// Context). Setting it disables auto-detection.
func (b *ContextBuilder) WithCapabilitySnapshot(s *snapshot.CapabilitySnapshot) *ContextBuilder {
	b.capSnap = s
	if s != nil {
		b.capability = NewCapabilityContext(s)
		b.autoDetect = false
	}
	return b
}

// WithHostSnapshot attaches an observed HostSnapshot (identity, not capability).
func (b *ContextBuilder) WithHostSnapshot(h *snapshot.HostSnapshot) *ContextBuilder {
	b.hostSnap = h
	return b
}

func (b *ContextBuilder) WithLogger(l *slog.Logger) *ContextBuilder { b.logger = l; return b }
func (b *ContextBuilder) WithTraceID(id string) *ContextBuilder     { b.trace.TraceID = id; return b }
func (b *ContextBuilder) WithExecutionID(id string) *ContextBuilder { b.executionID = id; return b }
func (b *ContextBuilder) WithStdContext(c context.Context) *ContextBuilder {
	b.ctx = c
	return b
}

// Build finalizes the Context. If Host/Capability were not set explicitly,
// they are auto-detected from the current machine; the corresponding
// CapabilitySnapshot/HostSnapshot are also attached (SourceLocal) for local
// execution out of the box, cached under the current target's key.
func (b *ContextBuilder) Build() Context {
	host := b.host
	if host.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			host.Hostname = h
		}
		host.OS = runtime.GOOS
		host.Arch = runtime.GOARCH
	}

	capSnap := b.capSnap
	cap := b.capability
	if b.autoDetect {
		if capSnap == nil {
			capSnap = detectLocalCapabilitySnapshot()
		}
		cap = NewCapabilityContext(capSnap)
	}

	hostSnap := b.hostSnap
	if hostSnap == nil {
		hostSnap = &snapshot.HostSnapshot{
			ID:          localHostID(),
			Name:        host.Hostname,
			Address:     host.Hostname,
			OS:          host.OS,
			Arch:        host.Arch,
			User:        localUser(),
			CollectedAt: time.Now(),
			Source:      snapshot.SourceLocal,
		}
	}

	states := map[TargetKey]*targetState{
		b.target.Key(): {cap: capSnap, host: hostSnap},
	}

	return &contextImpl{
		Context:     b.ctx,
		user:        b.user,
		host:        host,
		target:      b.target,
		capability:  cap,
		states:      states,
		trace:       b.trace,
		logger:      b.logger,
		executionID: b.executionID,
	}
}

func localUser() string {
	if os.Geteuid() == 0 {
		return "root"
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" { // Windows
		return u
	}
	return ""
}

func generateTraceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// WithCancel returns a child Context derived from parent that can be cancelled
// via the returned cancel function, while preserving all of parent's fields
// (user/host/target/capability/trace/logger and the embedded cancellation
// chain). Each derived context gets a FRESH, empty snapshot cache — never a
// shared one — so concurrent fan-out targets cannot cross-talk (Round 5).
func WithCancel(parent Context) (Context, context.CancelFunc) {
	cctx, cancel := context.WithCancel(parent)
	child := &contextImpl{
		Context:     cctx,
		user:        parent.User(),
		host:        parent.Host(),
		target:      parent.Target(),
		capability:  parent.Capability(),
		states:      map[TargetKey]*targetState{},
		trace:       parent.Trace(),
		logger:      parent.Logger(),
		executionID: parent.ExecutionID(),
	}
	return child, cancel
}

// WithTarget derives a child context from parent with a different TargetHost,
// preserving every other field (user/host/capability/trace/logger and the
// embedded cancellation chain). It is the primitive behind Batch fan-out
// (Phase 2.5): the same operation runs against many hosts, each with its own
// target but the caller's identity and tracing intact. The child gets a FRESH
// snapshot cache (Round 5) — it is enriched independently for its own target.
func WithTarget(parent Context, target TargetHost) Context {
	return &contextImpl{
		Context:     parent,
		user:        parent.User(),
		host:        parent.Host(),
		target:      target,
		capability:  parent.Capability(),
		states:      map[TargetKey]*targetState{},
		trace:       parent.Trace(),
		logger:      parent.Logger(),
		executionID: parent.ExecutionID(),
	}
}
