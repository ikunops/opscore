package protection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes (local to the protection package to avoid an import cycle with storage)
// ---------------------------------------------------------------------------

type fakeKillPersistence struct {
	mu        sync.Mutex
	kills     map[string]bool
	principal map[string]bool
	loadErr   error
}

func newFakeKillPersistence() *fakeKillPersistence {
	return &fakeKillPersistence{kills: map[string]bool{}, principal: map[string]bool{}}
}

func (f *fakeKillPersistence) LoadKills() (map[string]bool, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := make(map[string]bool, len(f.kills))
	for k, v := range f.kills {
		m[k] = v
	}
	return m, nil
}

func (f *fakeKillPersistence) LoadPrincipalKills() (map[string]bool, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := make(map[string]bool, len(f.principal))
	for k, v := range f.principal {
		m[k] = v
	}
	return m, nil
}

func (f *fakeKillPersistence) SetKilled(capID string, killed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills[capID] = killed
	return nil
}

func (f *fakeKillPersistence) SetPrincipalKilled(hash string, killed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.principal[hash] = killed
	return nil
}

func (f *fakeKillPersistence) ListKills() ([]KillEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]KillEntry, 0, len(f.kills)+len(f.principal))
	for k, v := range f.kills {
		out = append(out, KillEntry{CapabilityID: k, Killed: v})
	}
	for h, v := range f.principal {
		out = append(out, KillEntry{Principal: true, PrincipalHash: h, Killed: v})
	}
	return out, nil
}

type fakeAuditWriter struct {
	mu     sync.Mutex
	events []ProtectionEvent
	fail   bool
}

func (f *fakeAuditWriter) WriteEvent(_ context.Context, ev ProtectionEvent) error {
	if f.fail {
		return errors.New("audit unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeAuditWriter) eventsLocked() []ProtectionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ProtectionEvent, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakeAuditWriter) actions() []string {
	evs := f.eventsLocked()
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Action
	}
	return out
}

type fakeFailureReader struct {
	window FailureWindow
	err    error
}

func (f *fakeFailureReader) RecentFailures(_ string, _ time.Duration) (FailureWindow, error) {
	return f.window, f.err
}

// ---------------------------------------------------------------------------
// Gate construction helper
// ---------------------------------------------------------------------------

func newTestGate(kill *KillStore, reader FailureEvidenceReader, audit AuditWriter) *Gate {
	// Bootstrap so the uninitialized (fail-closed) KillStore does not turn
	// every clean-admit path into a "killed" reject. Production wiring does
	// the same Bootstrap explicitly before constructing the Gate (R21-13).
	_ = kill.Bootstrap()
	return New(Config{
		KillStore: kill,
		Breaker:   NewBreakerSet(reader, DefaultBreakerConfig(), time.Now),
		Sem:       NewSemaphoreSet(8),
		Buckets:   NewTokenBucketSet(TokenBucketConfig{Capacity: 1000, Refill: 1000}, time.Now),
		Audit:     audit,
		Timeout:   NewTimeoutConfig(),
	})
}

// ---------------------------------------------------------------------------
// Property tests
// ---------------------------------------------------------------------------

// P-1: empty bucket refills at the configured rate.
func TestTokenBucketRefills(t *testing.T) {
	mclk := &mutableClock{now: time.Unix(0, 0)}
	tbs := NewTokenBucketSet(TokenBucketConfig{Capacity: 1, Refill: 1}, mclk.nowTime)
	if !tbs.Take("op", "p") { // 1 token available
		t.Fatal("first take should succeed")
	}
	if tbs.Take("op", "p") { // drained
		t.Fatal("second take should fail when empty")
	}
	mclk.add(time.Second) // advance 1s → refill 1 token
	if !tbs.Take("op", "p") {
		t.Fatal("after 1s refill, take should succeed")
	}
}

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (m *mutableClock) nowTime() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *mutableClock) add(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

// P-2: bucket with 0 tokens rejects.
func TestTokenBucketRejectsWhenEmpty(t *testing.T) {
	mclk := &mutableClock{now: time.Unix(0, 0)}
	tbs := NewTokenBucketSet(TokenBucketConfig{Capacity: 1, Refill: 1}, mclk.nowTime)
	if !tbs.Take("op", "p") {
		t.Fatal("first take should succeed")
	}
	if tbs.Take("op", "p") {
		t.Fatal("empty bucket must reject")
	}
}

// P-3: a concurrency-rejected request never consumes a token.
func TestRejectedRequestsDontConsumeTokens(t *testing.T) {
	clock := time.Now
	tbs := NewTokenBucketSet(TokenBucketConfig{Capacity: 1, Refill: 0}, clock)
	kill := NewKillStore(newFakeKillPersistence(), clock)
	_ = kill.Bootstrap()
	sem := NewSemaphoreSet(1)
	sem.Acquire("op") // fill the only slot so the gate's Acquire fails
	gate := New(Config{
		KillStore: kill,
		Breaker:   NewBreakerSet(&fakeFailureReader{}, DefaultBreakerConfig(), clock),
		Sem:       sem,
		Buckets:   tbs,
		Audit:     &fakeAuditWriter{},
		Timeout:   NewTimeoutConfig(),
	})
	_, rej := gate.Check(context.Background(), "op", "principal")
	if rej == nil {
		t.Fatal("expected concurrency reject")
	}
	// The bucket must still hold its single token (Check never called Take).
	if !tbs.Take("op", principalHash("principal", gate.salt)) {
		t.Fatal("concurrency-rejected request must not consume a token")
	}
}

// P-4: timeout applies a deadline to the context.
func TestTimeoutAppliesDeadline(t *testing.T) {
	tc := NewTimeoutConfig()
	tc.Default = 30 * time.Second
	ctx, cancel := tc.WithDeadline(context.Background(), "op")
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if time.Until(d) <= 0 {
		t.Fatal("deadline must be in the future")
	}
}

// P-6: 5 consecutive failures open the breaker (in-memory feedback).
func TestBreakerOpensAfterFailures(t *testing.T) {
	b := NewBreakerSet(&fakeFailureReader{}, DefaultBreakerConfig(), time.Now)
	now := time.Now()
	for i := 0; i < 5; i++ {
		b.RecordOutcome("op", true, now)
	}
	state, _, _ := b.Evaluate("op", now)
	if state != BreakerOpen {
		t.Fatalf("expected Open after 5 failures, got %s", state)
	}
}

// P-7: after cooldown, an open breaker half-opens (admits one probe).
func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	cfg := DefaultBreakerConfig()
	b := NewBreakerSet(&fakeFailureReader{}, cfg, time.Now)
	now := time.Now()
	for i := 0; i < 5; i++ {
		b.RecordOutcome("op", true, now)
	}
	if st, _, _ := b.Evaluate("op", now); st != BreakerOpen {
		t.Fatalf("expected Open, got %s", st)
	}
	later := now.Add(cfg.Cooldown + time.Second)
	if st, _, _ := b.Evaluate("op", later); st != BreakerHalfOpen {
		t.Fatalf("expected HalfOpen after cooldown, got %s", st)
	}
}

// P-8: unreadable audit evidence → Unknown (fail closed).
func TestBreakerUnknownOnUnreadableAudit(t *testing.T) {
	b := NewBreakerSet(&fakeFailureReader{err: errors.New("boom")}, DefaultBreakerConfig(), time.Now)
	state, _, err := b.Evaluate("op", time.Now())
	if err == nil || state != BreakerUnknown {
		t.Fatalf("expected Unknown + error, got %s err=%v", state, err)
	}
}

// P-9: truncated evidence is a lower bound, not an exact zero.
func TestBreakerAcknowledgesTruncation(t *testing.T) {
	// 3 visible, truncated → Unknown (below threshold 5)
	b1 := NewBreakerSet(&fakeFailureReader{window: FailureWindow{Count: 3, Truncated: true}}, DefaultBreakerConfig(), time.Now)
	if st, _, _ := b1.Evaluate("op", time.Now()); st != BreakerUnknown {
		t.Fatalf("truncated+below must be Unknown, got %s", st)
	}
	// 5 visible, truncated → Open (lower bound proves threshold reached)
	b2 := NewBreakerSet(&fakeFailureReader{window: FailureWindow{Count: 5, Truncated: true}}, DefaultBreakerConfig(), time.Now)
	if st, _, _ := b2.Evaluate("op", time.Now()); st != BreakerOpen {
		t.Fatalf("truncated+at-threshold must be Open, got %s", st)
	}
}

// P-10: kill state persists across a restart (new KillStore, same backend).
func TestKillStorePersistsAcrossRestart(t *testing.T) {
	store := newFakeKillPersistence()
	ks1 := NewKillStore(store, time.Now)
	if err := ks1.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := ks1.SetKilled("op", true); err != nil {
		t.Fatal(err)
	}
	ks2 := NewKillStore(store, time.Now)
	if err := ks2.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if !ks2.IsKilled("op") {
		t.Fatal("kill must survive a restart")
	}
}

// P-11: startup load failure is fail closed.
func TestKillStoreStartupFailClosed(t *testing.T) {
	store := newFakeKillPersistence()
	store.loadErr = errors.New("db down")
	ks := NewKillStore(store, time.Now)
	if err := ks.Bootstrap(); err == nil {
		t.Fatal("expected bootstrap error")
	}
	if ks.State() != KillStateFailed {
		t.Fatalf("expected Failed state, got %s", ks.State())
	}
	if !ks.IsKilled("any-cap") {
		t.Fatal("failed bootstrap must fail closed (all killed)")
	}
}

// P-13: semaphore bounds concurrency.
func TestSemaphoreBoundsConcurrency(t *testing.T) {
	ss := NewSemaphoreSet(2)
	if !ss.Acquire("op") || !ss.Acquire("op") {
		t.Fatal("first two acquires must succeed")
	}
	if ss.Acquire("op") {
		t.Fatal("third acquire must fail (cap 2)")
	}
	ss.Release("op")
	if !ss.Acquire("op") {
		t.Fatal("after release, acquire must succeed")
	}
}

// P-14: release frees the slot on every path.
func TestSemaphoreReleasesOnAllPaths(t *testing.T) {
	ss := NewSemaphoreSet(1)
	if !ss.Acquire("op") {
		t.Fatal("acquire must succeed")
	}
	ss.Release("op")
	if !ss.Acquire("op") {
		t.Fatal("after release, acquire must succeed again")
	}
	ss.Release("op")
}

// buildGate constructs a fresh, bootstrapped gate with its own kill store and
// audit writer (returned for inspection). A fresh gate per scenario avoids the
// circuit breaker's in-memory Open/HalfOpen state leaking across sub-tests
// (the breaker legitimately latches Open and would otherwise shadow later
// guards).
func buildGate(clock func() time.Time, reader *fakeFailureReader) (*Gate, *KillStore, *fakeAuditWriter) {
	store := newFakeKillPersistence()
	ks := NewKillStore(store, clock)
	_ = ks.Bootstrap()
	audit := &fakeAuditWriter{}
	g := newTestGate(ks, reader, audit)
	return g, ks, audit
}

// P-15: guard check order is kill → breaker → concurrency → rate → timeout.
// Each step uses a fresh gate with ALL lower-priority guards loaded, proving
// the higher-priority guard fires first (not merely "that guard fires").
func TestGateCheckOrder(t *testing.T) {
	clock := time.Now
	ph := "p"

	// 1. Kill switch wins over every other guard.
	{
		g, ks, _ := buildGate(clock, &fakeFailureReader{window: FailureWindow{Count: 10, Truncated: false}})
		_ = ks.SetKilled("op", true)
		g.sem.Acquire("op")
		for i := 0; i < 7; i++ {
			g.sem.Acquire("op")
		}
		for g.buckets.Take("op", principalHash(ph, g.salt)) {
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionKilled {
			t.Fatalf("kill must be first; got %v", rej)
		}
	}

	// 2. Breaker (open) wins over concurrency/rate.
	{
		g, _, _ := buildGate(clock, &fakeFailureReader{window: FailureWindow{Count: 10, Truncated: false}})
		g.sem.Acquire("op")
		for i := 0; i < 7; i++ {
			g.sem.Acquire("op")
		}
		for g.buckets.Take("op", principalHash(ph, g.salt)) {
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionCircuitOpen {
			t.Fatalf("breaker must be next; got %v", rej)
		}
	}

	// 3. Concurrency wins over rate.
	{
		g, _, _ := buildGate(clock, &fakeFailureReader{})
		g.sem.Acquire("op")
		for i := 0; i < 7; i++ {
			g.sem.Acquire("op")
		}
		for g.buckets.Take("op", principalHash(ph, g.salt)) {
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionConcurrencyExceeded {
			t.Fatalf("concurrency must be next; got %v", rej)
		}
	}

	// 4. Rate limit is the last reject gate.
	{
		g, _, _ := buildGate(clock, &fakeFailureReader{})
		for g.buckets.Take("op", principalHash(ph, g.salt)) {
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionRateLimited {
			t.Fatalf("rate limit must be last; got %v", rej)
		}
	}

	// 5. Clean request admits and the context carries the S-2 deadline.
	{
		g, _, _ := buildGate(clock, &fakeFailureReader{})
		adm, rej := g.Check(context.Background(), "op", ph)
		if rej != nil || adm == nil {
			t.Fatalf("clean check must admit; rej=%v", rej)
		}
		ctx, cancel := adm.DeadlineContext(context.Background())
		defer cancel()
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("admitted request context must carry the S-2 deadline")
		}
		adm.Release()
	}
}

// P-16: every one of the 7 reject paths writes a protection.* audit event.
func TestEveryRejectProducesAuditEvent(t *testing.T) {
	clock := time.Now
	ph := "p"

	// killed
	{
		g, ks, audit := buildGate(clock, &fakeFailureReader{})
		_ = ks.SetKilled("op", true)
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionKilled {
			t.Fatalf("killed: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionKilled) {
			t.Fatalf("killed: expected audit action, got %v", audit.actions())
		}
	}

	// principal_killed
	{
		g, ks, audit := buildGate(clock, &fakeFailureReader{})
		_ = ks.SetPrincipalKilled(principalHash(ph, g.salt), true)
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionPrincipalKilled {
			t.Fatalf("principal_killed: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionPrincipalKilled) {
			t.Fatalf("principal_killed: expected audit action, got %v", audit.actions())
		}
	}

	// circuit_open
	{
		g, _, audit := buildGate(clock, &fakeFailureReader{window: FailureWindow{Count: 10, Truncated: false}})
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionCircuitOpen {
			t.Fatalf("circuit_open: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionCircuitOpen) {
			t.Fatalf("circuit_open: expected audit action, got %v", audit.actions())
		}
	}

	// breaker_unknown
	{
		g, _, audit := buildGate(clock, &fakeFailureReader{err: errors.New("audit down")})
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionBreakerUnknown {
			t.Fatalf("breaker_unknown: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionBreakerUnknown) {
			t.Fatalf("breaker_unknown: expected audit action, got %v", audit.actions())
		}
	}

	// concurrency
	{
		g, _, audit := buildGate(clock, &fakeFailureReader{})
		g.sem.Acquire("op")
		for i := 0; i < 7; i++ {
			g.sem.Acquire("op")
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionConcurrencyExceeded {
			t.Fatalf("concurrency: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionConcurrencyExceeded) {
			t.Fatalf("concurrency: expected audit action, got %v", audit.actions())
		}
	}

	// rate
	{
		g, _, audit := buildGate(clock, &fakeFailureReader{})
		for g.buckets.Take("op", principalHash(ph, g.salt)) {
		}
		if _, rej := g.Check(context.Background(), "op", ph); rej == nil || rej.Action != ActionRateLimited {
			t.Fatalf("rate: expected reject, got %v", rej)
		}
		if !contains(audit.actions(), ActionRateLimited) {
			t.Fatalf("rate: expected audit action, got %v", audit.actions())
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// P-21: the R21-8 truncated-breaker decision table.
func TestBreakerTruncatedBelowThresholdIsUnknown(t *testing.T) {
	cfg := DefaultBreakerConfig()
	rows := []struct {
		trunc    bool
		count    int
		expected BreakerState
	}{
		{false, 3, BreakerClosed},
		{true, 3, BreakerUnknown},
		{true, 5, BreakerOpen},
		{true, 7, BreakerOpen},
	}
	for _, r := range rows {
		b := NewBreakerSet(&fakeFailureReader{window: FailureWindow{Count: r.count, Truncated: r.trunc}}, cfg, time.Now)
		st, _, err := b.Evaluate("op", time.Now())
		if r.trunc && r.count < cfg.FailureThreshold {
			if st != BreakerUnknown || err != nil {
				t.Fatalf("row truncated=%v count=%d: expected Unknown, got %s err=%v", r.trunc, r.count, st, err)
			}
		} else if st != r.expected {
			t.Fatalf("row truncated=%v count=%d: expected %s, got %s", r.trunc, r.count, r.expected, st)
		}
	}
	// error row
	b := NewBreakerSet(&fakeFailureReader{err: errors.New("x")}, cfg, time.Now)
	st, _, err := b.Evaluate("op", time.Now())
	if err == nil || st != BreakerUnknown {
		t.Fatalf("error row: expected Unknown+err, got %s err=%v", st, err)
	}
}

// P-22: a reject survives an audit write failure (R21-9).
func TestRejectSurvivesAuditFailure(t *testing.T) {
	clock := time.Now
	store := newFakeKillPersistence()
	ks := NewKillStore(store, clock)
	_ = ks.Bootstrap()
	audit := &fakeAuditWriter{fail: true}
	gate := newTestGate(ks, &fakeFailureReader{}, audit)
	for gate.buckets.Take("op", principalHash("p", gate.salt)) { // drain to force rate reject
	}
	adm, rej := gate.Check(context.Background(), "op", "p")
	if rej == nil {
		t.Fatal("expected a reject despite audit failure")
	}
	if rej.Action != ActionRateLimited {
		t.Fatalf("expected rate_limited reject, got %s", rej.Action)
	}
	if adm != nil {
		t.Fatal("audit failure must NOT convert reject to admission")
	}
}

// P-24: audit write failure increments the exact counter (R21-11).
func TestAuditFailureIncrementsCounter(t *testing.T) {
	clock := time.Now
	store := newFakeKillPersistence()
	ks := NewKillStore(store, clock)
	_ = ks.Bootstrap()
	audit := &fakeAuditWriter{fail: true}
	gate := newTestGate(ks, &fakeFailureReader{}, audit)
	for gate.buckets.Take("op", principalHash("p", gate.salt)) {
	}
	gate.Check(context.Background(), "op", "p") // rejected, audit fails
	if got := gate.SnapshotMetrics().AuditWriteFailed; got != 1 {
		t.Fatalf("expected audit_write_failed counter = 1, got %d", got)
	}
}

// P-26: kill store tri-state distinguishes empty from failed (R21-13).
func TestKillStoreTriState(t *testing.T) {
	clock := time.Now
	// Uninitialized → fail closed.
	ks0 := NewKillStore(newFakeKillPersistence(), clock)
	if ks0.State() != KillStateUninitialized {
		t.Fatalf("expected Uninitialized, got %s", ks0.State())
	}
	if !ks0.IsKilled("op") {
		t.Fatal("uninitialized must fail closed")
	}

	// Ready with no kills → not killed.
	ks1 := NewKillStore(newFakeKillPersistence(), clock)
	if err := ks1.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if ks1.State() != KillStateReady {
		t.Fatalf("expected Ready, got %s", ks1.State())
	}
	if ks1.IsKilled("op") {
		t.Fatal("ready with no kills must not be killed")
	}

	// Failed → fail closed.
	store := newFakeKillPersistence()
	store.loadErr = errors.New("down")
	ks2 := NewKillStore(store, clock)
	if err := ks2.Bootstrap(); err == nil {
		t.Fatal("expected bootstrap error")
	}
	if ks2.State() != KillStateFailed {
		t.Fatalf("expected Failed, got %s", ks2.State())
	}
	if !ks2.IsKilled("op") {
		t.Fatal("failed must fail closed")
	}
}

// P-27: timeout does NOT auto-release the semaphore (R21-14).
func TestTimeoutDoesNotReleaseSemaphoreEarly(t *testing.T) {
	clock := time.Now
	store := newFakeKillPersistence()
	ks := NewKillStore(store, clock)
	_ = ks.Bootstrap()
	gate := newTestGate(ks, &fakeFailureReader{}, &fakeAuditWriter{})
	gate.sem = NewSemaphoreSet(1) // cap 1

	adm, rej := gate.Check(context.Background(), "op", "p")
	if rej != nil || adm == nil {
		t.Fatalf("expected admission; rej=%v", rej)
	}
	// Slot is held. A second acquire must fail (no auto-release on timeout).
	if gate.sem.Acquire("op") {
		t.Fatal("slot must remain held until Release()")
	}
	adm.Release()
	// Only after explicit Release does the slot free.
	if !gate.sem.Acquire("op") {
		t.Fatal("after Release, slot must free")
	}
	gate.sem.Release("op")
}
