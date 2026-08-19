package runtime

import "testing"

func TestTransition_Valid(t *testing.T) {
	cases := []struct{ from, to LifecycleState }{
		{StateDiscovered, StateLoaded},
		{StateLoaded, StateRegistered},
		{StateRegistered, StateEnabled},
		{StateRegistered, StateUnloaded}, // never enabled -> safe to unload
		{StateEnabled, StateDisabled},
		{StateDisabled, StateEnabled}, // re-enable
		{StateDisabled, StateUnloaded},
	}
	for _, c := range cases {
		if err := Transition(c.from, c.to); err != nil {
			t.Errorf("expected %s->%s legal, got %v", c.from, c.to, err)
		}
	}
}

func TestTransition_Illegal(t *testing.T) {
	cases := []struct{ from, to LifecycleState }{
		{StateDiscovered, StateRegistered}, // skip Loaded
		{StateLoaded, StateEnabled},       // skip Registered
		{StateEnabled, StateUnloaded},     // MUST disable first
		{StateRegistered, StateDisabled},  // must enable first
		{StateUnloaded, StateDiscovered},  // terminal
	}
	for _, c := range cases {
		if err := Transition(c.from, c.to); err == nil {
			t.Errorf("expected %s->%s illegal, got nil", c.from, c.to)
		}
	}
}

func TestTransition_SameState(t *testing.T) {
	if err := Transition(StateEnabled, StateEnabled); err == nil {
		t.Error("expected same-state transition to error")
	}
}

func TestCanTransition(t *testing.T) {
	if CanTransition(StateEnabled, StateUnloaded) {
		t.Error("Enabled->Unloaded must be false")
	}
	if !CanTransition(StateDisabled, StateUnloaded) {
		t.Error("Disabled->Unloaded must be true")
	}
}
