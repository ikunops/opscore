package core

import "sync"

// Operation is a registered capability of the system.
// Operations are registered at startup (Operation as Code), never at runtime.
type Operation struct {
	Name       string
	Permission Permission
	Risk       RiskLevel
	Handler    Handler

	// Source is the origin of the operation's registration. Set by the
	// synchronizer when it projects the Registry into Storage (ADR-004 /
	// Phase 3.0): "builtin" | "system" | "plugin:<name>". It lets the
	// Execution record and Audit know WHERE a capability came from without a
	// join back into Storage (MUST-1 of the Phase 3.0 plugin-prep).
	Source string
}

// Registry stores all registered Operations.
// Thread-safe. Read-heavy, write-only-at-startup.
type Registry struct {
	mu  sync.RWMutex
	ops map[string]Operation
}

func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]Operation)}
}

func (r *Registry) Register(op Operation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops[op.Name] = op
}

// Unregister removes an operation from the Registry (used by plugin Unload
// to tear down a plugin's capabilities, Phase 3.1). Safe to call with a
// name that is not registered.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ops, name)
}

func (r *Registry) Get(name string) (Operation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	op, ok := r.ops[name]
	return op, ok
}

func (r *Registry) List() []Operation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Operation, 0, len(r.ops))
	for _, op := range r.ops {
		list = append(list, op)
	}
	return list
}
