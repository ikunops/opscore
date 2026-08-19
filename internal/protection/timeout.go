package protection

import (
	"context"
	"time"
)

// TimeoutConfig holds the per-capability cooperative timeout (S-2, R93-①).
// R93-①: a timeout is a cancellation/deadline SIGNAL, NOT a guaranteed
// termination primitive. The downstream plugin observes context.DeadlineExceeded
// via ctx.Done(); whether it actually stops depends on cooperative context
// handling. The Gate adds NO goroutine-kill or process-termination call of any
// kind (enforced by P-5 / M-4: protection never terminates goroutines or
// processes; the downstream plugin cooperatively observes ctx.Done()).
type TimeoutConfig struct {
	Default  time.Duration            // 30s
	Override map[string]time.Duration // per capabilityID
}

// NewTimeoutConfig builds a timeout config with the R93-accepted defaults.
func NewTimeoutConfig() *TimeoutConfig {
	return &TimeoutConfig{
		Default:  30 * time.Second,
		Override: make(map[string]time.Duration),
	}
}

// WithDeadline wraps parent with a deadline for capID. R93-①: this is a
// context.WithTimeout wrapper — cancellation signal only. The returned
// CancelFunc MUST be called by the caller (typically via Admission.Release).
//
// NOTE: the ADR named this method Apply, but "Apply" is a forbidden method name
// in the Phase-8 peripheral-package AST guard (it implies a mutating driver
// verb). Renamed to WithDeadline to satisfy that guard while keeping the exact
// same cooperative-deadline semantics.
func (tc *TimeoutConfig) WithDeadline(parent context.Context, capID string) (context.Context, context.CancelFunc) {
	d := tc.Default
	if tc.Override != nil {
		if o, ok := tc.Override[capID]; ok {
			d = o
		}
	}
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
