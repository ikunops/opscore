package protection

import (
	"fmt"
	"sync"
	"time"
)

// KillStore is the SINGLE persistence owner for protection kill-state (R93-③).
// The in-memory maps are only a runtime projection of persistent state.
type KillStore struct {
	mu              sync.RWMutex
	killed          map[string]bool
	principalKilled map[string]bool
	store           KillPersistence
	state           KillStoreState
	clock           func() time.Time
	// opMeta records operator-initiated kill attribution (who/why/when) for the
	// dashboard read surface. It is a runtime projection only — the authoritative
	// kill decision is the `killed` bool (persisted); opMeta is reconstructed from
	// the audit trail after restart. Phase 22.2.
	opMeta map[string]operatorKillMeta
}

// operatorKillMeta is the attribution of an operator-initiated kill (Phase 22.2).
type operatorKillMeta struct {
	Operator string
	Reason   string
	At       time.Time
}

// OperatorKillEntry projects operator-kill attribution for the read surface.
type OperatorKillEntry struct {
	CapabilityID string
	Operator     string
	Reason       string
	At           time.Time
}

// NewKillStore builds an uninitialized kill store around a persistence backend.
func NewKillStore(store KillPersistence, clock func() time.Time) *KillStore {
	if clock == nil {
		clock = time.Now
	}
	return &KillStore{
		store:           store,
		killed:          make(map[string]bool),
		principalKilled: make(map[string]bool),
		state:           KillStateUninitialized,
		clock:           clock,
		opMeta:          make(map[string]operatorKillMeta),
	}
}

// Bootstrap loads persistent kill-state at startup (R93-③). If either load
// fails, state stays Failed (NOT Ready) and IsKilled returns true for ALL
// capabilities — the system refuses to assume "all un-killed" when it cannot
// verify its own protection state.
func (ks *KillStore) Bootstrap() error {
	kills, err := ks.store.LoadKills()
	if err != nil {
		ks.mu.Lock()
		ks.state = KillStateFailed
		ks.mu.Unlock()
		return fmt.Errorf("kill store load kills: %w", err)
	}
	principal, err := ks.store.LoadPrincipalKills()
	if err != nil {
		ks.mu.Lock()
		ks.state = KillStateFailed
		ks.mu.Unlock()
		return fmt.Errorf("kill store load principal kills: %w", err)
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.killed = kills
	ks.principalKilled = principal
	ks.state = KillStateReady
	return nil
}

// State returns the tri-state (R21-13).
func (ks *KillStore) State() KillStoreState {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.state
}

// IsKilled is the fast in-memory gate. If not Ready, returns true (fail closed).
func (ks *KillStore) IsKilled(capID string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.state != KillStateReady {
		return true
	}
	return ks.killed[capID]
}

// IsPrincipalKilled is the by-principal fast gate (fail closed unless Ready).
func (ks *KillStore) IsPrincipalKilled(hash string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.state != KillStateReady {
		return true
	}
	return ks.principalKilled[hash]
}

// SetKilled writes through to persistence, then projects to memory (R93-③:
// unidirectional persistent → memory).
func (ks *KillStore) SetKilled(capID string, killed bool) error {
	if err := ks.store.SetKilled(capID, killed); err != nil {
		return err
	}
	ks.mu.Lock()
	ks.killed[capID] = killed
	ks.mu.Unlock()
	return nil
}

// SetPrincipalKilled writes through to persistence, then projects to memory.
func (ks *KillStore) SetPrincipalKilled(hash string, killed bool) error {
	if err := ks.store.SetPrincipalKilled(hash, killed); err != nil {
		return err
	}
	ks.mu.Lock()
	ks.principalKilled[hash] = killed
	ks.mu.Unlock()
	return nil
}

// List returns the persisted kill entries for the read surface.
func (ks *KillStore) List() ([]KillEntry, error) {
	return ks.store.ListKills()
}

// RecordOperatorKill sets the capability kill flag (single owner, persisted) and
// records operator attribution in opMeta (Phase 22.2). The Gate continues to see
// the kill via IsKilled; opMeta is dashboard-only attribution.
func (ks *KillStore) RecordOperatorKill(capID, operator, reason string) error {
	if err := ks.SetKilled(capID, true); err != nil {
		return err
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.opMeta[capID] = operatorKillMeta{Operator: operator, Reason: reason, At: ks.clock()}
	return nil
}

// ClearOperatorKill removes the operator kill flag (Phase 22.2 P22-4: this does
// NOT restore execution — the Gate re-evaluates on next admission). opMeta is
// dropped; the audit trail retains the who/why permanently.
func (ks *KillStore) ClearOperatorKill(capID string) error {
	if err := ks.SetKilled(capID, false); err != nil {
		return err
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	delete(ks.opMeta, capID)
	return nil
}

// ListOperatorKills returns operator-kill attribution for the read surface.
func (ks *KillStore) ListOperatorKills() []OperatorKillEntry {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]OperatorKillEntry, 0, len(ks.opMeta))
	for capID, m := range ks.opMeta {
		out = append(out, OperatorKillEntry{
			CapabilityID: capID,
			Operator:     m.Operator,
			Reason:       m.Reason,
			At:           m.At,
		})
	}
	return out
}

// Now returns the current time (injectable clock wrapper, used by adapters).
func (ks *KillStore) Now() time.Time {
	return ks.clock()
}
