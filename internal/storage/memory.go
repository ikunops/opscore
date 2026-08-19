package storage

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("storage: not found")

// ---------------------------------------------------------------------------
// MemoryStorage
// ---------------------------------------------------------------------------

// MemoryStorage is the default, dependency-free Storage implementation.
// It is safe for concurrent use and intended for embedded/test/Phase 1.1 use
// before SQLite is wired in (Phase 1.2).
type MemoryStorage struct {
	op     *memOperationStore
	user   *memUserStore
	role   *memRoleStore
	audit  *memAuditStore
	cfg    *memConfigStore
	task   *memTaskStore
	snap   *memSnapshotStore
	plugin *memPluginStore
}

// NewMemoryStorage builds an empty in-memory store.
func NewMemoryStorage() *MemoryStorage {
	op := &memOperationStore{items: map[string]Operation{}, byID: map[int64]Operation{}}
	role := &memRoleStore{items: map[int64]Role{}, ops: map[int64][]int64{}, opStore: op}
	return &MemoryStorage{
		op:     op,
		user:   &memUserStore{byName: map[string]User{}, byID: map[int64]User{}, roles: map[int64][]int64{}, roleStore: role},
		role:   role,
		audit:  &memAuditStore{items: []AuditEvent{}},
		cfg:    &memConfigStore{items: map[string]string{}},
		task:   &memTaskStore{tasks: map[int64]Task{}, steps: map[int64][]TaskStep{}},
		snap:   &memSnapshotStore{byID: map[int64]CapabilitySnapshot{}, byHash: map[string]int64{}},
		plugin: &memPluginStore{items: map[string]Plugin{}},
	}
}

func (m *MemoryStorage) Operations() OperationStore         { return m.op }
func (m *MemoryStorage) Users() UserStore                   { return m.user }
func (m *MemoryStorage) Roles() RoleStore                   { return m.role }
func (m *MemoryStorage) Audit() AuditStore                  { return m.audit }
func (m *MemoryStorage) Config() ConfigStore                { return m.cfg }
func (m *MemoryStorage) Tasks() TaskStore                   { return m.task }
func (m *MemoryStorage) Snapshots() CapabilitySnapshotStore { return m.snap }
func (m *MemoryStorage) Plugins() PluginStore               { return m.plugin }
func (m *MemoryStorage) Close() error                       { return nil }

// ---------------------------------------------------------------------------
// PluginStore (Phase 3.0 / MUST-3)
// ---------------------------------------------------------------------------

type memPluginStore struct {
	mu    sync.RWMutex
	items map[string]Plugin
}

func (s *memPluginStore) Upsert(p Plugin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[p.ID] = p
	return nil
}

func (s *memPluginStore) Get(id string) (Plugin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[id]
	if !ok {
		return Plugin{}, ErrNotFound
	}
	return p, nil
}

func (s *memPluginStore) List() ([]Plugin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Plugin, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	return out, nil
}

func (s *memPluginStore) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	p.Enabled = enabled
	s.items[id] = p
	return nil
}

func (s *memPluginStore) SetStatus(id string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	p.Status = status
	s.items[id] = p
	return nil
}

// ---------------------------------------------------------------------------
// CapabilitySnapshotStore (content-addressed by hash)
// ---------------------------------------------------------------------------

type memSnapshotStore struct {
	mu     sync.RWMutex
	byID   map[int64]CapabilitySnapshot
	byHash map[string]int64
	next   atomic.Int64
}

func (s *memSnapshotStore) Put(hash string, payload []byte, schemaVersion int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byHash[hash]; ok {
		return id, nil // idempotent on hash
	}
	id := s.next.Add(1)
	s.byID[id] = CapabilitySnapshot{ID: id, Hash: hash, SchemaVersion: schemaVersion, Payload: payload, CreatedAt: time.Now()}
	s.byHash[hash] = id
	return id, nil
}

func (s *memSnapshotStore) GetByHash(hash string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return nil, ErrNotFound
	}
	return s.byID[id].Payload, nil
}

func (s *memSnapshotStore) GetByID(id int64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sn, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return sn.Payload, nil
}

func (s *memSnapshotStore) IDForHash(hash string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return 0, ErrNotFound
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// OperationStore
// ---------------------------------------------------------------------------

type memOperationStore struct {
	mu    sync.RWMutex
	items map[string]Operation
	byID  map[int64]Operation
	next  atomic.Int64
}

func (s *memOperationStore) Save(op Operation) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[op.Name]; ok {
		op.ID = existing.ID
		s.items[op.Name] = op
		s.byID[op.ID] = op
		return op, nil
	}
	op.ID = s.next.Add(1)
	s.items[op.Name] = op
	s.byID[op.ID] = op
	return op, nil
}

func (s *memOperationStore) GetByName(name string) (Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.items[name]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return op, nil
}

func (s *memOperationStore) GetByID(id int64) (Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.byID[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return op, nil
}

func (s *memOperationStore) List() ([]Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Operation, 0, len(s.items))
	for _, op := range s.items {
		out = append(out, op)
	}
	return out, nil
}

func (s *memOperationStore) ListEnabled() ([]Operation, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, op := range all {
		if op.Enabled {
			out = append(out, op)
		}
	}
	return out, nil
}

func (s *memOperationStore) SetEnabled(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.items[name]
	if !ok {
		return ErrNotFound
	}
	op.Enabled = enabled
	s.items[name] = op
	s.byID[op.ID] = op
	return nil
}

func (s *memOperationStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.items[name]
	if !ok {
		return ErrNotFound
	}
	delete(s.items, name)
	delete(s.byID, op.ID)
	return nil
}

// ---------------------------------------------------------------------------
// UserStore
// ---------------------------------------------------------------------------

type memUserStore struct {
	mu        sync.RWMutex
	byName    map[string]User
	byID      map[int64]User
	roles     map[int64][]int64 // userID -> roleIDs
	roleStore *memRoleStore
	next      atomic.Int64
}

func (s *memUserStore) Save(u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byName[u.Username]; ok {
		u.ID = existing.ID
		s.byName[u.Username] = u
		s.byID[u.ID] = u
		return u, nil
	}
	u.ID = s.next.Add(1)
	s.byName[u.Username] = u
	s.byID[u.ID] = u
	return u, nil
}

func (s *memUserStore) GetByUsername(username string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byName[username]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (s *memUserStore) GetByID(id int64) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (s *memUserStore) List() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.byName))
	for _, u := range s.byName {
		out = append(out, u)
	}
	return out, nil
}

func (s *memUserStore) AddRole(userID, roleID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.roles[userID] {
		if id == roleID {
			return nil
		}
	}
	s.roles[userID] = append(s.roles[userID], roleID)
	return nil
}

func (s *memUserStore) RemoveRole(userID, roleID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.roles[userID]
	out := ids[:0]
	for _, id := range ids {
		if id != roleID {
			out = append(out, id)
		}
	}
	s.roles[userID] = out
	return nil
}

func (s *memUserStore) Roles(userID int64) ([]Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Role, 0, len(s.roles[userID]))
	for _, roleID := range s.roles[userID] {
		if r, ok := s.roleStore.items[roleID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// RoleStore
// ---------------------------------------------------------------------------

type memRoleStore struct {
	mu      sync.RWMutex
	items   map[int64]Role
	ops     map[int64][]int64 // roleID -> opIDs
	next    atomic.Int64
	opStore *memOperationStore
}

func (s *memRoleStore) Save(r Role) (Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// upsert by name so re-sync does not duplicate roles
	for id, existing := range s.items {
		if existing.Name == r.Name {
			r.ID = id
			s.items[id] = r
			return r, nil
		}
	}
	if r.ID == 0 {
		r.ID = s.next.Add(1)
	}
	s.items[r.ID] = r
	return r, nil
}

func (s *memRoleStore) GetByName(name string) (Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.items {
		if r.Name == name {
			return r, nil
		}
	}
	return Role{}, ErrNotFound
}

func (s *memRoleStore) GetByID(id int64) (Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.items[id]
	if !ok {
		return Role{}, ErrNotFound
	}
	return r, nil
}

func (s *memRoleStore) List() ([]Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Role, 0, len(s.items))
	for _, r := range s.items {
		out = append(out, r)
	}
	return out, nil
}

func (s *memRoleStore) AddOperation(roleID, opID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.ops[roleID] {
		if id == opID {
			return nil // idempotent
		}
	}
	s.ops[roleID] = append(s.ops[roleID], opID)
	return nil
}

func (s *memRoleStore) RemoveOperation(roleID, opID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.ops[roleID]
	out := ids[:0]
	for _, id := range ids {
		if id != opID {
			out = append(out, id)
		}
	}
	s.ops[roleID] = out
	return nil
}

func (s *memRoleStore) Operations(roleID int64) ([]Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Operation, 0, len(s.ops[roleID]))
	for _, opID := range s.ops[roleID] {
		if op, ok := s.opStore.byID[opID]; ok {
			out = append(out, op)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

type memAuditStore struct {
	mu    sync.RWMutex
	items []AuditEvent
	next  atomic.Int64
}

func (s *memAuditStore) Append(e AuditEvent) (AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = s.next.Add(1)
	s.items = append(s.items, e)
	return e, nil
}

func (s *memAuditStore) List(limit int) ([]AuditEvent, error) {
	return s.slice(limit, "")
}

func (s *memAuditStore) ListByOperation(op string, limit int) ([]AuditEvent, error) {
	return s.slice(limit, op)
}

// ListByCorrelation is the Phase 17.3 additive read view (ADR-038 §3.1).
// Newest-first, matching the SQLite implementation's ordering.
func (s *memAuditStore) ListByCorrelation(correlationID string, limit int) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AuditEvent, 0, limit)
	for i := len(s.items) - 1; i >= 0; i-- {
		if s.items[i].CorrelationID == correlationID {
			out = append(out, s.items[i])
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// Query is the Phase 18 evidence read (ADR-040 §3.1). It applies the identical
// predicate / limit / truncation rules as the SQLite store so the two are
// behaviourally interchangeable — asserted by TestAuditStoreConformance, which
// runs one query matrix against both.
//
// The walk is newest-first (matching ORDER BY id DESC) and stops one row AFTER
// the limit is reached: that extra match is not returned, it is the truncation
// probe. It is the in-memory equivalent of the SQL `LIMIT n+1`.
func (s *memAuditStore) Query(q AuditQuery) (AuditPage, error) {
	limit := EffectiveAuditLimit(q.Limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Non-nil so an empty page marshals as [] — "found nothing" must not be
	// expressible as `null`, which reads like "did not look".
	out := make([]AuditEvent, 0, limit)
	truncated := false
	for i := len(s.items) - 1; i >= 0; i-- {
		e := s.items[i]
		// Phase 19 cursor (ADR-042 §3.2): id < After is the contract; After==0
		// is the wildcard, so omitting it changes nothing. Applied before the
		// predicate so the cursor is a hard boundary, not a filter on the page.
		if q.After != 0 && e.ID >= q.After {
			continue
		}
		if !q.Matches(e) {
			continue
		}
		if len(out) == limit {
			// One match beyond the window exists: report the window, do not
			// silently widen or silently drop it.
			truncated = true
			break
		}
		out = append(out, e)
	}
	return AuditPage{Events: out, Limit: limit, Truncated: truncated}, nil
}

func (s *memAuditStore) slice(limit int, onlyOp string) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AuditEvent, 0, limit)
	// newest first
	for i := len(s.items) - 1; i >= 0; i-- {
		e := s.items[i]
		if onlyOp != "" && e.Operation != onlyOp {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// ConfigStore
// ---------------------------------------------------------------------------

type memConfigStore struct {
	mu    sync.RWMutex
	items map[string]string
}

func (s *memConfigStore) Get(key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[key]
	return v, ok, nil
}

func (s *memConfigStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = value
	return nil
}

func (s *memConfigStore) List() ([]Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Config, 0, len(s.items))
	for k, v := range s.items {
		out = append(out, Config{Key: k, Value: v})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// TaskStore
// ---------------------------------------------------------------------------

type memTaskStore struct {
	mu     sync.RWMutex
	tasks  map[int64]Task
	steps  map[int64][]TaskStep
	next   atomic.Int64
	stepID atomic.Int64
}

func (s *memTaskStore) Save(t Task) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == 0 {
		t.ID = s.next.Add(1)
	}
	s.tasks[t.ID] = t
	return t, nil
}

func (s *memTaskStore) GetByID(id int64) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (s *memTaskStore) AppendStep(step TaskStep) (TaskStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step.ID = s.stepID.Add(1)
	s.steps[step.TaskID] = append(s.steps[step.TaskID], step)
	return step, nil
}

func (s *memTaskStore) UpdateStep(id int64, status, output string, durationMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, steps := range s.steps {
		for i := range steps {
			if steps[i].ID == id {
				steps[i].Status = status
				steps[i].Output = output
				steps[i].DurationMs = durationMs
				return nil
			}
		}
	}
	return ErrNotFound
}

func (s *memTaskStore) Steps(taskID int64) ([]TaskStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TaskStep, len(s.steps[taskID]))
	copy(out, s.steps[taskID])
	return out, nil
}
