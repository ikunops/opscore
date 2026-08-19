// Package sync bridges the runtime Operation Registry (the source of truth for
// capabilities, per ADR-004) and the durable Storage projection.
//
// ADR-004: Code Owns Capability, Database Owns Assignment.
//   - The Registry declares what operations exist and their metadata.
//   - Storage persists that metadata + the authorization graph (roles→ops).
//   - This synchronizer keeps the two consistent at startup (and on plugin load).
package sync

import (
	"fmt"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin"
	"github.com/YuDong999/opscore/internal/storage"
)

// DefaultAdminRole is the bootstrap role granted every synced operation.
const DefaultAdminRole = "admin"

// systemOps are control-plane operations (not kernel operation handlers).
// They gate the Execution API (Phase 2.1.4) so the API surface has its
// own RBAC graph rather than riding on each kernel operation's permission.
// Registered with Source="system" and Enabled=true; the admin role is
// granted them by bootstrapAdmin so the bootstrap admin can drive the API.
func systemOps() []storage.Operation {
	mk := func(name string) storage.Operation {
		return storage.Operation{
			Name:         name,
			ResourceType: "execution",
			ActionType:   name, // execution.create / read / cancel / list
			Risk:         "low",
			Source:       "system",
			Enabled:      true,
		}
	}
	return []storage.Operation{
		mk("execution.create"),
		mk("execution.read"),
		mk("execution.cancel"),
		mk("execution.list"),
	}
}

// MetadataSynchronizer projects the Operation Registry into Storage.
type MetadataSynchronizer struct {
	reg  *core.Registry
	stor storage.Storage
}

// New builds a synchronizer for the given registry and storage.
func New(reg *core.Registry, stor storage.Storage) *MetadataSynchronizer {
	return &MetadataSynchronizer{reg: reg, stor: stor}
}

// Sync upserts all builtin Operations into Storage (Source="builtin",
// Enabled=true), the control-plane execution.* ops (Source="system"), and
// bootstraps the default admin role granting all of them. Idempotent:
// re-running is safe.
func (s *MetadataSynchronizer) Sync() error {
	all := make([]storage.Operation, 0, len(s.reg.List())+4)
	for _, op := range s.reg.List() {
		// Stamp the registration origin back onto the in-memory Operation
		// (Phase 3.0 / MUST-1) so the Dispatcher can carry it into the
		// ExecutionPlan and the Executor into the ExecutionRecord, letting
		// Audit know WHERE a capability came from without a Storage join.
		// Guard: do NOT clobber a plugin op's Source ("plugin:<name>")
		// if one happens to be in the Registry during a re-Sync (the
		// plugin loader owns those, not this bootstrap pass).
		if op.Source == "" || op.Source == "builtin" {
			op.Source = "builtin"
		}
		s.reg.Register(op)
		so, err := s.syncOne("builtin", storage.Operation{
			Name:         op.Name,
			ResourceType: op.Permission.ResourceType,
			ActionType:   op.Permission.Action,
			Risk:         op.Risk.String(),
		})
		if err != nil {
			return err
		}
		all = append(all, so)
	}
	for _, so := range systemOps() {
		so, err := s.syncOne("system", so)
		if err != nil {
			return err
		}
		all = append(all, so)
	}
	return s.bootstrapAdmin(all)
}

// SyncPlugin upserts a set of Operations owned by a plugin (Source="plugin:<name>").
// Used by the Plugin Manager in Phase 3. Every op is validated against the
// plugin namespace contract (MUST-2 of the Phase 3.0 prep) BEFORE it is
// projected into Storage, so a malicious/buggy plugin can NEVER register a
// system-namespaced permission (e.g. execution.create) and pollute RBAC.
func (s *MetadataSynchronizer) SyncPlugin(pluginName string, ops []core.Operation) error {
	for _, op := range ops {
		if err := plugin.ValidateOperation(op); err != nil {
			return fmt.Errorf("plugin %q: %w", pluginName, err)
		}
		// Stamp the registration origin back onto the in-memory Operation so
		// the Dispatcher can carry it into the ExecutionPlan (MUST-1).
		op.Source = "plugin:" + pluginName
		s.reg.Register(op)
		_, err := s.syncOne("plugin:"+pluginName, storage.Operation{
			Name:         op.Name,
			ResourceType: op.Permission.ResourceType,
			ActionType:   op.Permission.Action,
			Risk:         op.Risk.String(),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// syncOne persists one operation (forcing Source + Enabled) and returns the
// stored row so its assigned id is available for role-granting.
func (s *MetadataSynchronizer) syncOne(source string, op storage.Operation) (storage.Operation, error) {
	op.Source = source
	op.Enabled = true
	if _, err := s.stor.Operations().Save(op); err != nil {
		return storage.Operation{}, err
	}
	// Re-read to capture the assigned id for role-granting.
	stored, err := s.stor.Operations().GetByName(op.Name)
	if err != nil {
		return storage.Operation{}, err
	}
	return stored, nil
}

func (s *MetadataSynchronizer) bootstrapAdmin(ops []storage.Operation) error {
	role, err := s.stor.Roles().Save(storage.Role{
		Name:        DefaultAdminRole,
		Description: "superuser — granted every synced operation",
	})
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := s.stor.Roles().AddOperation(role.ID, op.ID); err != nil {
			return err
		}
	}
	return nil
}
