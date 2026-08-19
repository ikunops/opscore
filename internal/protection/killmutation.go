package protection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrAuditOutcomeFailed indicates the KillStore mutation succeeded but the
// durable audit OUTCOME could not be recorded. Per P22-8 the mutation is NOT
// rolled back (the audit persistence and the kill store are distinct domains
// with no cross-store transaction); the caller returns a degraded error and the
// already-durable intent event remains queryable as an intent-without-outcome
// record.
var ErrAuditOutcomeFailed = errors.New("protection: audit outcome write failed after kill-store mutation")

// OperatorMutation is the SINGLE write entry point for operator-initiated
// kill/release (P22-2). It writes ONLY through the KillStore that the Gate reads
// (no second KillState) and records protection.kill / protection.release audit
// observations via the AuditWriter. It never changes Gate semantics.
//
// Audit ordering (P22-8, accepted from Phase 17): intent → mutation → outcome.
// The intent is made durable BEFORE the mutation; after a successful mutation
// the outcome is attempted. If the outcome write fails the mutation stands
// (no rollback claim — the two persistence domains share no transaction) and
// ErrAuditOutcomeFailed is returned.
type OperatorMutation struct {
	ks    *KillStore
	audit AuditWriter
	clock func() time.Time
}

// NewOperatorMutation builds the operator mutation service. A nil clock defaults
// to time.Now.
func NewOperatorMutation(ks *KillStore, audit AuditWriter, clock func() time.Time) *OperatorMutation {
	if clock == nil {
		clock = time.Now
	}
	return &OperatorMutation{ks: ks, audit: audit, clock: clock}
}

// KillMutationResult is the server-derived outcome of an operator kill/release.
// Every field is computed by the server from the real Protection State — never
// accepted from the client (P22-10).
type KillMutationResult struct {
	CapabilityID string
	PrevKilled   bool
	NewKilled    bool
	Reason       string
	Operator     string
	At           time.Time
}

// Kill records an operator-initiated kill of capID (P22-4: the Gate still
// evaluates normally afterwards; this only sets the kill flag). prev/new are
// derived from the KillStore, never from the request.
func (m *OperatorMutation) Kill(ctx context.Context, capID, reason, operator string) (KillMutationResult, error) {
	now := m.clock()
	prev := m.ks.IsKilled(capID) // server truth, NOT client-provided

	// 1. durable audit intent (protection.kill intent).
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: now, Action: ActionOperatorKill, CapID: capID,
		Principal: operator, Detail: "intent",
	}); err != nil {
		return KillMutationResult{}, fmt.Errorf("protection: audit intent failed: %w", err)
	}

	// 2. KillStore mutation (single owner, persisted).
	if err := m.ks.RecordOperatorKill(capID, operator, reason); err != nil {
		return KillMutationResult{}, fmt.Errorf("protection: kill-store mutation failed: %w", err)
	}

	// 3. server-derived new + durable audit outcome.
	res := KillMutationResult{CapabilityID: capID, PrevKilled: prev, NewKilled: true, Reason: reason, Operator: operator, At: m.clock()}
	detail, _ := json.Marshal(map[string]any{
		"prev_killed": prev, "new_killed": true, "reason": reason, "operator": operator,
	})
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: res.At, Action: ActionOperatorKill, CapID: capID,
		Principal: operator, Detail: string(detail),
	}); err != nil {
		return res, ErrAuditOutcomeFailed
	}
	return res, nil
}

// Release removes an operator-initiated kill (P22-4: does NOT restore execution;
// the next admission is re-evaluated by the Gate — breaker/policy/rate/concurrency
// may still block). prev/new are server-derived.
func (m *OperatorMutation) Release(ctx context.Context, capID, operator string) (KillMutationResult, error) {
	now := m.clock()
	prev := m.ks.IsKilled(capID) // server truth

	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: now, Action: ActionOperatorRelease, CapID: capID,
		Principal: operator, Detail: "intent",
	}); err != nil {
		return KillMutationResult{}, fmt.Errorf("protection: audit intent failed: %w", err)
	}

	if err := m.ks.ClearOperatorKill(capID); err != nil {
		return KillMutationResult{}, fmt.Errorf("protection: kill-store mutation failed: %w", err)
	}

	res := KillMutationResult{CapabilityID: capID, PrevKilled: prev, NewKilled: false, Operator: operator, At: m.clock()}
	detail, _ := json.Marshal(map[string]any{
		"prev_killed": prev, "new_killed": false, "operator": operator,
	})
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: res.At, Action: ActionOperatorRelease, CapID: capID,
		Principal: operator, Detail: string(detail),
	}); err != nil {
		return res, ErrAuditOutcomeFailed
	}
	return res, nil
}

func (m *OperatorMutation) writeAudit(ctx context.Context, ev ProtectionEvent) error {
	if m.audit == nil {
		return nil
	}
	return m.audit.WriteEvent(ctx, ev)
}
