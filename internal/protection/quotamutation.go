package protection

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// OperatorQuotaMutation is the SINGLE write entry point for operator-initiated
// quota definition changes (Phase 23.2 / R23-3). It writes ONLY through the
// QuotaStore that the Gate reads (no second definition source) and records
// protection.quota_set / protection.quota_clear audit observations via the
// AuditWriter. It never changes Gate admission semantics — it only edits the
// definitions the quota step consults.
//
// Audit ordering mirrors P22-8: intent → mutation → outcome. The intent is
// made durable BEFORE the mutation; after a successful mutation the outcome is
// attempted. If the outcome write fails the mutation stands (no rollback claim
// — the two persistence domains share no transaction) and ErrAuditOutcomeFailed
// is returned.
type OperatorQuotaMutation struct {
	qs    *QuotaStore
	audit AuditWriter
	clock func() time.Time
}

// NewOperatorQuotaMutation builds the operator quota mutation service. A nil
// clock defaults to time.Now.
func NewOperatorQuotaMutation(qs *QuotaStore, audit AuditWriter, clock func() time.Time) *OperatorQuotaMutation {
	if clock == nil {
		clock = time.Now
	}
	return &OperatorQuotaMutation{qs: qs, audit: audit, clock: clock}
}

// QuotaMutationResult is the server-derived outcome of an operator quota
// set/clear. Every field is computed by the server from the real QuotaStore —
// never accepted from the client (P22-10 analog, R23-3: definitions only, no
// consumption is ever exposed to or accepted from the client).
type QuotaMutationResult struct {
	Capability string
	Principal  string
	Action     string // "set" | "clear"
	Operator   string
	At         time.Time
}

// Set writes (upserts) a quota definition through the single owner QuotaStore.
func (m *OperatorQuotaMutation) Set(ctx context.Context, def QuotaDefinition, operator string) (QuotaMutationResult, error) {
	now := m.clock()

	// 1. durable audit intent (protection.quota_set intent).
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: now, Action: ActionQuotaSet, CapID: def.Capability,
		Principal: operator, Detail: "intent",
	}); err != nil {
		return QuotaMutationResult{}, fmt.Errorf("protection: audit intent failed: %w", err)
	}

	// 2. QuotaStore mutation (single owner, persisted).
	if err := m.qs.SetDefinition(def); err != nil {
		return QuotaMutationResult{}, fmt.Errorf("protection: quota-store mutation failed: %w", err)
	}

	// 3. server-derived outcome + durable audit outcome.
	res := QuotaMutationResult{Capability: def.Capability, Principal: def.Principal, Action: "set", Operator: operator, At: m.clock()}
	detail, _ := json.Marshal(map[string]any{
		"capability": def.Capability, "principal": def.Principal,
		"rss_bytes": def.RSSBytes, "cpu_secs": def.CPUSecs, "operator": operator,
	})
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: res.At, Action: ActionQuotaSet, CapID: def.Capability,
		Principal: operator, Detail: string(detail),
	}); err != nil {
		return res, ErrAuditOutcomeFailed
	}
	return res, nil
}

// Clear removes a quota definition through the single owner QuotaStore.
func (m *OperatorQuotaMutation) Clear(ctx context.Context, capability, principal, operator string) (QuotaMutationResult, error) {
	now := m.clock()

	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: now, Action: ActionQuotaClear, CapID: capability,
		Principal: operator, Detail: "intent",
	}); err != nil {
		return QuotaMutationResult{}, fmt.Errorf("protection: audit intent failed: %w", err)
	}

	if err := m.qs.ClearDefinition(capability, principal); err != nil {
		return QuotaMutationResult{}, fmt.Errorf("protection: quota-store mutation failed: %w", err)
	}

	res := QuotaMutationResult{Capability: capability, Principal: principal, Action: "clear", Operator: operator, At: m.clock()}
	detail, _ := json.Marshal(map[string]any{
		"capability": capability, "principal": principal, "operator": operator,
	})
	if err := m.writeAudit(ctx, ProtectionEvent{
		Timestamp: res.At, Action: ActionQuotaClear, CapID: capability,
		Principal: operator, Detail: string(detail),
	}); err != nil {
		return res, ErrAuditOutcomeFailed
	}
	return res, nil
}

func (m *OperatorQuotaMutation) writeAudit(ctx context.Context, ev ProtectionEvent) error {
	if m.audit == nil {
		return nil
	}
	return m.audit.WriteEvent(ctx, ev)
}
