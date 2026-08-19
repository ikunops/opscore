// Package hostregistry is the Host Registry (Phase 2.3): named, grouped target
// machines so operations can reference them by name ("web-01") instead of a
// full connection spec, and so Batch (Phase 2.5) can fan out to a group.
//
// It lives in controlplane (not core) on purpose: a store/repository is
// control-plane state, exactly like storage.Storage. core stays a pure kernel
// (Context/Dispatcher/Executor/...) and only knows the bare TargetHost
// transport spec carried by Context.Target. This keeps the kernel boundary
// clean (Round6: "Kernel 不知道 HTTP/JWT/数据库") and matches the project's
// convention that stores live in controlplane.
package hostregistry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/YuDong999/opscore/internal/core"
)

// ErrHostNotFound is returned when a named host is not registered.
var ErrHostNotFound = errors.New("host not found")

// Host is a registered, named target machine. It wraps core.TargetHost (the
// bare connection spec consumed by the SSH transport) with registry identity
// and grouping, so operations can reference machines by name instead of pasting
// a full connection spec, and Batch (Phase 2.5) can fan out to a group.
type Host struct {
	Name   string            // unique registry key, e.g. "web-01"
	Target core.TargetHost   // connection spec (reuse the SSH transport type)
	Groups []string          // logical groups for batch fan-out (Phase 2.5)
	Labels map[string]string // optional free-form metadata
}

// ToTarget returns the bare connection spec for use in a core.Context.
func (h Host) ToTarget() core.TargetHost { return h.Target }

// HostStore persists registered hosts. The default implementation is
// MemoryHostStore; a future SQLite/Postgres backend implements the same
// interface without touching callers (Repository pattern, like storage.Storage).
type HostStore interface {
	Save(h Host) (Host, error)
	GetByName(name string) (Host, error)
	List() ([]Host, error)
	ListByGroup(group string) ([]Host, error)
	Delete(name string) error
}

// MemoryHostStore is the embedded default HostStore. Thread-safe; the
// connection specs live only for the process lifetime (persistence is a later
// Phase 2/3 item, exactly as host.go anticipates).
type MemoryHostStore struct {
	mu    sync.RWMutex
	hosts map[string]Host
}

// NewMemoryHostStore builds an empty in-memory host registry.
func NewMemoryHostStore() *MemoryHostStore {
	return &MemoryHostStore{hosts: make(map[string]Host)}
}

func (s *MemoryHostStore) Save(h Host) (Host, error) {
	if h.Name == "" {
		return Host{}, fmt.Errorf("host name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[h.Name] = h
	return h, nil
}

func (s *MemoryHostStore) GetByName(name string) (Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[name]
	if !ok {
		return Host{}, ErrHostNotFound
	}
	return h, nil
}

func (s *MemoryHostStore) List() ([]Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemoryHostStore) ListByGroup(group string) ([]Host, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(all))
	for _, h := range all {
		for _, g := range h.Groups {
			if g == group {
				out = append(out, h)
				break
			}
		}
	}
	return out, nil
}

func (s *MemoryHostStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[name]; !ok {
		return ErrHostNotFound
	}
	delete(s.hosts, name)
	return nil
}

// ResolveTarget resolves a host reference to a connection spec. A ref matching
// a registered host name returns that host's spec; any other string is treated
// as an unknown host and returns ErrHostNotFound. (The API treats a string
// target as a named host; inline objects are handled separately by the caller.)
func ResolveTarget(store HostStore, ref string) (core.TargetHost, error) {
	if store == nil {
		return core.TargetHost{}, fmt.Errorf("host registry not configured")
	}
	h, err := store.GetByName(ref)
	if err != nil {
		return core.TargetHost{}, err
	}
	return h.ToTarget(), nil
}

// ResolveGroup returns the connection specs of all hosts in a group — the
// source for Phase 2.5 Batch fan-out.
func ResolveGroup(store HostStore, group string) ([]core.TargetHost, error) {
	if store == nil {
		return nil, fmt.Errorf("host registry not configured")
	}
	hosts, err := store.ListByGroup(group)
	if err != nil {
		return nil, err
	}
	out := make([]core.TargetHost, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.ToTarget())
	}
	return out, nil
}
