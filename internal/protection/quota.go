package protection

import (
	"fmt"
	"sync"
	"time"
)

// QuotaDefinition is an operator-configured resource ceiling (R23-3: QuotaStore
// owns ONLY definitions; current consumption lives in the evidence source and
// is NEVER duplicated as authoritative state here).
type QuotaDefinition struct {
	Capability string  // capability id the ceiling applies to
	Principal  string  // "" = capability-wide default applying to all principals
	RSSBytes   int64   // peak RSS ceiling (bytes); 0 = unlimited
	CPUSecs    float64 // CPU-seconds budget over the bounded window; 0 = unlimited
}

// QuotaUsage is observed current consumption from the evidence source (R23-3).
// It is owned exclusively by QuotaEvidenceReader, never by QuotaStore.
type QuotaUsage struct {
	RSSBytes int64
	CPUSecs  float64
	// Complete is false when evidence is unavailable/incomplete or the observation
	// window is invalid. The caller MUST treat it as Unknown (R23-4) — never as
	// zero usage, never as a default limit.
	Complete bool
}

// QuotaEvidenceReader is the sole read dependency for consumption (R21-12 /
// R23-3). protection defines the interface; controlplane supplies the adapter
// (RemoteExecution samples or auditor resource field). A non-complete result is
// Unknown (R23-4), not zero.
type QuotaEvidenceReader interface {
	CurrentUsage(capabilityID, principal string) (QuotaUsage, error)
}

// QuotaPersistence is the storage interface for quota definitions (R23-3). The
// protection package defines it; storage/memory (and storage/sqlite) implement
// it. Single owner: only QuotaStore writes through this interface.
type QuotaPersistence interface {
	LoadDefinitions() ([]QuotaDefinition, error)
	SaveDefinition(d QuotaDefinition) error
	ClearDefinition(capability, principal string) error
}

// QuotaStore owns quota DEFINITIONS (R23-3). The in-memory map is a runtime
// projection of persistent state; it holds NO consumption.
type QuotaStore struct {
	mu      sync.RWMutex
	defs    map[string]QuotaDefinition // key: capability + "\x00" + principal
	persist QuotaPersistence
	clock   func() time.Time
}

func quotaKey(capability, principal string) string { return capability + "\x00" + principal }

// NewQuotaStore builds an uninitialized quota store around a persistence backend.
func NewQuotaStore(p QuotaPersistence, clock func() time.Time) *QuotaStore {
	if clock == nil {
		clock = time.Now
	}
	return &QuotaStore{persist: p, defs: map[string]QuotaDefinition{}, clock: clock}
}

// Bootstrap loads persistent definitions at startup.
func (qs *QuotaStore) Bootstrap() error {
	ds, err := qs.persist.LoadDefinitions()
	if err != nil {
		return fmt.Errorf("quota store load: %w", err)
	}
	qs.mu.Lock()
	defer qs.mu.Unlock()
	qs.defs = map[string]QuotaDefinition{}
	for _, d := range ds {
		qs.defs[quotaKey(d.Capability, d.Principal)] = d
	}
	return nil
}

// GetDefinition returns the principal-specific definition if present, else the
// capability-wide default (principal==""). ok=false means no definition at all
// (→ Unknown per R23-4).
func (qs *QuotaStore) GetDefinition(capability, principal string) (QuotaDefinition, bool) {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	if d, ok := qs.defs[quotaKey(capability, principal)]; ok {
		return d, true
	}
	if d, ok := qs.defs[quotaKey(capability, "")]; ok {
		return d, true
	}
	return QuotaDefinition{}, false
}

// SetDefinition writes a definition through to persistence (single owner).
func (qs *QuotaStore) SetDefinition(d QuotaDefinition) error {
	if err := qs.persist.SaveDefinition(d); err != nil {
		return err
	}
	qs.mu.Lock()
	defer qs.mu.Unlock()
	qs.defs[quotaKey(d.Capability, d.Principal)] = d
	return nil
}

// ClearDefinition removes a definition (single owner).
func (qs *QuotaStore) ClearDefinition(capability, principal string) error {
	if err := qs.persist.ClearDefinition(capability, principal); err != nil {
		return err
	}
	qs.mu.Lock()
	defer qs.mu.Unlock()
	delete(qs.defs, quotaKey(capability, principal))
	return nil
}

// ListDefinitions returns all definitions for the read surface.
func (qs *QuotaStore) ListDefinitions() []QuotaDefinition {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	out := make([]QuotaDefinition, 0, len(qs.defs))
	for _, d := range qs.defs {
		out = append(out, d)
	}
	return out
}

// QuotaExceeded reports whether observed usage breaches the definition. A
// zero-valued field on the definition means "unlimited" for that dimension.
func QuotaExceeded(def QuotaDefinition, u QuotaUsage) bool {
	if def.RSSBytes > 0 && u.RSSBytes > def.RSSBytes {
		return true
	}
	if def.CPUSecs > 0 && u.CPUSecs > def.CPUSecs {
		return true
	}
	return false
}
