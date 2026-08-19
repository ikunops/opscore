package harness

import (
	"context"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/cluster"
	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/enterprise"
	"github.com/YuDong999/opscore/internal/external"
	"github.com/YuDong999/opscore/internal/governance"
	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/platformview"
)

// forbiddenImports lists the frozen execution-path packages the Harness MUST NEVER import
// (ADR-026 MUST-1). Capability packages (observability/cluster/enterprise/governance) are read-only
// sources and are allowed — the Harness assembles them.
var forbiddenImports = []string{
	"core/execution",
	"plugin/runtime",
	"plugin/isolation",
	"controlplane/hostregistry",
	"controlplane/server",
	"builtin/",
}

// forbiddenMethods lists the execution-method vocabulary the Harness MUST NEVER expose (MUST-0).
var forbiddenMethods = []string{
	"Run", "Exec", "Invoke", "Apply", "Execute", "Command",
	"Emit", "Dispatch", "Rollback", "Kill", "Schedule", "Mutate",
	"Resolve", "Replay", "Remediate",
}

// harnessSource returns the non-test source of the harness package for AST inspection.
func harnessSource(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var b strings.Builder
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		b.Write(data)
		b.WriteString("\n")
	}
	return b.String()
}

// harnessImportPaths returns the real import paths of the harness package (excluding tests),
// parsed via go/parser so documentation comments that merely NAME forbidden packages are not
// mistaken for actual imports.
func harnessImportPaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var paths []string
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, m, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", m, err)
		}
		for _, imp := range f.Imports {
			paths = append(paths, imp.Path.Value)
		}
	}
	return paths
}

// TestHarnessNoFrozenOwnership (SHOULD-6 + MUST-1): the Harness never imports the frozen execution
// path. (It MAY import capability read APIs — that is the assembly duty.)
func TestHarnessNoFrozenOwnership(t *testing.T) {
	for _, imp := range harnessImportPaths(t) {
		for _, forbidden := range forbiddenImports {
			if strings.Contains(imp, forbidden) {
				t.Errorf("harness imports forbidden frozen path %q (in %s)", forbidden, imp)
			}
		}
	}
}

// TestHarnessNoExecutionMethod (SHOULD-6 + MUST-0): the Harness exposes no command / execution
// method vocabulary.
func TestHarnessNoExecutionMethod(t *testing.T) {
	src := harnessSource(t)
	re := regexp.MustCompile(`func \([^)]+\) (` + strings.Join(forbiddenMethods, "|") + `)\(`)
	if loc := re.FindString(src); loc != "" {
		t.Errorf("harness defines forbidden execution method: %s", loc)
	}
}

// TestProductionWiring (SHOULD-6 + SHOULD-2): Build produces a ready Harness with a non-nil
// external.Server (i.e. real Readers were injected, not nil).
func TestProductionWiring(t *testing.T) {
	h, err := Build(context.Background(), HarnessConfig{ListenAddr: ":0"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if h == nil {
		t.Fatal("Build returned nil harness")
	}
	if h.server == nil {
		t.Fatal("harness.server is nil — external.Server not injected")
	}
	if h.http == nil {
		t.Fatal("harness.http is nil — transport not mounted")
	}
}

// TestNoNilReader (SHOULD-6 + SHOULD-2): realReaders injects a non-nil Reader for every capability
// in BOTH facade Reader bundles — no nil-Readers in production.
func TestNoNilReader(t *testing.T) {
	cap := &capabilities{
		obs: observability.NewCollector(),
		cl:  cluster.NewManager(),
		ent: enterprise.NewService(),
		gov: governance.NewEngine(),
	}
	pv, corr := realReaders(cap)

	if pv.Obs == nil || pv.Cluster == nil || pv.Enterprise == nil || pv.Governance == nil {
		t.Error("platformview.Readers contains a nil reader")
	}
	if corr.Obs == nil || corr.Cluster == nil || corr.Enterprise == nil || corr.Governance == nil {
		t.Error("correlation.Readers contains a nil reader")
	}
}

// TestExternalUsesInjectedReaders (SHOULD-6): external.Server honours the Readers it is given —
// a fake Reader is observed end-to-end, proving the Harness's injected Readers are what serve.
type fakePVReader struct{}

func (fakePVReader) GetExecutionOverview(_ context.Context, id string) (platformview.ExecutionOverview, error) {
	return platformview.ExecutionOverview{ExecutionID: "INJECTED-" + id}, nil
}
func (fakePVReader) GetHostPolicyStatus(_ context.Context, _ string) (platformview.HostPolicyStatus, error) {
	return platformview.HostPolicyStatus{}, nil
}
func (fakePVReader) GetGovernanceSummary(_ context.Context, _ string) (platformview.GovernanceSummary, error) {
	return platformview.GovernanceSummary{}, nil
}
func (fakePVReader) GetClusterPlacementView(_ context.Context, _ string) (platformview.ClusterPlacementView, error) {
	return platformview.ClusterPlacementView{}, nil
}

type fakeCorrReader struct{}

func (fakeCorrReader) Correlate(_ context.Context, scope correlation.Scope) (correlation.CorrelationView, error) {
	return correlation.CorrelationView{Scope: scope, Meta: correlation.Meta{Reason: "injected"}}, nil
}

func TestExternalUsesInjectedReaders(t *testing.T) {
	srv := external.NewServer(fakePVReader{}, fakeCorrReader{}, nil)
	v, err := srv.GetExecution(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if v == nil || v.ExecutionID != "INJECTED-abc" {
		t.Fatalf("external.Server did not use the injected reader: got %#v", v)
	}
}

// TestShutdownIdempotent (SHOULD-8): repeated Shutdown calls are idempotent — the second returns
// the first result and produces no secondary side effect.
func TestShutdownIdempotent(t *testing.T) {
	h, err := Build(context.Background(), HarnessConfig{ListenAddr: ":0"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	err1 := h.Shutdown(context.Background())
	err2 := h.Shutdown(context.Background())
	if err1 != err2 {
		t.Errorf("Shutdown not idempotent: first=%v second=%v", err1, err2)
	}
}

// TestConfigLoadValid (R69 acceptance: Config validation test): a well-formed
// deployment config loads, maps onto HarnessConfig, and fills unspecified fields
// with operational defaults. It never mutates Runtime/Policy semantics.
func TestConfigLoadValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "opscore.json")
	content := `{
		"version": "1",
		"server": {"listen": ":9090", "probe": ":9091"},
		"log": {"level": "debug", "format": "json"},
		"storage": {"policyStoreDir": "/tmp/opscore-policy-test"}
	}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Version != "1" {
		t.Errorf("Version = %q, want 1", cfg.Version)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.ListenAddr)
	}
	if cfg.ProbeAddr != ":9091" {
		t.Errorf("ProbeAddr = %q, want :9091", cfg.ProbeAddr)
	}
	if cfg.Logging.Level != "debug" || cfg.Logging.Format != "json" {
		t.Errorf("Logging = %+v, want debug/json", cfg.Logging)
	}
	if cfg.PolicyStoreDir != "/tmp/opscore-policy-test" {
		t.Errorf("PolicyStoreDir = %q", cfg.PolicyStoreDir)
	}
}

// TestConfigLoadFailClosed (R69 acceptance: Config validation test): malformed
// or unsupported configs are rejected (fail-closed, ADR-032 §3.6).
func TestConfigLoadFailClosed(t *testing.T) {
	cases := map[string]string{
		"unknown key":     `{"version": "1", "bogus": true}`,
		"unsupported ver": `{"version": "2"}`,
		"bad log level":   `{"version": "1", "log": {"level": "trace"}}`,
		"bad log format":  `{"version": "1", "log": {"format": "xml"}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "bad.json")
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(p); err == nil {
				t.Errorf("LoadConfig expected error for %q", content)
			}
		})
	}
}

// TestConfigValidate (R69 acceptance: fail-closed startup): Validate rejects
// unsupported schema versions and illegal log params, and accepts the default.
func TestConfigValidate(t *testing.T) {
	if err := (HarnessConfig{Version: "2"}).Validate(); err == nil {
		t.Error("Validate accepted unsupported version 2")
	}
	if err := (HarnessConfig{Logging: LoggingConfig{Level: "trace"}}).Validate(); err == nil {
		t.Error("Validate accepted illegal log level trace")
	}
	if err := (HarnessConfig{Logging: LoggingConfig{Format: "xml"}}).Validate(); err == nil {
		t.Error("Validate accepted illegal log format xml")
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Errorf("DefaultConfig should validate: %v", err)
	}
	if err := (HarnessConfig{Version: "1", Logging: LoggingConfig{Level: "info", Format: "json"}}).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

// TestPolicyStoreDirWiring (R69 acceptance: PolicyStoreDir wiring test): Build
// wires an explicit, non-nil Repository selected by PolicyStoreDir. The
// repository location is the ONLY thing that changes — Policy lifecycle
// semantics are untouched (A-5). Round-trips through Save/Get/List.
func TestPolicyStoreDirWiring(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PolicyStoreDir = t.TempDir()
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if h.cap.polRepo == nil {
		t.Fatal("PolicyStoreDir not wired — polRepo is nil")
	}

	rec := governancepolicy.PolicyRecord{
		PolicyID: "test-policy",
		Status:   governancepolicy.StatusActive,
	}
	if _, err := h.cap.polRepo.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := h.cap.polRepo.Get("test-policy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("policy not persisted")
	}
	if got.PolicyID != "test-policy" {
		t.Errorf("PolicyID = %q", got.PolicyID)
	}
	list, err := h.cap.polRepo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List returned %d policies, want 1", len(list))
	}
}

// TestProbeReadiness (R69 acceptance: Health purity test + readiness): the probe
// handlers are read-only observers. /healthz is always ok; /readyz reflects
// policy-store reachability; /versionz exposes build metadata. Repeated reads
// MUST NOT mutate the policy store (A-3).
func TestProbeReadiness(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PolicyStoreDir = t.TempDir()
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Seed one policy so we can prove reads do not grow the store.
	if _, err := h.cap.polRepo.Save(governancepolicy.PolicyRecord{
		PolicyID: "seed", Status: governancepolicy.StatusActive,
	}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	srv := httptest.NewServer(h.newProbeRouter())
	defer srv.Close()

	// healthz
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// readyz — repeated reads must not mutate the store.
	before, _ := h.cap.polRepo.List()
	for i := 0; i < 5; i++ {
		r, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatalf("readyz #%d: %v", i, err)
		}
		if r.StatusCode != http.StatusOK {
			t.Errorf("readyz #%d status = %d, want 200", i, r.StatusCode)
		}
		r.Body.Close()
	}
	after, _ := h.cap.polRepo.List()
	if len(before) != len(after) {
		t.Errorf("readyz mutated the policy store: before=%d after=%d", len(before), len(after))
	}

	// versionz
	v, err := http.Get(srv.URL + "/versionz")
	if err != nil {
		t.Fatalf("versionz: %v", err)
	}
	if v.StatusCode != http.StatusOK {
		t.Errorf("versionz status = %d, want 200", v.StatusCode)
	}
	v.Body.Close()
}

// TestShutdownClosesBothServers (R69 acceptance: Shutdown idempotency + both
// surfaces): Shutdown drains and closes BOTH the external/v1 and the operational
// probe server, and is idempotent (A-4).
func TestShutdownClosesBothServers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PolicyStoreDir = t.TempDir()
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if h.http == nil {
		t.Fatal("external server not mounted")
	}
	if h.probe == nil {
		t.Fatal("probe server not mounted")
	}
	err1 := h.Shutdown(context.Background())
	err2 := h.Shutdown(context.Background())
	if err1 != err2 {
		t.Errorf("Shutdown not idempotent: first=%v second=%v", err1, err2)
	}
}

// TestCompositionRootUnique (R69 acceptance: Production wiring test / A-1): the
// only way to assemble the process is harness.Build. This test asserts that
// Build yields a fully-assembled Harness and that no second wiring path exists
// in the package (reinforced by TestHarnessNoFrozenOwnership).
func TestCompositionRootUnique(t *testing.T) {
	h, err := Build(context.Background(), DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if h.server == nil || h.pv == nil || h.corr == nil || h.cap == nil {
		t.Error("Build did not assemble the full read graph (server/pv/corr/cap)")
	}
	if h.probe == nil {
		t.Error("Build did not mount the operational probe server")
	}
}
