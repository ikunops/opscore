package protection

import (
	"sync"
)

// SemaphoreSet manages per-capabilityID bounded concurrency (S-5).
type SemaphoreSet struct {
	mu         sync.Mutex
	sems       map[string]chan struct{}
	caps       map[string]int
	defaultCap int
}

// NewSemaphoreSet builds a semaphore set with the R93-accepted default cap (8).
func NewSemaphoreSet(defaultCap int) *SemaphoreSet {
	if defaultCap <= 0 {
		defaultCap = 8
	}
	return &SemaphoreSet{
		sems:       make(map[string]chan struct{}),
		caps:       make(map[string]int),
		defaultCap: defaultCap,
	}
}

// SetCap sets the concurrency cap for a capability (override of the default).
func (ss *SemaphoreSet) SetCap(capID string, cap int) {
	if cap <= 0 {
		cap = ss.defaultCap
	}
	ss.mu.Lock()
	ss.caps[capID] = cap
	ss.mu.Unlock()
}

func (ss *SemaphoreSet) cap(capID string) int {
	if c, ok := ss.caps[capID]; ok {
		return c
	}
	return ss.defaultCap
}

// Acquire takes a slot. Returns true if acquired, false if full. Does NOT
// block (R93 accepted: reject does not wait indefinitely).
func (ss *SemaphoreSet) Acquire(capID string) bool {
	ss.mu.Lock()
	ch, ok := ss.sems[capID]
	if !ok {
		ch = make(chan struct{}, ss.cap(capID))
		ss.sems[capID] = ch
	}
	ss.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release returns a slot. MUST be called on every path (success, error,
// timeout). Safe to call when no slot is held.
func (ss *SemaphoreSet) Release(capID string) {
	ss.mu.Lock()
	ch := ss.sems[capID]
	ss.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
		}
	}
}
