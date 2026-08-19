// Package runtime defines the OpsCore Plugin Runtime Contract (Phase 3.1).
//
// Contract, not implementation: it specifies the surfaces a plugin integration
// MUST satisfy (Manifest/Descriptor separation, Loader abstraction, Lifecycle
// state machine, Module/Handler contract) WITHOUT any .so loading, hot-reload,
// or real plugin discovery. See ADR-010 (Plugin Runtime Contract & Isolation
// Boundary) for the rationale and the GPT Round 6/7 sign-off.
package runtime

import "fmt"

// LifecycleState is a plugin's position in the frozen state machine
// (GPT Round 6/7):
//
//	Discovered -> Loaded -> Registered -> Enabled -> Disabled -> Unloaded
//
// The ONLY legal predecessor of Unloaded is Disabled or Registered — never
// Enabled. This prevents yanking a GRANTED capability (Enabled means its ops
// are authorized) out from under a running Execution.
type LifecycleState string

const (
	StateDiscovered LifecycleState = "discovered"
	StateLoaded     LifecycleState = "loaded"
	StateRegistered LifecycleState = "registered"
	StateEnabled    LifecycleState = "enabled"
	StateDisabled   LifecycleState = "disabled"
	StateUnloaded   LifecycleState = "unloaded"
)

// validTransitions encodes the frozen state machine. A transition is legal iff
// target ∈ validTransitions[from]. The empty slice marks a terminal state.
var validTransitions = map[LifecycleState][]LifecycleState{
	StateDiscovered: {StateLoaded},
	StateLoaded:     {StateRegistered},
	StateRegistered: {StateEnabled, StateUnloaded}, // never enabled -> safe to unload
	StateEnabled:    {StateDisabled},               // Unload FORBIDDEN from Enabled
	StateDisabled:   {StateEnabled, StateUnloaded}, // re-enable or unload
	StateUnloaded:   {},                           // terminal
}

// CanTransition reports whether moving from->to is legal under the state machine.
func CanTransition(from, to LifecycleState) bool {
	for _, t := range validTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Transition is like CanTransition but returns a descriptive error so callers
// can surface "cannot unload an enabled plugin; disable it first".
func Transition(from, to LifecycleState) error {
	if from == to {
		return fmt.Errorf("plugin lifecycle: already in state %q", from)
	}
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("plugin lifecycle: illegal transition %q -> %q (must disable before unload)",
		from, to)
}
