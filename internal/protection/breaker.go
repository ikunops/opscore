package protection

import (
	"sync"
	"time"
)

// BreakerState is the 4-state circuit breaker enum (R93-② adds Unknown).
type BreakerState int

const (
	// BreakerClosed: normal operation.
	BreakerClosed BreakerState = iota
	// BreakerOpen: rejecting all requests for this capability.
	BreakerOpen
	// BreakerHalfOpen: admitting exactly one probe request after cooldown.
	BreakerHalfOpen
	// BreakerUnknown: audit evidence unreadable or truncated below threshold.
	// Fail closed (R93-②, R21-8): the request is rejected.
	BreakerUnknown
)

func (b BreakerState) String() string {
	switch b {
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	case BreakerUnknown:
		return "unknown"
	default:
		return "closed"
	}
}

// BreakerConfig tunes the breaker.
type BreakerConfig struct {
	FailureThreshold int           // consecutive/visible failures to open
	Window           time.Duration // sliding window for evidence
	Cooldown         time.Duration // before half-open probe
}

// DefaultBreakerConfig is the R93-accepted default.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold: 5,
		Window:           60 * time.Second,
		Cooldown:         30 * time.Second,
	}
}

// BreakerSet manages per-capabilityID breakers (S-3). It depends ONLY on the
// FailureEvidenceReader (R21-12); it never imports storage/audit.
type BreakerSet struct {
	mu       sync.Mutex
	breakers map[string]*circuitBreaker
	reader   FailureEvidenceReader
	clock    func() time.Time
	cfg      BreakerConfig
}

type circuitBreaker struct {
	state        BreakerState
	failureCount int // in-memory, fed by RecordOutcome (P-6)
	openedAt     time.Time
	probeAllowed bool // half-open admits exactly one probe
}

// NewBreakerSet builds a breaker set. clock is injectable for testing.
func NewBreakerSet(reader FailureEvidenceReader, cfg BreakerConfig, clock func() time.Time) *BreakerSet {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Window <= 0 {
		cfg.Window = 60 * time.Second
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if clock == nil {
		clock = time.Now
	}
	return &BreakerSet{
		breakers: make(map[string]*circuitBreaker),
		reader:   reader,
		clock:    clock,
		cfg:      cfg,
	}
}

// FailureThreshold returns the configured consecutive-failure threshold. It is
// the read-side accessor used to record decision-time provenance evidence
// (R24-1): the provenance log reports the threshold that informed a breaker
// decision, but never re-evaluates the breaker.
func (bs *BreakerSet) FailureThreshold() int { return bs.cfg.FailureThreshold }

func (bs *BreakerSet) get(capID string) *circuitBreaker {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b, ok := bs.breakers[capID]
	if !ok {
		b = &circuitBreaker{state: BreakerClosed}
		bs.breakers[capID] = b
	}
	return b
}

// Evaluate returns the breaker state for capID. Guard order is fixed; the
// caller treats Unknown and Open as reject (fail closed).
//
// Decision table (binding, R21-8):
//
//	truncated=false, failures < threshold  → Closed
//	truncated=true,  failures < threshold  → Unknown   (fail closed)
//	truncated=true,  failures ≥ threshold   → Open      (lower bound proves it)
//	audit error                           → Unknown    (fail closed)
func (bs *BreakerSet) Evaluate(capID string, now time.Time) (BreakerState, int, error) {
	b := bs.get(capID)
	bs.mu.Lock()
	defer bs.mu.Unlock()

	switch b.state {
	case BreakerOpen:
		if now.Sub(b.openedAt) >= bs.cfg.Cooldown {
			b.state = BreakerHalfOpen
			b.probeAllowed = true
			return BreakerHalfOpen, b.failureCount, nil
		}
		return BreakerOpen, b.failureCount, nil
	case BreakerHalfOpen:
		return BreakerHalfOpen, b.failureCount, nil
	}

	// Closed state: consult the evidence reader (R93-②).
	if bs.reader == nil {
		return BreakerClosed, 0, nil
	}
	w, err := bs.reader.RecentFailures(capID, bs.cfg.Window)
	if err != nil {
		return BreakerUnknown, 0, err // R93-②: evidence unreadable → fail closed
	}

	visible := w.Count
	// When the evidence store is clean (no truncation, zero visible failures),
	// supplement with the in-memory failure count fed by RecordOutcome so a
	// process that sees failures but has not yet persisted them still opens
	// (P-6). When the store reports any data, the store is authoritative and we
	// do NOT double-count with the in-memory counter.
	effective := visible
	if !w.Truncated && visible == 0 {
		effective = b.failureCount
	}

	if w.Truncated && effective < bs.cfg.FailureThreshold {
		return BreakerUnknown, effective, nil // R21-8: fail closed
	}
	if effective >= bs.cfg.FailureThreshold {
		b.state = BreakerOpen
		b.openedAt = now
		return BreakerOpen, effective, nil
	}
	return BreakerClosed, effective, nil
}

// RecordOutcome feeds an execution result into the breaker (R21-10: protection
// feedback only). It is consulted by the HalfOpen probe transition; in the
// Closed state the evidence reader remains authoritative (no in-memory open).
func (bs *BreakerSet) RecordOutcome(capID string, failed bool, now time.Time) {
	b := bs.get(capID)
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if failed {
		b.failureCount++
	}
	if b.state == BreakerHalfOpen {
		if failed {
			b.state = BreakerOpen
			b.openedAt = now
		} else {
			b.state = BreakerClosed
		}
		b.probeAllowed = false
	}
}
