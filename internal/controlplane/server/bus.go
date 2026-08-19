package server

import (
	"sync"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// ExecutionEventBus is the in-process, multi-subscriber implementation
// of execution.EventBus (Phase 2.1 / Round 3). The ExecutionService
// publishes lifecycle events onto it; the WebSocket hub subscribes and
// fans them out to connected UI clients. It is nil-safe by contract
// (the Service checks bus != nil) but this concrete type is always
// non-nil when wired by the server.
//
// Design: events are delivered non-blocking. A slow subscriber that
// does not drain its channel (buffered, small) is dropped rather than
// stalling the execution hot path — one stuck browser tab must never
// back-pressure a running operation.
type ExecutionEventBus struct {
	mu   sync.Mutex
	subs map[chan execution.ExecutionEvent]struct{}
}

// NewExecutionEventBus builds an empty bus.
func NewExecutionEventBus() *ExecutionEventBus {
	return &ExecutionEventBus{subs: make(map[chan execution.ExecutionEvent]struct{})}
}

// Publish fans ev out to every current subscriber, dropping it on any
// subscriber whose buffer is full (slow consumer protection).
func (b *ExecutionEventBus) Publish(ev execution.ExecutionEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop to protect the publisher.
		}
	}
}

// Subscribe registers a new subscriber and returns its receive channel.
// The caller MUST call Unsubscribe when done to avoid a leak.
func (b *ExecutionEventBus) Subscribe() chan execution.ExecutionEvent {
	ch := make(chan execution.ExecutionEvent, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber's channel.
func (b *ExecutionEventBus) Unsubscribe(ch chan execution.ExecutionEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}
