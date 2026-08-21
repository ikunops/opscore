package protection

import (
	"sync"
	"time"
)

// bucketSample is one point-in-time snapshot of the exact counters.
type bucketSample struct {
	Time    time.Time
	Metrics Metrics
}

// RateHistory is a bounded, time-ordered buffer of counter snapshots. It is a
// READ PROJECTION (R24-5): it is fed off the Gate path (a background ticker in
// the composition root) and never influences a Gate decision. It exists solely
// to derive sliding-window rate deltas for declarative alerting.
//
// Bounded: it retains at most `cap` samples (oldest evicted). It is safe for
// concurrent Record (writer goroutine) + WindowDelta (reader handler) use.
type RateHistory struct {
	window time.Duration
	cap    int
	mu     sync.Mutex
	samples []bucketSample
}

// NewRateHistory builds a rate-history buffer. window<=0 defaults to 1 minute;
// capacity<=0 defaults to 120 samples.
func NewRateHistory(window time.Duration, capacity int) *RateHistory {
	if window <= 0 {
		window = time.Minute
	}
	if capacity <= 0 {
		capacity = 120
	}
	return &RateHistory{
		window:  window,
		cap:     capacity,
		samples: make([]bucketSample, 0, capacity),
	}
}

// Record appends a snapshot. Intended to be called off the Gate path (a
// background ticker); it is safe to call concurrently with WindowDelta.
func (h *RateHistory) Record(now time.Time, m Metrics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, bucketSample{Time: now, Metrics: m})
	if len(h.samples) > h.cap {
		// Evict the oldest; bounded memory.
		h.samples = h.samples[1:]
	}
}

// WindowDelta returns the per-counter delta over the trailing `window`, ending
// at `now`. ok=false means there is insufficient history to make a confident
// delta — the caller MUST treat ok=false as CONSERVATIVE "do not fire" (R24-3
// declarative alerting must never raise on missing evidence). Specifically
// ok=false when there are fewer than two samples, or when no sample is older
// than (now - window) (i.e. the buffer does not yet span a full window).
func (h *RateHistory) WindowDelta(window time.Duration, now time.Time) (Metrics, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.samples) < 2 {
		return Metrics{}, false
	}
	cutoff := now.Add(-window)
	// Find the newest sample whose Time is at or before the window cutoff; that
	// sample is the "window start" baseline.
	startIdx := -1
	for i := len(h.samples) - 1; i >= 0; i-- {
		if !h.samples[i].Time.After(cutoff) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		// No sample older than the window: history too short to be confident.
		return Metrics{}, false
	}
	start := h.samples[startIdx].Metrics
	end := h.samples[len(h.samples)-1].Metrics
	return subtractMetrics(end, start), true
}

// subtractMetrics computes per-field deltas between two counter snapshots.
func subtractMetrics(a, b Metrics) Metrics {
	return Metrics{
		Admitted:                a.Admitted - b.Admitted,
		Killed:                  a.Killed - b.Killed,
		PrincipalKilled:         a.PrincipalKilled - b.PrincipalKilled,
		CircuitOpen:             a.CircuitOpen - b.CircuitOpen,
		BreakerUnknown:          a.BreakerUnknown - b.BreakerUnknown,
		ConcurrencyExceeded:     a.ConcurrencyExceeded - b.ConcurrencyExceeded,
		QuotaExceeded:           a.QuotaExceeded - b.QuotaExceeded,
		QuotaEvidenceUnavailable: a.QuotaEvidenceUnavailable - b.QuotaEvidenceUnavailable,
		RateLimited:             a.RateLimited - b.RateLimited,
		AuditWriteFailed:        a.AuditWriteFailed - b.AuditWriteFailed,
	}
}
