package memory

import (
	"sync"

	"github.com/YuDong999/opscore/internal/protection"
)

// quotaStore is the in-memory implementation of protection.QuotaPersistence,
// used as a test double and for all-in-memory deployments. It is one of exactly
// two QuotaPersistence implementations (the other is storage/sqlite); R23-3
// requires a single persistence owner, and this package is the non-durable
// double. It stores definitions only — consumption lives in the evidence
// source and is never held here.
type quotaStore struct {
	mu   sync.Mutex
	defs map[string]protection.QuotaDefinition // key: capability + "\x00" + principal
}

// NewQuotaStore builds an in-memory quota-definition backend.
func NewQuotaStore() protection.QuotaPersistence {
	return &quotaStore{defs: make(map[string]protection.QuotaDefinition)}
}

func (s *quotaStore) LoadDefinitions() ([]protection.QuotaDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protection.QuotaDefinition, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	return out, nil
}

func (s *quotaStore) SaveDefinition(d protection.QuotaDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defs[d.Capability+"\x00"+d.Principal] = d
	return nil
}

func (s *quotaStore) ClearDefinition(capability, principal string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.defs, capability+"\x00"+principal)
	return nil
}
