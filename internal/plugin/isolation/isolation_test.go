package isolation

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/ecosystem/sdk"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// ---------------------------------------------------------------------------
// Helper-process harness
//
// The test binary re-executes ITSELF as the plugin helper (the os/exec stdlib
// test pattern). That keeps the suite hermetic and offline: no second binary
// to build, no fixture to ship.
// ---------------------------------------------------------------------------

const helperEnv = "OPSCORE_ISO_HELPER_MODE"

func helperCfg(mode string, tune func(*Config)) Config {
	cfg := Config{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess"},
		Env:  append(os.Environ(), helperEnv+"="+mode),
	}
	if tune != nil {
		tune(&cfg)
	}
	return cfg
}

// TestHelperProcess is not a real test: when the env var is set it becomes the
// plugin helper. os.Exit runs before the testing framework can print anything,
// so stdout carries only protocol frames.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("not running as helper")
	}

	switch mode {
	case "hang":
		// Never answers. The host must kill it (MUST-3).
		time.Sleep(30 * time.Second)
		os.Exit(0)

	case "panic":
		// Dies violently mid-request. The host must survive (MUST-4).
		panic("plugin exploded on purpose")

	case "garbage":
		os.Stdout.WriteString("this is not a protocol frame\n")
		os.Exit(0)

	case "silent":
		os.Exit(3) // exits without answering
	}

	handlers := map[string]core.Handler{
		"plugin.demo.echo": core.HandlerFunc(
			func(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
				// Echo back what crossed the boundary so the host can assert on
				// the projection (including what was REDACTED).
				target := ctx.Target()
				return &core.ExecutionPlan{
					OperationName: "plugin.demo.echo",
					Permission:    core.Permission{ResourceType: "db", Action: "list"},
					Risk:          core.RiskMedium,
					Timeout:       7 * time.Second,
					Steps: []core.ExecutionStep{
						&core.CommandStep{
							Name:       "echo-user",
							ID:         "s1",
							Index:      0,
							Executable: "/bin/echo",
							Args:       []string{ctx.User().ID, target.Address, target.Password},
							Env:        map[string]string{"K": "V"},
							WorkingDir: "/tmp",
							Timeout:    3 * time.Second,
						},
					},
				}, nil
			}),
		"plugin.demo.fail": core.HandlerFunc(
			func(core.Context, map[string]any) (*core.ExecutionPlan, error) {
				return nil, errors.New("upstream database unreachable")
			}),
		"plugin.demo.badstep": core.HandlerFunc(
			func(core.Context, map[string]any) (*core.ExecutionPlan, error) {
				return &core.ExecutionPlan{
					OperationName: "plugin.demo.badstep",
					Steps:         []core.ExecutionStep{&opaqueStep{}},
				}, nil
			}),
		"plugin.demo.observe": core.HandlerFunc(
			func(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
				// Echo back what the HOST projected, so the host-side test can
				// assert the projection crossed AND that the helper did NOT
				// substitute its own machine's observations.
				capHostID := ""
				if cs := ctx.CapabilitySnapshot(); cs != nil {
					capHostID = cs.HostID
				}
				hostSnapID := ""
				if hs := ctx.HostSnapshot(); hs != nil {
					hostSnapID = hs.ID
				}
				return &core.ExecutionPlan{
					OperationName: "plugin.demo.observe",
					Steps: []core.ExecutionStep{
						&core.CommandStep{
							Name:       "observe",
							ID:         "o1",
							Index:      0,
							Executable: "/bin/echo",
							// [0]=capability snapshot HostID (host's, not helper's)
							// [1]=host snapshot ID
							// [2]=request id (host ExecutionID)
							Args: []string{capHostID, hostSnapID, ctx.ExecutionID()},
						},
					},
				}, nil
			}),
	}

	if err := Serve(os.Stdin, os.Stdout, handlers); err != nil {
		os.Stderr.WriteString("helper: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

// opaqueStep is a step with no wire form: it must NOT be able to cross.
type opaqueStep struct{}

func (o *opaqueStep) Describe() string                     { return "opaque" }
func (o *opaqueStep) Execute(core.Context) core.StepResult { return core.StepResult{} }

func hostCtx() core.Context {
	return core.NewContext().
		WithUser(core.UserContext{ID: "alice", Name: "Alice", Role: "admin"}).
		WithHost(core.HostContext{Hostname: "cp-01", OS: "linux", Arch: "amd64"}).
		WithTarget(core.TargetHost{
			Address:  "10.0.0.7",
			Port:     22,
			User:     "root",
			Password: "s3cr3t-must-not-cross",
			KeyBytes: []byte("PRIVATE KEY MATERIAL"),
			KeyPath:  "/etc/opscore/id_ed25519",
		}).
		WithCapability(core.CapabilityContext{}).
		Build()
}

// hostCtxWithSnapshots is like hostCtx but with HOST-OBSERVED snapshots
// attached, so the projection has something to carry across the boundary.
func hostCtxWithSnapshots() core.Context {
	capSnap := &snapshot.CapabilitySnapshot{
		HostID:  "cap-host-abc",
		Version: 1,
		Items: map[string]snapshot.CapabilityInfo{
			"systemd": {Name: "systemd", Available: true, Version: "249"},
		},
		Source: snapshot.SourceSSH,
	}
	hostSnap := &snapshot.HostSnapshot{
		ID:       "host-xyz",
		Name:     "cp-01",
		Address:  "10.0.0.7",
		OS:       "linux",
		Arch:     "amd64",
		Platform: "ubuntu",
	}
	return core.NewContext().
		WithUser(core.UserContext{ID: "alice", Name: "Alice", Role: "admin"}).
		WithCapabilitySnapshot(capSnap).
		WithHostSnapshot(hostSnap).
		WithExecutionID("req-1234").
		Build()
}

// ---------------------------------------------------------------------------
// Phase 6.4 Execution Projection
// ---------------------------------------------------------------------------

// TestCapabilitySnapshotProjectedReadOnly proves the helper receives the HOST's
// capability snapshot (read-only) and does NOT substitute its own machine's —
// the defining property of "projection, never detection".
func TestCapabilitySnapshotProjectedReadOnly(t *testing.T) {
	h := NewHandler("plugin.demo.observe", helperCfg("ok", nil))
	plan, err := h.Plan(hostCtxWithSnapshots(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[0] != "cap-host-abc" {
		t.Errorf("helper did not receive the host's capability snapshot: %q", cs.Args[0])
	}
	// The helper must not have auto-detected: its capability equals exactly what
	// the host projected, never a locally-derived value.
	if got := plan.Steps[0].(*core.CommandStep).Args[0]; got != "cap-host-abc" {
		t.Errorf("capability was re-detected instead of projected: %q", got)
	}
}

// TestHostSnapshotProjectedWhenPresent proves the host identity snapshot crosses.
func TestHostSnapshotProjectedWhenPresent(t *testing.T) {
	h := NewHandler("plugin.demo.observe", helperCfg("ok", nil))
	plan, err := h.Plan(hostCtxWithSnapshots(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[1] != "host-xyz" {
		t.Errorf("helper did not receive the host identity snapshot: %q", cs.Args[1])
	}
}

// TestRequestIDProjected proves the host ExecutionID survives the process hop.
func TestRequestIDProjected(t *testing.T) {
	h := NewHandler("plugin.demo.observe", helperCfg("ok", nil))
	plan, err := h.Plan(hostCtxWithSnapshots(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[2] != "req-1234" {
		t.Errorf("request id lost across the boundary: %q", cs.Args[2])
	}
}

// TestCapabilityBlindWhenNotProjected pins the safe default: a host that
// projects no capability snapshot yields a helper that is Capability-blind.
// (The complement — "projection, never detection" — is pinned by
// TestCapabilitySnapshotProjectedReadOnly, which proves the helper receives the
// HOST's snapshot rather than a locally-detected one.)
func TestCapabilityBlindWhenNotProjected(t *testing.T) {
	p := ProjectContext(hostCtx())
	if p.CapabilitySnapshot != nil {
		t.Error("projection must carry no capability snapshot when the host has none")
	}
	ctx := RebuildContext(context.Background(), p)
	if ctx.CapabilitySnapshot() != nil {
		t.Error("helper must stay capability-blind when nothing was projected")
	}
}

// ---------------------------------------------------------------------------
// Happy path + projection
// ---------------------------------------------------------------------------

func TestPlanRoundTripsThroughHelperProcess(t *testing.T) {
	var events []Event
	h := NewHandler("plugin.demo.echo", helperCfg("ok", func(c *Config) {
		c.AuditSink = func(e Event) { events = append(events, e) }
	}))

	plan, err := h.Plan(hostCtx(), map[string]any{"db": "orders"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.OperationName != "plugin.demo.echo" {
		t.Errorf("operation name lost: %q", plan.OperationName)
	}
	if plan.Permission.ResourceType != "db" || plan.Permission.Action != "list" {
		t.Errorf("permission lost: %+v", plan.Permission)
	}
	if plan.Risk != core.RiskMedium {
		t.Errorf("risk lost: %v", plan.Risk)
	}
	if plan.Timeout != 7*time.Second {
		t.Errorf("timeout lost: %v", plan.Timeout)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(plan.Steps))
	}
	cs, ok := plan.Steps[0].(*core.CommandStep)
	if !ok {
		t.Fatalf("step must decode back to *core.CommandStep, got %T", plan.Steps[0])
	}
	if cs.Executable != "/bin/echo" || cs.WorkingDir != "/tmp" || cs.Env["K"] != "V" {
		t.Errorf("step fields lost: %+v", cs)
	}
	if cs.Timeout != 3*time.Second {
		t.Errorf("step timeout lost: %v", cs.Timeout)
	}

	// The projection must have carried identity and target address...
	if cs.Args[0] != "alice" || cs.Args[1] != "10.0.0.7" {
		t.Errorf("context projection lost: %+v", cs.Args)
	}
	if len(events) != 1 || events[0].Code != CodeOK {
		t.Errorf("want one ok event, got %+v", events)
	}
}

// TestCredentialsNeverCrossTheBoundary pins the security property: an isolated
// plugin plans against a host WITHOUT ever receiving the secret used to reach
// it. An in-process handler could read ctx.Target().Password; this one cannot.
func TestCredentialsNeverCrossTheBoundary(t *testing.T) {
	h := NewHandler("plugin.demo.echo", helperCfg("ok", nil))
	plan, err := h.Plan(hostCtx(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if got := cs.Args[2]; got != "" {
		t.Fatalf("SSH password leaked into the helper: %q", got)
	}

	// Belt and braces: the projection itself must not contain the secrets.
	p := ProjectContext(hostCtx())
	blob := p.TargetAddress + p.TargetUser + p.UserID + p.Hostname
	for _, secret := range []string{"s3cr3t-must-not-cross", "PRIVATE KEY MATERIAL", "id_ed25519"} {
		if strings.Contains(blob, secret) {
			t.Errorf("projection leaked %q", secret)
		}
	}
}

// ---------------------------------------------------------------------------
// MUST-3: timeout is REAL termination
// ---------------------------------------------------------------------------

func TestTimeoutKillsTheHelperProcess(t *testing.T) {
	var got Event
	h := NewHandler("plugin.demo.echo", helperCfg("hang", func(c *Config) {
		c.ExecTimeout = 400 * time.Millisecond
		c.AuditSink = func(e Event) { got = e }
	}))

	start := time.Now()
	plan, err := h.Plan(hostCtx(), nil)
	elapsed := time.Since(start)

	if plan != nil {
		t.Error("fail-closed violated: a plan was returned after a timeout")
	}
	if !errors.Is(err, ErrHelperTimeout) {
		t.Fatalf("want ErrHelperTimeout, got %v", err)
	}
	// The helper sleeps 30s. Returning quickly proves we did not merely stop
	// waiting — Wait() returned, which means the process is REAPED, i.e. dead.
	if elapsed > 10*time.Second {
		t.Errorf("helper was not actually killed: took %s", elapsed)
	}
	if got.Code != CodeTimeoutKilled || !got.Killed {
		t.Errorf("audit must report a kill, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// MUST-4: a helper crash must not damage the host
// ---------------------------------------------------------------------------

func TestHelperPanicIsContainedAndHostKeepsWorking(t *testing.T) {
	var got Event
	crashing := NewHandler("plugin.demo.echo", helperCfg("panic", func(c *Config) {
		c.AuditSink = func(e Event) { got = e }
	}))

	plan, err := crashing.Plan(hostCtx(), nil)
	if plan != nil {
		t.Error("fail-closed violated: a plan was returned by a crashed helper")
	}
	if !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("want ErrHelperCrashed, got %v", err)
	}
	if got.Code != CodeHelperCrash {
		t.Errorf("want helper-crash code, got %q", got.Code)
	}
	// The panic trace must survive for diagnosis even though it cannot kill us.
	if !strings.Contains(got.Stderr, "plugin exploded on purpose") {
		t.Errorf("panic trace not captured: %q", truncate(got.Stderr, 200))
	}

	// The whole point of MUST-4: the host is unharmed and keeps serving.
	healthy := NewHandler("plugin.demo.echo", helperCfg("ok", nil))
	if _, err := healthy.Plan(hostCtx(), nil); err != nil {
		t.Fatalf("host degraded after a helper crash: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MUST-5: every failure path is fail-closed
// ---------------------------------------------------------------------------

func TestFailClosedPaths(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		mode    string
		tune    func(*Config)
		wantErr error
		wantHas string
	}{
		{name: "plugin error", op: "plugin.demo.fail", mode: "ok", wantErr: ErrPluginFailed,
			wantHas: "upstream database unreachable"},
		{name: "unknown operation", op: "plugin.demo.nope", mode: "ok", wantErr: ErrPluginFailed,
			wantHas: "unknown operation"},
		{name: "unserializable step", op: "plugin.demo.badstep", mode: "ok", wantErr: ErrPluginFailed,
			wantHas: "not serializable"},
		{name: "garbage on stdout", op: "plugin.demo.echo", mode: "garbage", wantErr: ErrProtocol,
			wantHas: "malformed frame header"},
		{name: "helper exits silently", op: "plugin.demo.echo", mode: "silent", wantErr: ErrHelperCrashed},
		{name: "response over the size cap", op: "plugin.demo.echo", mode: "ok",
			tune:    func(c *Config) { c.MaxResponseBytes = 8 },
			wantErr: ErrProtocol, wantHas: "exceeds the 8 byte limit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(tc.op, helperCfg(tc.mode, tc.tune))
			plan, err := h.Plan(hostCtx(), nil)
			if plan != nil {
				t.Error("fail-closed violated: a plan was returned")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.wantHas != "" && !strings.Contains(err.Error(), tc.wantHas) {
				t.Errorf("error should mention %q, got %q", tc.wantHas, err.Error())
			}
		})
	}
}

func TestSpawnFailureIsReportedNotPanicked(t *testing.T) {
	h := NewHandler("plugin.demo.echo", Config{Path: "/definitely/not/a/binary"})
	plan, err := h.Plan(hostCtx(), nil)
	if plan != nil || err == nil {
		t.Fatalf("want a spawn error and no plan, got plan=%v err=%v", plan, err)
	}
	if !strings.Contains(err.Error(), "cannot start helper") {
		t.Errorf("unhelpful spawn error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Codec unit tests (no process involved)
// ---------------------------------------------------------------------------

func TestEncodePlanRejectsUnknownStepKind(t *testing.T) {
	_, err := EncodePlan(&core.ExecutionPlan{
		OperationName: "x",
		Steps:         []core.ExecutionStep{&opaqueStep{}},
	})
	if err == nil {
		t.Fatal("a step with no wire form must not be encodable")
	}
	if !strings.Contains(err.Error(), "not serializable") {
		t.Errorf("error should explain the boundary, got %v", err)
	}
}

func TestDecodePlanRejectsUnknownStepKind(t *testing.T) {
	_, err := DecodePlan(&sdk.PlanWire{Steps: []sdk.StepWire{{Kind: "docker"}}})
	if err == nil {
		t.Fatal("an unknown step kind must be rejected, not skipped")
	}
}

func TestRebuildContextCarriesNoCredentialsAndNoCapability(t *testing.T) {
	ctx := RebuildContext(context.Background(), ProjectContext(hostCtx()))
	tgt := ctx.Target()
	if tgt.Password != "" || tgt.KeyPath != "" || len(tgt.KeyBytes) != 0 {
		t.Errorf("rebuilt target must be credential-free: %+v", tgt)
	}
	if tgt.Address != "10.0.0.7" || tgt.User != "root" || tgt.Port != 22 {
		t.Errorf("rebuilt target lost routing info: %+v", tgt)
	}
	if ctx.User().ID != "alice" || ctx.Host().Hostname != "cp-01" {
		t.Errorf("rebuilt identity lost: %+v / %+v", ctx.User(), ctx.Host())
	}
	// Capability is host-observed and not projected in v1; the helper must not
	// substitute its own machine's capabilities.
	if ctx.CapabilitySnapshot() != nil {
		t.Error("helper must not auto-detect a capability snapshot")
	}
}

// ---------------------------------------------------------------------------
// MUST-2 enforced mechanically, in BOTH directions
// ---------------------------------------------------------------------------

// TestManagerIsUnawareOfProcessIsolation pins MUST-2: "Manager / Registry /
// Reload / Watcher never learn that a helper process exists."
//
// Direction 1: isolation must not reach into the Manager — it is a peripheral
// decorator, not a runtime controller (the Phase 6.1 lesson, restated).
// Direction 2, the one that actually encodes MUST-2: the runtime must not
// import isolation. If someone later teaches the Manager about helper
// processes, this test goes red instead of the design quietly eroding.
func TestManagerIsUnawareOfProcessIsolation(t *testing.T) {
	const isolationPkg = "internal/plugin/isolation"

	assertNoImport(t, ".", []string{
		"internal/plugin/runtime",
		"internal/builtin",
	})
	assertNoImport(t, "../runtime", []string{isolationPkg})
	assertNoImport(t, "..", []string{isolationPkg})
	// Phase 7.1: the Runtime Core must stay unaware of the Executable Plugin
	// SDK too — the SDK is a peripheral ecosystem package, not part of the
	// frozen Contract surface.
	assertNoImport(t, "../runtime", []string{"ecosystem/sdk"})
	// Phase 7.2: the Runtime Core must also stay unaware of the packaging
	// layer. Packages are a host-side distribution concern bridged only by
	// this isolation package; the Manager never learns a package exists.
	assertNoImport(t, "../runtime", []string{"ecosystem/packaging"})
}

func assertNoImport(t *testing.T, dir string, forbidden []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(p, bad) {
					t.Errorf("%s imports %q — forbidden (MUST-2)", path, p)
				}
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
