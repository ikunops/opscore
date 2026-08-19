// Package memory provides an in-memory protection.KillPersistence implementation
// used as a test double and for the all-in-memory deployment mode. It is one of
// exactly two KillPersistence implementations in the repo (the other is
// storage/sqlite); R93-③ requires a single persistence owner, and this package
// is the non-durable double.
package memory

import (
	"sync"

	"github.com/YuDong999/opscore/internal/protection"
)

type protectionStore struct {
	mu        sync.Mutex
	killed    map[string]bool
	principal map[string]bool
}

// NewProtectionStore builds an in-memory kill-state backend.
func NewProtectionStore() protection.KillPersistence {
	return &protectionStore{
		killed:    make(map[string]bool),
		principal: make(map[string]bool),
	}
}

func (s *protectionStore) LoadKills() (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]bool, len(s.killed))
	for k, v := range s.killed {
		m[k] = v
	}
	return m, nil
}

func (s *protectionStore) LoadPrincipalKills() (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]bool, len(s.principal))
	for k, v := range s.principal {
		m[k] = v
	}
	return m, nil
}

func (s *protectionStore) SetKilled(capID string, killed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killed[capID] = killed
	return nil
}

func (s *protectionStore) SetPrincipalKilled(hash string, killed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principal[hash] = killed
	return nil
}

func (s *protectionStore) ListKills() ([]protection.KillEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protection.KillEntry, 0, len(s.killed)+len(s.principal))
	for k, v := range s.killed {
		out = append(out, protection.KillEntry{CapabilityID: k, Killed: v})
	}
	for h, v := range s.principal {
		out = append(out, protection.KillEntry{Principal: true, PrincipalHash: h, Killed: v})
	}
	return out, nil
}
