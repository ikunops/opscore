package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/management"
)

// mgmtConfig returns a config whose stores live under t.TempDir(), so each test
// gets an isolated policy store and audit log.
func mgmtConfig(t *testing.T, token string) HarnessConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PolicyStoreDir = filepath.Join(dir, "policies")
	cfg.AuditStorePath = filepath.Join(dir, "audit.db")
	cfg.ManagementToken = token
	return cfg
}

// TestManagementNotAssembledWithoutToken is MUST-P17-14 stated as an observable
// fact rather than an intention.
//
// The assertion is about ABSENCE, which is why it checks the harness's own view
// (no server object, no address) instead of poking at a port: a port test would
// pass just as happily against a listener that answers 401 to everything, and
// that is precisely the outcome §3.1 rejects.
func TestManagementNotAssembledWithoutToken(t *testing.T) {
	h, err := Build(context.Background(), mgmtConfig(t, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	if h.ManagementEnabled() {
		t.Fatal("management surface assembled with no token configured (MUST-P17-14)")
	}
	if got := h.ManagementAddr(); got != "" {
		t.Errorf("ManagementAddr = %q, want empty — there is no listener to address", got)
	}
	if h.mgmt != nil || h.auditDB != nil {
		t.Errorf("mgmt=%v auditDB=%v, want both nil", h.mgmt != nil, h.auditDB != nil)
	}
	if n := len(h.boundServers()); n != 2 {
		t.Errorf("boundServers = %d, want 2 (external + probe) — a third would be listening", n)
	}
}

// TestManagementAssembledWithToken covers the positive case and, more usefully,
// checks that the assembled surface actually enforces the token rather than
// merely existing.
func TestManagementAssembledWithToken(t *testing.T) {
	cfg := mgmtConfig(t, "s3cret")
	cfg.ManagementAddr = ":19082"
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	if !h.ManagementEnabled() {
		t.Fatal("management surface not assembled despite a configured token")
	}
	if got := h.ManagementAddr(); got != ":19082" {
		t.Errorf("ManagementAddr = %q, want :19082", got)
	}
	if n := len(h.boundServers()); n != 3 {
		t.Errorf("boundServers = %d, want 3 (external + probe + management)", n)
	}

	ts := httptest.NewServer(h.mgmt.Handler)
	defer ts.Close()

	body := strings.NewReader(`{"policy_id":"pol-wired","rules":[]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+management.RoutePrefix+"policies", body)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("unauthenticated post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token status = %d, want 401 — the wiring bound the surface "+
			"without its authenticator", resp.StatusCode)
	}
}

// TestManagementWriteIsVisibleToExternalRead is the wiring test that actually
// matters.
//
// Everything else here can pass while the two surfaces are quietly talking to
// two different repositories — each self-consistent, jointly useless. This one
// writes through management/v1 and reads back through external/v1, so it fails
// if the composition root ever hands out a second policy store.
func TestManagementWriteIsVisibleToExternalRead(t *testing.T) {
	cfg := mgmtConfig(t, "s3cret")
	h, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	mgmtSrv := httptest.NewServer(h.mgmt.Handler)
	defer mgmtSrv.Close()
	readSrv := httptest.NewServer(h.http.Handler)
	defer readSrv.Close()

	payload := `{"policy_id":"pol-x","rules":[{"rule_id":"r1","priority":7,"kind":"change-freeze"}]}`
	req, _ := http.NewRequest(http.MethodPost, mgmtSrv.URL+management.RoutePrefix+"policies",
		strings.NewReader(payload))
	req.Header.Set(management.HeaderToken, "s3cret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := mgmtSrv.Client().Do(req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}

	readResp, err := readSrv.Client().Get(readSrv.URL + "/external/v1/policy/pol-x")
	if err != nil {
		t.Fatalf("external read: %v", err)
	}
	defer readResp.Body.Close()
	if readResp.StatusCode != http.StatusOK {
		t.Fatalf("external read status = %d, want 200 — the write did not reach the "+
			"store the read surface projects from", readResp.StatusCode)
	}
	var view map[string]interface{}
	if err := json.NewDecoder(readResp.Body).Decode(&view); err != nil {
		t.Fatalf("decode external view: %v", err)
	}
	if !strings.Contains(strings.ToLower(jsonString(t, view)), "r1") {
		t.Errorf("external view does not mention the rule written through management: %v", view)
	}
}

func jsonString(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestTokenIsUnrepresentableInConfigFile pins the design decision that the
// credential has no config key at all.
//
// The value of the test is the FAILURE MODE it locks in: putting a token in the
// JSON is a hard parse error, not a lint warning or a convention. If someone
// later adds a `token` field "for convenience", this test is what tells them
// they are re-opening the path where secrets get committed to a repo.
func TestTokenIsUnrepresentableInConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": "1",
	  "management": {"listen": ":8082", "token": "hunter2"}
	}`), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("config file with a management token parsed successfully; " +
			"the credential must have no representable key")
	}
}

// TestLoadConfigTakesTokenFromEnvironment covers the supported path, including
// the read-only default when the variable is unset.
func TestLoadConfigTakesTokenFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": "1",
	  "management": {"listen": ":9082", "principal": "ops-bot"}
	}`), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	t.Setenv(EnvManagementToken, "from-env")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ManagementToken != "from-env" {
		t.Errorf("token = %q, want from-env", cfg.ManagementToken)
	}
	if cfg.ManagementAddr != ":9082" || cfg.ManagementPrincipal != "ops-bot" {
		t.Errorf("addr=%q principal=%q", cfg.ManagementAddr, cfg.ManagementPrincipal)
	}
	if !managementEnabled(cfg) {
		t.Error("managementEnabled = false despite a token in the environment")
	}

	t.Setenv(EnvManagementToken, "")
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load without token: %v", err)
	}
	if managementEnabled(cfg2) {
		t.Error("managementEnabled = true with an empty environment token")
	}
}

// TestWhitespaceTokenIsTreatedAsAbsent guards a real deployment failure mode:
// an env var set to "" or " " by a templating system that "helpfully" always
// defines the key. A blank string is not a secret, and treating it as one would
// bind the write surface behind a token nobody can present but everybody can
// guess the shape of.
func TestWhitespaceTokenIsTreatedAsAbsent(t *testing.T) {
	h, err := Build(context.Background(), mgmtConfig(t, "   "))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()
	if h.ManagementEnabled() {
		t.Fatal("whitespace-only token assembled the write surface")
	}
}

// TestManagementRequestedButAuditStoreUnavailableFailsBuild pins the asymmetry
// in assembleManagement: silence is acceptable when nothing was asked for, and
// unacceptable when something was.
//
// The alternative — boot anyway with the surface quietly dropped — is the worst
// available outcome: the operator believes writes are audited, the deployment
// reports healthy, and the discovery happens during an incident.
func TestManagementRequestedButAuditStoreUnavailableFailsBuild(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	cfg := DefaultConfig()
	cfg.PolicyStoreDir = filepath.Join(dir, "policies")
	cfg.ManagementToken = "s3cret"
	// The parent path component is a regular file, so the audit store cannot be
	// created under it on any platform.
	cfg.AuditStorePath = filepath.Join(blocker, "audit.db")

	if _, err := Build(context.Background(), cfg); err == nil {
		t.Fatal("Build succeeded with an unusable audit store; a write surface " +
			"without durable audit violates MUST-P17-13")
	} else if !strings.Contains(err.Error(), "audit store") {
		t.Errorf("error %q does not name the audit store — the operator cannot act on it", err)
	}
}

// TestExternalMuxDoesNotRouteManagementPaths is the §3.6 isolation check from the
// consumer side: the read router, exactly as production builds it, must have no
// route for any management path.
func TestExternalMuxDoesNotRouteManagementPaths(t *testing.T) {
	h, err := Build(context.Background(), mgmtConfig(t, "s3cret"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	ts := httptest.NewServer(h.http.Handler)
	defer ts.Close()

	for _, pattern := range management.RoutePatterns() {
		path := pattern
		if i := strings.IndexByte(path, ' '); i >= 0 {
			path = path[i+1:]
		}
		path = strings.ReplaceAll(path, "{id}", "probe")

		req, _ := http.NewRequest(http.MethodPost, ts.URL+path,
			strings.NewReader(`{"policy_id":"probe","rules":[]}`))
		req.Header.Set(management.HeaderToken, "s3cret")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("probe %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("external/v1 answered %d for %s, want 404 — the write surface "+
				"leaked onto the read bind", resp.StatusCode, path)
		}
	}
}

// TestIsolationAssertionActuallyFires proves assertNoManagementRoutes is load-
// bearing rather than decorative.
//
// A guard that has never been observed to fail is indistinguishable from a
// no-op, so we hand it a mux that deliberately swallows the management
// namespace — the realistic mistake, since a prefix route catches paths it never
// names — and require the panic.
func TestIsolationAssertionActuallyFires(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("assertNoManagementRoutes did not panic for a mux that routes " +
				"the management namespace")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "ADR-036") {
			t.Errorf("panic %q does not cite the rule being violated", r)
		}
	}()

	leaky := http.NewServeMux()
	leaky.HandleFunc(management.RoutePrefix, func(http.ResponseWriter, *http.Request) {})
	assertNoManagementRoutes(leaky)
}

// TestShutdownClosesManagementAndAuditStore checks the teardown ordering encoded
// in Shutdown: the listener drains before the audit database closes, so no
// in-flight write can find its audit store gone.
func TestShutdownClosesManagementAndAuditStore(t *testing.T) {
	h, err := Build(context.Background(), mgmtConfig(t, "s3cret"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// Idempotence (SHOULD-8) must survive the extra closable resource: a second
	// Shutdown must not double-close the DB.
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if _, err := h.auditDB.Audit().List(1); err == nil {
		t.Error("audit store still usable after Shutdown; it was not closed")
	}
}
