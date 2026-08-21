package protection

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// TokenBucketConfig configures a token bucket.
type TokenBucketConfig struct {
	Capacity float64 // tokens at full
	Refill   float64 // tokens per second
}

// TokenBucketSet manages per-(capabilityID, principalHash) buckets (S-1).
// Rejected requests never reach Take (concurrency is checked first), so a
// rejected-by-concurrency request does not consume a token (R93 accepted).
type TokenBucketSet struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	clock   func() time.Time
	cfg     TokenBucketConfig
}

type tokenBucket struct {
	tokens   float64
	capacity float64
	refill   float64 // tokens per second
	last     time.Time
}

// NewTokenBucketSet builds a bucket set. clock is injectable for testing.
func NewTokenBucketSet(cfg TokenBucketConfig, clock func() time.Time) *TokenBucketSet {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 60
	}
	if cfg.Refill <= 0 {
		cfg.Refill = cfg.Capacity / 60 // default: capacity tokens per 60s
	}
	if clock == nil {
		clock = time.Now
	}
	return &TokenBucketSet{
		buckets: make(map[string]*tokenBucket),
		clock:   clock,
		cfg:     cfg,
	}
}

// Capacity returns the configured bucket capacity (read-side accessor for
// decision-time provenance evidence, R24-1).
func (tbs *TokenBucketSet) Capacity() float64 { return tbs.cfg.Capacity }

// Refill returns the configured refill rate in tokens/second (read-side
// accessor for decision-time provenance evidence, R24-1).
func (tbs *TokenBucketSet) Refill() float64 { return tbs.cfg.Refill }

// Take consumes a token for (capID, hash). Returns true if a token was
// available, false if the bucket is empty. Pure arithmetic, no goroutine, no
// blocking wait (R93 accepted: reject does not wait).
func (tbs *TokenBucketSet) Take(capID, hash string) bool {
	key := capID + ":" + hash
	now := tbs.clock()
	tbs.mu.Lock()
	b, ok := tbs.buckets[key]
	if !ok {
		b = &tokenBucket{
			tokens:   tbs.cfg.Capacity,
			capacity: tbs.cfg.Capacity,
			refill:   tbs.cfg.Refill,
			last:     now,
		}
		tbs.buckets[key] = b
	}
	tbs.mu.Unlock()
	return b.take(now)
}

// take refills based on elapsed time then consumes one token if available.
// M-1 (remove refill logic) fails P-1; M-2 (return true when empty) fails P-2.
func (t *tokenBucket) take(now time.Time) bool {
	elapsed := now.Sub(t.last).Seconds()
	if elapsed > 0 {
		t.tokens = min(t.capacity, t.tokens+elapsed*t.refill)
		t.last = now
	}
	if t.tokens >= 1 {
		t.tokens -= 1
		return true
	}
	return false
}

// principalHash hashes a principal with a per-process random salt. The salt is
// generated at startup, never persisted, and exists only to prevent
// cross-restart correlation. The hash is never logged in cleartext.
func principalHash(principal string, salt []byte) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(principal))
	return hex.EncodeToString(h.Sum(nil))
}

// newSalt generates a fresh per-process salt.
func newSalt() []byte {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	return salt
}
