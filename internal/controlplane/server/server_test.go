package server

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/YuDong999/opscore/internal/builtin/service"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
	"log/slog"

	"golang.org/x/crypto/ssh"
)

// newTestServer wires a full Core + Sync + Server with a bootstrapped admin.
// It returns the Server and its Storage (so tests can seed/inspect state).
func newTestServer(t *testing.T) (*Server, storage.Storage) {
	t.Helper()
	stor := storage.NewMemoryStorage()

	// Core
	registry := core.NewRegistry()
	registry.Register(core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "system.service", Action: "restart"},
		Risk:       core.RiskMedium,
		Handler:    service.NewRestartHandler(),
	})
	auditSink := core.NewLogSink(slog.Default())
	executor := core.NewExecutor(auditSink)
	dispatcher := core.NewDispatcher(registry, executor)

	// Sync metadata + admin role
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	srv, err := New(Config{
		Storage:       stor,
		Dispatcher:    dispatcher,
		AccessSecret:  "access-secret",
		RefreshSecret: "refresh-secret",
		// Existing auth/rbac tests exercise self-registration, so keep it on
		// here; the default-off behavior is covered by server_auth_test.go.
		AllowRegister:  true,
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, stor
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServer_AuthnAuthzFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// health
	rec := doJSON(t, h, "GET", "/api/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}

	// login as admin
	rec = doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", rec.Code, rec.Body.String())
	}
	var tp tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if tp.AccessToken == "" || tp.RefreshToken == "" {
		t.Fatal("missing tokens")
	}

	// /me
	rec = doJSON(t, h, "GET", "/api/me", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status = %d", rec.Code)
	}
	var me struct {
		Username string   `json:"username"`
		Roles    []string `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Username != "admin" || len(me.Roles) == 0 || me.Roles[0] != "admin" {
		t.Fatalf("me unexpected: %+v", me)
	}

	// list operations -> admin allowed
	rec = doJSON(t, h, "GET", "/api/operations", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list struct {
		Operations []struct {
			Name    string `json:"name"`
			Allowed bool   `json:"allowed"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Operations) != 1 || !list.Operations[0].Allowed {
		t.Fatalf("expected admin allowed on op: %+v", list.Operations)
	}

	// plan (authorized) -> reaches the dispatcher. In this test environment the
	// restart handler's capability check fails (no systemctl on the host), which
	// is orthogonal to authn/authz. We assert the request was NOT blocked by
	// auth (401/403/404) and reached the Kernel.
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/plan", tp.AccessToken, map[string]any{"name": "nginx"})
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
		t.Fatalf("plan should be authorized; got %d body=%s", rec.Code, rec.Body.String())
	}

	// unauthenticated plan -> 401
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/plan", "", map[string]any{"name": "nginx"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestServer_UnprivilegedForbidden(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// register an unprivileged user
	rec := doJSON(t, h, "POST", "/api/auth/register", "", credentials{"bob", "bobpw"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d", rec.Code)
	}
	// login
	rec = doJSON(t, h, "POST", "/api/auth/login", "", credentials{"bob", "bobpw"})
	if rec.Code != http.StatusOK {
		t.Fatalf("bob login status = %d", rec.Code)
	}
	var tp tokenPair
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)

	// bob may list but NOT plan (no roles)
	rec = doJSON(t, h, "GET", "/api/operations", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	// plan must be 403 (forbidden)
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/plan", tp.AccessToken, map[string]any{"name": "nginx"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unprivileged, got %d body=%s", rec.Code, rec.Body.String())
	}

	// unknown operation -> 404
	rec = doJSON(t, h, "POST", "/api/operations/nope.op/plan", tp.AccessToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown op, got %d", rec.Code)
	}
}

func TestServer_Refresh(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	var tp tokenPair
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)

	rec = doJSON(t, h, "POST", "/api/auth/refresh", "", map[string]any{"refresh_token": tp.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", rec.Code, rec.Body.String())
	}
	var tp2 tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp2); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if tp2.AccessToken == "" {
		t.Fatal("missing new access token")
	}
	// new access token works on /me
	rec = doJSON(t, h, "GET", "/api/me", tp2.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me with refreshed token status = %d", rec.Code)
	}
}

func TestServer_AuditEndpoint(t *testing.T) {
	srv, stor := newTestServer(t)
	h := srv.Handler()

	// seed an audit event directly (in real use the Executor emits it)
	if _, err := stor.Audit().Append(storage.AuditEvent{
		Actor:     "admin",
		Operation: "system.service.restart",
		Action:    "execute",
		Target:    "nginx",
		Result:    "success",
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	// admin can read audit
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	var tp tokenPair
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)
	rec = doJSON(t, h, "GET", "/api/audit", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(body.Events) != 1 || body.Events[0]["actor"] != "admin" {
		t.Fatalf("unexpected audit events: %+v", body.Events)
	}

	// bob (unprivileged) is forbidden from reading audit
	rec = doJSON(t, h, "POST", "/api/auth/register", "", credentials{"bob", "bobpw"})
	rec = doJSON(t, h, "POST", "/api/auth/login", "", credentials{"bob", "bobpw"})
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)
	rec = doJSON(t, h, "GET", "/api/audit", tp.AccessToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for bob, got %d", rec.Code)
	}

	// unauthenticated -> 401
	rec = doJSON(t, h, "GET", "/api/audit", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// startLocalSSH starts a minimal in-process SSH server (password "testpw") on a
// random loopback port. Used to exercise the remote-execution wiring without a
// real host. It runs commands via `sh -c`, so missing binaries (e.g. systemctl)
// surface as non-zero exit — which is exactly what proves the SSH path was used.
func startLocalSSH(t *testing.T) (string, func()) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == "testpw" {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("bad")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "session" {
						_ = nc.Reject(ssh.UnknownChannelType, "x")
						continue
					}
					ch, reqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range reqs {
							if req.Type == "exec" {
								cmd := string(req.Payload[4:])
								out, rerr := exec.Command("sh", "-c", cmd).CombinedOutput()
								code := byte(0)
								if rerr != nil {
									code = 1
								}
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, code})
								_ = req.Reply(true, nil)
								_, _ = ch.Write(out)
								_ = ch.Close()
							} else {
								_ = req.Reply(true, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// TestServer_RemoteTarget_Wiring proves the full HTTP -> RBAC -> Context(target)
// -> Dispatcher -> CommandStep -> SSH chain is wired: with a remote target the
// handler's LOCAL capability gate is skipped (plan succeeds), and run actually
// reaches the SSH server (fails there, not with a local-gate error).
func TestServer_RemoteTarget_Wiring(t *testing.T) {
	addr, stop := startLocalSSH(t)
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	target := core.TargetHost{
		Address:               host,
		Port:                  port,
		User:                  "tester",
		Password:              "testpw",
		InsecureIgnoreHostKey: true,
	}

	stor := storage.NewMemoryStorage()
	registry := core.NewRegistry()
	registry.Register(core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "system.service", Action: "restart"},
		Risk:       core.RiskMedium,
		Handler:    service.NewRestartHandler(),
	})
	dispatcher := core.NewDispatcher(registry, core.NewExecutor(core.NewLogSink(slog.Default())))
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		AccessSecret:   "a",
		RefreshSecret:  "r",
		DefaultTarget:  target,
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h := srv.Handler()

	// login admin
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	var tp tokenPair
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)

	// plan with remote target: local capability gate must be skipped -> 200 + steps
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/plan", tp.AccessToken, map[string]any{"name": "nginx"})
	if rec.Code != http.StatusOK {
		t.Fatalf("remote plan status = %d body=%s", rec.Code, rec.Body.String())
	}
	var plan struct {
		Steps []string `json:"steps"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	if len(plan.Steps) == 0 {
		t.Fatalf("expected steps in remote plan, got %+v", plan)
	}

	// run with remote target: reaches SSH server (no systemctl there) -> 500,
	// but NOT 401/403/404 and NOT the local "systemctl not found on this host".
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/run", tp.AccessToken, map[string]any{"name": "nginx", "target": map[string]any{
		"address": host, "port": port, "user": "tester", "password": "testpw", "insecure": true,
	}})
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
		t.Fatalf("remote run should be authorized; got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("remote run expected 500 (ssh command fails), got %d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("systemctl not found on this host")) {
		t.Fatalf("remote run incorrectly hit the LOCAL capability gate: %s", rec.Body.String())
	}
}
