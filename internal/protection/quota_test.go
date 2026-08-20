package protection

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Phase 23.2 Resource Quota Protection unit tests (ADR-051 / R23-1..R23-4).
//
// Locks the invariant that QuotaStore owns DEFINITIONS only (R23-3), the Gate
// consults the evidence source for consumption (never QuotaStore), an absent
// definition admits, and an Unknown/incomplete evidence reading fails CLOSED
// (R23-1/R23-4) — never substituting zero/default for unavailable evidence.
// ---------------------------------------------------------------------------

// memQuotaPersist is an in-memory QuotaPersistence for tests.
type memQuotaPersist struct {
	mu   sync.Mutex
	defs map[string]QuotaDefinition
}

func newMemQuotaPersist() *memQuotaPersist { return &memQuotaPersist{defs: map[string]QuotaDefinition{}} }

func (m *memQuotaPersist) LoadDefinitions() ([]QuotaDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]QuotaDefinition, 0, len(m.defs))
	for _, d := range m.defs {
		out = append(out, d)
	}
	return out, nil
}
func (m *memQuotaPersist) SaveDefinition(d QuotaDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defs[quotaKey(d.Capability, d.Principal)] = d
	return nil
}
func (m *memQuotaPersist) ClearDefinition(capability, principal string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.defs, quotaKey(capability, principal))
	return nil
}

// fakeEvidence is a controllable QuotaEvidenceReader: explicit usage map + a
// forced-error switch to simulate evidence-source failure (R23-1).
type fakeEvidence struct {
	mu    sync.Mutex
	usage map[string]QuotaUsage
	err   bool
	errV  error
}

func (f *fakeEvidence) CurrentUsage(capability, principal string) (QuotaUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err {
		if f.errV != nil {
			return QuotaUsage{}, f.errV
		}
		return QuotaUsage{}, errors.New("simulated evidence source failure")
	}
	if u, ok := f.usage[quotaKey(capability, principal)]; ok {
		return u, nil
	}
	return QuotaUsage{Complete: false}, nil // Unknown per R23-4
}

// recAudit records protection events for assertions.
type recAudit struct {
	mu     sync.Mutex
	events []ProtectionEvent
}

func (a *recAudit) WriteEvent(_ context.Context, ev ProtectionEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}
func (a *recAudit) hasAction(act string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.Action == act {
			return true
		}
	}
	return false
}

func TestQuotaStore_DefinitionOwnershipNoConsumption(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	if err := qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 1000, CPUSecs: 5}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// R23-3: QuotaStore holds only the definition. Holds NO consumption — the
	// struct exposes RSSBytes/CPUSecs as the CEILING, not observed usage. The
	// gate must read consumption from the evidence source, never from here.
	d, ok := qs.GetDefinition("exec", "alice")
	if !ok {
		t.Fatalf("definition not found")
	}
	if d.RSSBytes != 1000 || d.CPUSecs != 5 {
		t.Fatalf("ceiling fields wrong: %+v", d)
	}
	// A clean store keeps exactly one definition (single owner invariant).
	if got := len(qs.ListDefinitions()); got != 1 {
		t.Fatalf("expected exactly one definition, got %d", got)
	}
}

func TestQuotaStore_PrincipalOverridesCapabilityWide(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 1000})
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "alice", RSSBytes: 100})
	d, ok := qs.GetDefinition("exec", "alice")
	if !ok || d.RSSBytes != 100 {
		t.Fatalf("principal-specific definition must win, got %+v ok=%v", d, ok)
	}
	// A different principal falls back to the capability-wide default.
	d2, ok2 := qs.GetDefinition("exec", "bob")
	if !ok2 || d2.RSSBytes != 1000 {
		t.Fatalf("capability-wide default must apply, got %+v ok=%v", d2, ok2)
	}
	if err := qs.ClearDefinition("exec", "alice"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := qs.GetDefinition("exec", "alice"); !ok {
		t.Fatalf("after clearing principal def, must fall back to wide (ok=false expected — no principal left, wide still present)")
	}
	// Wide still present.
	if _, ok := qs.GetDefinition("exec", ""); !ok {
		t.Fatalf("capability-wide definition should survive a principal clear")
	}
}

func TestQuotaExceeded_CeilingSemantics(t *testing.T) {
	def := QuotaDefinition{Capability: "exec", RSSBytes: 1000, CPUSecs: 10}
	if !QuotaExceeded(def, QuotaUsage{RSSBytes: 2000, Complete: true}) {
		t.Fatalf("RSS over ceiling must exceed")
	}
	if QuotaExceeded(def, QuotaUsage{RSSBytes: 500, CPUSecs: 5, Complete: true}) {
		t.Fatalf("within ceiling must NOT exceed")
	}
	// Zero-valued ceiling dimension means "unlimited".
	if QuotaExceeded(QuotaDefinition{Capability: "exec", RSSBytes: 0, CPUSecs: 10}, QuotaUsage{RSSBytes: 999999, CPUSecs: 1, Complete: true}) {
		t.Fatalf("RSS=0 ⇒ unlimited, must not exceed")
	}
}

// TestGateQuota_AbsentDefinitionAdmits: with no quota definition the gate applies
// NO quota constraint ⇒ admit (R23-4: absent definition ⇒ no constraint).
func TestGateQuota_AbsentDefinitionAdmits(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	g := New(Config{Quotas: qs, Evidence: &fakeEvidence{}, Audit: &recAudit{}})
	adm, rej := g.Check(context.Background(), "exec", "alice")
	if rej != nil {
		t.Fatalf("absent definition must admit, got reject %+v", rej)
	}
	adm.Release()
}

// TestGateQuota_ExceededRejects: definition present + complete over-limit
// evidence ⇒ reject protection.quota_exceeded (503).
func TestGateQuota_ExceededRejects(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 100})
	ev := &fakeEvidence{usage: map[string]QuotaUsage{
		quotaKey("exec", "alice"): {RSSBytes: 200, Complete: true},
	}}
	rec := &recAudit{}
	g := New(Config{Quotas: qs, Evidence: ev, Audit: rec})
	_, rej := g.Check(context.Background(), "exec", "alice")
	if rej == nil {
		t.Fatal("over-limit ⇒ must reject")
	}
	if rej.Action != ActionQuotaExceeded {
		t.Fatalf("want %s, got %s", ActionQuotaExceeded, rej.Action)
	}
	if rej.HTTPStatus != 503 {
		t.Fatalf("want 503, got %d", rej.HTTPStatus)
	}
	if !rec.hasAction(ActionQuotaExceeded) {
		t.Fatalf("expected %s audit observation", ActionQuotaExceeded)
	}
}

// TestGateQuota_EvidenceUnknownFailsClosed: definition present but evidence is
// Unknown (no observation) ⇒ fail-closed reject (R23-1/R23-4). Consumption is
// NEVER read from QuotaStore — proving the evidence source is the sole input.
func TestGateQuota_EvidenceUnknownFailsClosed(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 100})
	rec := &recAudit{}
	g := New(Config{Quotas: qs, Evidence: &fakeEvidence{}, Audit: rec})
	_, rej := g.Check(context.Background(), "exec", "alice")
	if rej == nil {
		t.Fatal("Unknown evidence ⇒ must fail closed")
	}
	if rej.Action != ActionQuotaEvidenceUnavailable {
		t.Fatalf("want %s, got %s", ActionQuotaEvidenceUnavailable, rej.Action)
	}
	if rej.HTTPStatus != 503 {
		t.Fatalf("want 503, got %d", rej.HTTPStatus)
	}
	if !rec.hasAction(ActionQuotaEvidenceUnavailable) {
		t.Fatalf("expected %s audit observation", ActionQuotaEvidenceUnavailable)
	}
}

// TestGateQuota_EvidenceErrorFailsClosed: an evidence-source error is treated as
// Unknown ⇒ fail-closed (R23-1).
func TestGateQuota_EvidenceErrorFailsClosed(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 100})
	ev := &fakeEvidence{err: true}
	rec := &recAudit{}
	g := New(Config{Quotas: qs, Evidence: ev, Audit: rec})
	_, rej := g.Check(context.Background(), "exec", "alice")
	if rej == nil || rej.Action != ActionQuotaEvidenceUnavailable {
		t.Fatalf("evidence error ⇒ must fail closed with %s, got %+v", ActionQuotaEvidenceUnavailable, rej)
	}
}

// TestGateQuota_ConcurrencyRolledBackOnReject: a quota reject must release the
// concurrency slot acquired in step 4 (a reject is not a leaked semaphore).
func TestGateQuota_ConcurrencyRolledBackOnReject(t *testing.T) {
	qs := NewQuotaStore(newMemQuotaPersist(), nil)
	_ = qs.SetDefinition(QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 100})
	sem := NewSemaphoreSet(1) // capacity 1 so we can detect a leak
	g := New(Config{Quotas: qs, Evidence: &fakeEvidence{}, Audit: &recAudit{}, Sem: sem})
	// First check rejects (evidence Unknown) and should free the slot.
	if _, rej := g.Check(context.Background(), "exec", "alice"); rej == nil {
		t.Fatal("expected fail-closed reject")
	}
	// Slot must be free again: a fresh admit (no quota def) should succeed.
	adm, rej := g.Check(context.Background(), "other-cap", "alice")
	if rej != nil {
		t.Fatalf("semaphore slot leaked after quota reject: %+v", rej)
	}
	adm.Release()
	// And metrics track the evidence-unavailable counter.
	if got := g.SnapshotMetrics().QuotaEvidenceUnavailable; got != 1 {
		t.Fatalf("quota_evidence_unavailable counter = %d, want 1", got)
	}
}
