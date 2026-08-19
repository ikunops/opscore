package execution

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory, thread-safe implementation of Store.
// Suitable for tests, the CLI, and the default serve mode until a durable
// backend (SQLite/Postgres) is wired in a later sub-phase. It keeps the
// Recorder/Store contract identical so swapping the backend is a one-liner.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*ExecutionRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]*ExecutionRecord)}
}

func (s *MemoryStore) Create(rec ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	// A record created already in RUNNING state starts now.
	if rec.Status == StatusRunning && rec.StartedAt == nil {
		st := rec.CreatedAt
		rec.StartedAt = &st
	}
	s.items[rec.ID] = &rec
	return nil
}

func (s *MemoryStore) UpdateStatus(id string, status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	rec.Status = status
	rec.Version++
	now := time.Now()
	switch status {
	case StatusRunning:
		if rec.StartedAt == nil {
			rec.StartedAt = &now
		}
	case StatusSuccess, StatusFailed, StatusCancelled:
		if rec.FinishedAt == nil {
			rec.FinishedAt = &now
		}
	}
	return nil
}

// Transition is the atomic CAS variant of UpdateStatus: it applies 'to'
// only when the record is currently in 'from'. A mismatch (a concurrent
// transition already moved it) returns ErrConflict instead of clobbering.
func (s *MemoryStore) Transition(id string, from, to Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	if rec.Status != from {
		return ErrConflict
	}
	rec.Status = to
	rec.Version++
	now := time.Now()
	switch to {
	case StatusRunning:
		if rec.StartedAt == nil {
			rec.StartedAt = &now
		}
	case StatusSuccess, StatusFailed, StatusCancelled:
		if rec.FinishedAt == nil {
			rec.FinishedAt = &now
		}
	}
	return nil
}

// UpdateStep upserts a step by ID — repeated writes (e.g. live status
// updates) replace the prior record instead of appending duplicates.
func (s *MemoryStore) UpdateStep(id string, step ExecutionStepRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	for i := range rec.Steps {
		if rec.Steps[i].ID == step.ID {
			rec.Steps[i] = step
			return nil
		}
	}
	rec.Steps = append(rec.Steps, step)
	return nil
}

func (s *MemoryStore) Get(id string) (*ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (s *MemoryStore) List(q Query) ([]ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExecutionRecord, 0, len(s.items))
	for _, rec := range s.items {
		if q.Operation != "" && rec.Operation != q.Operation {
			continue
		}
		if q.Status != "" && rec.Status != q.Status {
			continue
		}
		out = append(out, *rec)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
