// Package builtin defines the contract shared by every in-tree operation
// family. A builtin is a "compile-time plugin": the same Module interface will
// later be implemented by runtime-loaded plugins, so the Dispatcher and
// Control Plane never need to know whether an operation came from the binary
// or from a plugin (Round6: "Builtin = Compile-time Plugin").
package builtin

import "github.com/YuDong999/opscore/internal/core"

// Module is a self-registering family of operations.
//   - Name identifies the family (e.g. "service", "firewall", "journal").
//   - Register wires the module's Operations into the kernel Registry at
//     startup. main aggregates []Module and calls Register on each, so there is
//     never an "if builtin" branch in the kernel.
type Module interface {
	Name() string
	Register(reg *core.Registry)
}

// RegisterAll registers every module into the registry. It is the single
// aggregation point called from main.newCore; adding a builtin is as simple as
// appending it to the slice passed in.
func RegisterAll(reg *core.Registry, modules ...Module) {
	for _, m := range modules {
		m.Register(reg)
	}
}
