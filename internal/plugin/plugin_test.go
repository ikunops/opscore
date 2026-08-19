package plugin

import (
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// greetHandler is a stand-in for a plugin's real Go operation handler. It
// proves plugin handlers are ordinary Go code (Operation-as-Code), not YAML.
type greetHandler struct{ name string }

func (h greetHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	// Handlers return an EMPTY Permission/Risk by design: the Dispatcher
	// stamps plan.Permission from the registered operation, and Risk is a
	// property of the declared operation, not something a handler elevates.
	// (The sandbox envelope denies any handler that tries to return a
	// mismatched/escalated permission or higher risk — see sandbox package.)
	return &core.ExecutionPlan{
		OperationName: h.name,
		Steps: []core.ExecutionStep{
			&core.CommandStep{Name: "echo", Executable: "echo", Args: []string{"hi"}, Timeout: 5 * time.Second},
		},
	}, nil
}

// TestModule_RegistersAndRuns proves a plugin Module is indistinguishable from a
// builtin: it registers into the same core.Registry and its handlers run.
func TestModule_RegistersAndRuns(t *testing.T) {
	m := Manifest{
		SchemaVersion: 1,
		Name:          "demo-plugin",
		Version:       "1.0.0",
		Description:   "Phase 3 SDK demonstration plugin",
		Operations: []OperationDecl{
			{Name: "demo.greet", ResourceType: "demo.plugin", Action: "greet", Risk: RiskLow},
			{Name: "demo.farewell", ResourceType: "demo.plugin", Action: "farewell", Risk: RiskMedium},
		},
	}
	handlers := map[string]core.Handler{
		"demo.greet":    greetHandler{"demo.greet"},
		"demo.farewell": greetHandler{"demo.farewell"},
	}

	mod, err := NewModule(m, handlers)
	if err != nil {
		t.Fatal(err)
	}
	if mod.Name() != "demo-plugin" {
		t.Fatalf("module name = %q", mod.Name())
	}

	reg := core.NewRegistry()
	mod.Register(reg) // identical call shape to a builtin module

	for _, name := range []string{"demo.greet", "demo.farewell"} {
		op, ok := reg.Get(name)
		if !ok {
			t.Fatalf("operation %q not registered", name)
		}
		if op.Handler == nil {
			t.Fatalf("operation %q has nil handler", name)
		}
		p, err := op.Handler.Plan(nil, nil) // execute the Go handler
		if err != nil {
			t.Fatalf("plan %q: %v", name, err)
		}
		if len(p.Steps) != 1 {
			t.Fatalf("plan %q steps = %d", name, len(p.Steps))
		}
	}

	// The Control Plane only ever sees metadata.
	if mod.Manifest().Version != "1.0.0" {
		t.Fatalf("manifest version lost: %+v", mod.Manifest())
	}
}

// TestNewModule_MissingHandler enforces the "Manifest is metadata only" rule:
// every declared operation must have a Go handler.
func TestNewModule_MissingHandler(t *testing.T) {
	m := Manifest{
		SchemaVersion: 1,
		Name:          "p",
		Operations:    []OperationDecl{{Name: "x", ResourceType: "r", Action: "a", Risk: RiskLow}},
	}
	if _, err := NewModule(m, nil); err == nil {
		t.Fatal("expected missing-handler error")
	}
}

// TestManifest_Validate covers the structural checks.
func TestManifest_Validate(t *testing.T) {
	if err := (Manifest{}).Validate(); err == nil {
		t.Fatal("expected empty-name error")
	}
	dup := Manifest{Name: "p", Operations: []OperationDecl{
		{Name: "x", ResourceType: "r", Action: "a"},
		{Name: "x", ResourceType: "r", Action: "a"},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("expected duplicate-operation error")
	}
}

// TestParseManifest_JSON proves the manifest is a real, serializable format a
// future runtime loader can read from disk (handlers supplied separately).
func TestParseManifest_JSON(t *testing.T) {
	data := []byte(`{
		"schema_version": 1,
		"name": "jplugin",
		"version": "2.1.0",
		"operations": [{"name": "j.op", "resource_type": "j.res", "action": "do", "risk": "low"}]
	}`)
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "jplugin" || len(m.Operations) != 1 {
		t.Fatalf("parsed manifest = %+v", m)
	}
	if m.Operations[0].Name != "j.op" {
		t.Fatalf("operation name = %q", m.Operations[0].Name)
	}
}

// TestNewModuleFromDescriptor proves the single-bundle entry point (MUST fix,
// GPT review) builds an indistinguishable Module from a ModuleDescriptor.
func TestNewModuleFromDescriptor(t *testing.T) {
	d := ModuleDescriptor{
		Manifest: Manifest{
			SchemaVersion: 1,
			Name:          "desc-plugin",
			Version:       "0.1.0",
			Operations:    []OperationDecl{{Name: "desc.op", ResourceType: "desc", Action: "do", Risk: RiskLow}},
		},
		Handlers: map[string]core.Handler{"desc.op": greetHandler{"desc.op"}},
	}
	mod, err := NewModuleFromDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	if mod.Name() != "desc-plugin" {
		t.Fatalf("module name = %q", mod.Name())
	}
	reg := core.NewRegistry()
	mod.Register(reg)
	if _, ok := reg.Get("desc.op"); !ok {
		t.Fatal("desc.op not registered via descriptor")
	}
}
