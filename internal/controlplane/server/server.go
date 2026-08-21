// Package server exposes OpsCore's Control Plane over HTTP (Phase 1.5).
//
// It is a thin transport layer: it authenticates the caller (Bearer access
// token), authorizes the requested operation against the RBAC graph, builds a
// core.Context carrying the resolved identity, and delegates to the Kernel
// Dispatcher. It never contains business logic or capability checks — those
// live in core (Phase 0) and auth (Phase 1.4) respectively.
//
// Routes:
//
//	POST /api/auth/login            {username,password} -> {access,refresh}
//	POST /api/auth/refresh          {refresh}           -> {access,refresh}
//	POST /api/auth/register         {username,password} -> unprivileged user (disabled by default; enable with AllowRegister)
//	GET  /api/health                -> {status:"ok"}
//	GET  /api/me                    -> current user + roles
//	GET  /api/operations            -> registered ops (with allowed flag)
//	POST /api/operations/{name}/plan -> dry-run (authn+authz)
//	POST /api/operations/{name}/run   -> execute  (authn+authz)
//	POST /api/batch                   -> fan one op out to a host group (Phase 2.5)
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/controlplane/hostregistry"
	"github.com/YuDong999/opscore/internal/controlplane/inventory"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	corecap "github.com/YuDong999/opscore/internal/core/capability"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
)

// uiHTML is the embedded single-page Control Plane UI (Phase 4 preview).
//
//go:embed ui.html
var uiHTML string

// uiConfig is injected into the served UI so the SPA knows the server's default
// target (and whether demo mode is on) without a round-trip.
type uiConfig struct {
	Demo          bool      `json:"demo"`
	DefaultTarget *uiTarget `json:"defaultTarget"`
}

// uiTarget is the snake_case projection of core.TargetHost the UI form expects.
// Kept separate from core.TargetHost so the UI's field names stay independent of
// the kernel's struct (which is not otherwise JSON-serialized).
type uiTarget struct {
	Address               string `json:"address"`
	Port                  int    `json:"port"`
	User                  string `json:"user"`
	Password              string `json:"password"`
	InsecureIgnoreHostKey bool   `json:"insecure_ignore_host_key"`
}

func toUITarget(t core.TargetHost) *uiTarget {
	return &uiTarget{
		Address:               t.Address,
		Port:                  t.Port,
		User:                  t.User,
		Password:              t.Password,
		InsecureIgnoreHostKey: t.InsecureIgnoreHostKey,
	}
}

// Config configures the Control Plane HTTP server.
type Config struct {
	Storage    storage.Storage
	Dispatcher *core.Dispatcher
	// Runtime, when set, is used to execute operations so that runs are
	// recorded (Execution API) and cancellable by id. When nil, the server
	// falls back to the Dispatcher's synchronous Execute (no execution id /
	// cancellation). Serve mode always provides a Runtime.
	Runtime *core.Runtime
	// Gate, when set, installs the Phase 21 Operational Protection layer
	// (ADR-048) in front of every execution path: kill switch, circuit breaker,
	// concurrency cap, rate limit, and cooperative timeout — evaluated in a
	// fixed order AFTER authn+authz. Nil disables protection so test servers and
	// minimal deployments can run ungated; Serve mode always provides a Gate.
	Gate *protection.Gate
	// AlertTracker holds the Phase 24.2 declarative alert state (R24-3:
	// Alerting Declarative — the server computes + exposes only, never
	// transports or triggers). Nil disables the /alerts read surface.
	AlertTracker *protection.AlertTracker
	// AlertPolicy is the declarative alert threshold config (Phase 24.2).
	AlertPolicy protection.AlertPolicy
	// HostStore, when set, enables the Host Registry: operations may reference
	// a named host ("target": "web-01") instead of a full inline connection
	// spec, and /api/hosts CRUD manages the registry. Nil disables both.
	HostStore     hostregistry.HostStore
	AccessSecret  string
	RefreshSecret string
	Logger        *slog.Logger
	// DefaultTarget, if set, is the host every operation executes against over
	// SSH. A per-request "target" in the body overrides it. Empty => local.
	DefaultTarget core.TargetHost
	// UseSudo, when true, runs remote commands via `sudo -n`. Set it when the
	// managed host's SSH user is unprivileged. Never combine with DemoMode
	// (the in-process fake host does not emulate sudo).
	UseSudo bool
	// BootstrapAdmin, if set, creates (or, if present, re-roles) an initial
	// admin user granted the synced "admin" role. Intended for first-run setup;
	// Phase 4 will replace open registration with admin-gated provisioning.
	BootstrapAdmin *BootstrapAdmin
	// DemoMode, when true, signals the UI that DefaultTarget points at an
	// in-process fake host, so it can show a "演示模式" banner.
	DemoMode bool
	// AllowRegister, when false (default), makes POST /api/auth/register
	// return 403. Open self-registration is a needless attack surface; it
	// must be explicitly opted into. First-run admin is provisioned via
	// BootstrapAdmin instead (Phase 4 will replace this with admin-gated
	// provisioning + approval).
	AllowRegister bool
}

// BootstrapAdmin names the first-run administrator account.
type BootstrapAdmin struct {
	Username string
	Password string
}

// Server is the HTTP Control Plane.
type Server struct {
	stor          storage.Storage
	auth          *auth.AuthService
	dispatcher    *core.Dispatcher
	runtime       *core.Runtime
	hosts         hostregistry.HostStore
	logger        *slog.Logger
	defaultTarget core.TargetHost
	demoMode      bool
	useSudo       bool
	allowRegister bool
	// bus is the in-process execution.EventBus fan-out. The
	// ExecutionService publishes lifecycle events onto it; the WS hub
	// subscribes and streams them to UI clients (Phase 2.1.4 / Round 3).
	bus *ExecutionEventBus
	// wsHub upgrades /api/executions/stream and forwards bus
	// events to connected WebSocket clients.
	wsHub *WSHub
	// gate is the optional Phase 21 Operational Protection entry point.
	gate *protection.Gate
	// alertTracker holds the Phase 24.2 declarative alert state.
	alertTracker *protection.AlertTracker
	// alertPolicy is the Phase 24.2 declarative alert threshold config.
	alertPolicy protection.AlertPolicy
}

// New builds a Server and, if configured, bootstraps the admin user.
func New(cfg Config) (*Server, error) {
	if cfg.Storage == nil {
		return nil, errors.New("server: storage required")
	}
	if cfg.Dispatcher == nil {
		return nil, errors.New("server: dispatcher required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Wire the execution.EventBus fan-out (Phase 2.1.4 / Round 3):
	// the ExecutionService publishes lifecycle events onto it, and the
	// WebSocket hub subscribes to stream them to UI clients. The
	// bus is wired into the Runtime's Service so every async run
	// emits onto it without the kernel knowing about any transport.
	bus := NewExecutionEventBus()
	if cfg.Runtime != nil {
		cfg.Runtime.Service().SetBus(bus)
	}
	hub := NewWSHub(bus)
	s := &Server{
		stor:          cfg.Storage,
		auth:          auth.NewAuthService(cfg.Storage, cfg.AccessSecret, cfg.RefreshSecret),
		dispatcher:    cfg.Dispatcher,
		runtime:       cfg.Runtime,
		hosts:         cfg.HostStore,
		logger:        logger,
		defaultTarget: cfg.DefaultTarget,
		demoMode:      cfg.DemoMode,
		useSudo:       cfg.UseSudo,
		allowRegister: cfg.AllowRegister,
		bus:           bus,
		wsHub:         hub,
		gate:          cfg.Gate,
		alertTracker:  cfg.AlertTracker,
		alertPolicy:   cfg.AlertPolicy,
	}
	if cfg.BootstrapAdmin != nil {
		if err := s.bootstrapAdmin(cfg.BootstrapAdmin); err != nil {
			return nil, fmt.Errorf("server: bootstrap admin: %w", err)
		}
		logger.Info("control-plane admin bootstrapped", "username", cfg.BootstrapAdmin.Username)
	}
	return s, nil
}

// bootstrapAdmin creates the user (idempotent) and grants the synced admin role.
func (s *Server) bootstrapAdmin(cfg *BootstrapAdmin) error {
	u, err := s.auth.Register(cfg.Username, cfg.Password)
	if err != nil {
		if !errors.Is(err, auth.ErrUserExists) {
			return err
		}
		u, err = s.stor.Users().GetByUsername(cfg.Username)
		if err != nil {
			return err
		}
	}
	role, err := s.stor.Roles().GetByName(sync.DefaultAdminRole)
	if err != nil {
		return fmt.Errorf("admin role not synced (run synchronizer first): %w", err)
	}
	return s.stor.Users().AddRole(u.ID, role.ID)
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/operations", s.handleListOps)
	mux.HandleFunc("POST /api/operations/{name}/plan", s.handlePlan)
	mux.HandleFunc("POST /api/operations/{name}/run", s.handleRun)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	// Host Inventory (Phase 2.7 closure): the unified consumer of HostSnapshot
	// + CapabilitySnapshot. /api/inventory reads the current target (after
	// enrichment); /api/inventory/{name} resolves a named host from the registry
	// and probes it. RBAC-gated by system.host.capability.list (same graph as
	// /api/capabilities — no second authorization path).
	mux.HandleFunc("GET /api/inventory", s.handleInventory)
	mux.HandleFunc("GET /api/inventory/{name}", s.handleInventory)
	// Capability snapshot read-back (Phase 2.8 closure): resolve a content-
	// addressed CapabilitySnapshot by its hash so an audit viewer can reconstruct
	// the exact capability set in effect at execution time. Gated by the same
	// RBAC permission as /api/capabilities — no second authorization path.
	mux.HandleFunc("GET /api/snapshots/{hash}", s.handleGetSnapshot)
	// Execution API (Phase 2.1.4): introspect runs + cancel in-flight ones.
	// RBAC: admin sees all executions; a non-admin user sees (and can cancel)
	// only their own. Cancellation is only meaningful while RUNNING.
	//
	//   POST /api/executions            -> async create (202 + id), background run
	//   GET  /api/executions            -> list (own unless execution.list)
	//   GET  /api/executions/{id}       -> get one (execution.read + ownership)
	//   GET  /api/executions/{id}/timeline -> lifecycle timeline projection
	//   POST /api/executions/{id}/cancel -> request cancel (execution.cancel + ownership)
	//   GET  /api/executions/stream   -> WebSocket stream of execution.* events
	mux.HandleFunc("POST /api/executions", s.handleCreateExecution)
	mux.HandleFunc("GET /api/executions", s.handleListExecutions)
	mux.HandleFunc("GET /api/executions/{id}", s.handleGetExecution)
	mux.HandleFunc("GET /api/executions/{id}/timeline", s.handleGetExecutionTimeline)
	mux.HandleFunc("POST /api/executions/{id}/cancel", s.handleCancelExecution)
	mux.HandleFunc("GET /api/executions/stream", s.handleExecutionsStream)
	// Host Registry (Phase 2.3): named targets, admin-gated, secret-redacted.
	mux.HandleFunc("GET /api/hosts", s.handleListHosts)
	mux.HandleFunc("POST /api/hosts", s.handleCreateHost)
	mux.HandleFunc("GET /api/hosts/{name}", s.handleGetHost)
	mux.HandleFunc("DELETE /api/hosts/{name}", s.handleDeleteHost)
	// Batch Execution (Phase 2.5): fan a single operation out to a host group
	// and/or an explicit list of named hosts.
	mux.HandleFunc("POST /api/batch", s.handleBatch)
	// Phase 21 Operational Protection read surface: REMOVED from the :8080
	// execution mux. Per R21-1 (ADR-046/048) it is served on :8082 by
	// Server.ProtectionReadMux(), bound by the Control Plane composition root
	// (cmd/opscore) so the in-memory counters are REAL — updated by the same
	// process that performs the execution interception. Hanging it on the
	// harness :8082 read-surface (which has no gate) would report always-zero
	// "fake green" counters — a silent monitoring blind spot.
	mux.HandleFunc("GET /", s.handleConsole)
	return mux
}

// renderUI injects the server's default target + demo flag into the embedded
// HTML and returns the served page. The placeholder token is replaced together
// with its fallback `{}` so the injected JSON becomes a single valid assignment
// — a lone trailing `{}` after the JSON would be a syntax error and kill the
// whole <script> (the Phase 1.9.1 regression). Kept as a pure function so the
// exact injection contract is covered by a deterministic unit test rather than
// a flaky HTTP smoke test.
func renderUI(demo bool, target core.TargetHost) string {
	cfg := uiConfig{Demo: demo}
	if !target.IsZero() {
		cfg.DefaultTarget = toUITarget(target)
	}
	js, err := json.Marshal(cfg)
	if err != nil {
		js = []byte("{}")
	}
	return strings.Replace(uiHTML, "/*__OPSCORE_UI_CONFIG__*/{}", string(js), 1)
}

// handleConsole serves the embedded Control Plane UI (a single-page app).
// It injects the server's default target + demo flag into the HTML so the UI
// can pre-fill the target form and show a demo banner without an extra call.
func (s *Server) handleConsole(w http.ResponseWriter, _ *http.Request) {
	html := renderUI(s.demoMode, s.defaultTarget)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

// ---------------------------------------------------------------------------
// Auth helpers
// ---------------------------------------------------------------------------

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) subject(r *http.Request) (string, error) {
	hdr := r.Header.Get("Authorization")
	if strings.HasPrefix(hdr, "Bearer ") {
		return s.auth.Authenticate(strings.TrimPrefix(hdr, "Bearer "))
	}
	// P22-9 fallback: the httpOnly + SameSite=Strict session cookie set by
	// POST /management/v1/login (or /api/auth/login). The SPA path uses this so
	// the token never lives in JS. Bearer remains the API-client contract.
	if ck, err := r.Cookie(sessionCookieName); err == nil && ck.Value != "" {
		return s.auth.Authenticate(ck.Value)
	}
	return "", errors.New("missing bearer token or session cookie")
}

func (s *Server) coreCtx(r *http.Request, username string, target core.TargetHost, parent context.Context) core.Context {
	if parent == nil {
		parent = r.Context()
	}
	ctx := core.NewContext().
		WithUser(core.UserContext{ID: username, Name: username}).
		WithTarget(target).
		WithLogger(s.logger).
		WithStdContext(parent).
		Build()
	// Phase 2.6: attach the observed capability/host snapshot for remote
	// targets (best-effort SSH probe, cached). The Resolver then consumes it
	// instead of guessing the dominant Linux tool.
	ctx = core.EnrichContextForTarget(ctx, target)
	// Phase 2.8 closure: content-address the observed capability snapshot into
	// the CapabilitySnapshotStore so the CapabilityHash frozen into the
	// ExecutionRecord/AuditEvent resolves to a real payload (weak reference,
	// ADR-009). Idempotent on hash, so re-enriching the same host reuses the row.
	persistCapabilitySnapshot(ctx, s.stor, s.logger)
	return ctx
}

// persistCapabilitySnapshot is the control-plane integration point for the
// Phase 2.8 weak-reference audit chain. It persists the observed capability
// snapshot (when one is present) into the CapabilitySnapshotStore keyed by its
// content hash. Best-effort: a persist failure is logged but never aborts the
// operation — audit integrity must not depend on storage being healthy.
func persistCapabilitySnapshot(ctx core.Context, stor storage.Storage, logger *slog.Logger) {
	if stor == nil {
		return
	}
	snap := ctx.CapabilitySnapshot()
	if snap == nil {
		return
	}
	if _, err := storage.PersistCapabilitySnapshot(snap, stor.Snapshots()); err != nil && logger != nil {
		logger.Warn("capability snapshot persist failed", "hash", snap.Hash(), "err", err)
	}
}

// handleGetSnapshot resolves a content-addressed CapabilitySnapshot by its hash
// (Phase 2.8 closure). The body is the raw JSON of snapshot.CapabilitySnapshot
// as persisted by PersistCapabilitySnapshot. RBAC-gated by the same permission
// as /api/capabilities so the audit viewer and the live probe share one graph.
func (s *Server) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := auth.Authorize(s.stor, username, "system.host.capability.list"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	hash := r.PathValue("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "hash required")
		return
	}
	payload, err := s.stor.Snapshots().GetByHash(hash)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "snapshot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// parseTargetBody extracts an optional "target" from decoded params (so it
// does not leak into the operation input) and returns a TargetHost. Two shapes
// are accepted:
//   - a string  -> a named host reference, resolved through the Host Registry
//     (e.g. "web-01"); the server must have a HostStore configured.
//   - an object -> an inline connection spec (the original behaviour).
//
// A present-but-invalid target is an error; an absent one is not.
func parseTargetBody(params map[string]any, hosts hostregistry.HostStore) (core.TargetHost, error) {
	raw, ok := params["target"]
	if !ok {
		return core.TargetHost{}, nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return core.TargetHost{}, fmt.Errorf("target name must not be empty")
		}
		return hostregistry.ResolveTarget(hosts, v)
	case map[string]any:
		t := core.TargetHost{}
		if a, ok := v["address"].(string); ok {
			t.Address = a
		}
		if t.Address == "" {
			return core.TargetHost{}, fmt.Errorf("target.address is required")
		}
		if p, ok := v["port"].(float64); ok {
			t.Port = int(p)
		}
		if u, ok := v["user"].(string); ok {
			t.User = u
		}
		if pw, ok := v["password"].(string); ok {
			t.Password = pw
		}
		if k, ok := v["key_path"].(string); ok {
			t.KeyPath = k
		}
		if ik, ok := v["insecure"].(bool); ok {
			t.InsecureIgnoreHostKey = ik
		}
		if sd, ok := v["sudo"].(bool); ok {
			t.Sudo = sd
		}
		return t, nil
	default:
		return core.TargetHost{}, fmt.Errorf("target must be a host name (string) or an object")
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	access, refresh, _, err := s.auth.Login(c.Username, c.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, tokenPair{AccessToken: access, RefreshToken: refresh})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	access, refresh, err := s.auth.Refresh(body.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	writeJSON(w, http.StatusOK, tokenPair{AccessToken: access, RefreshToken: refresh})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.allowRegister {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if c.Username == "" || c.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	u, err := s.auth.Register(c.Username, c.Password)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			writeError(w, http.StatusConflict, "user already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"username": u.Username, "roles": []string{}})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	u, err := s.stor.Users().GetByUsername(username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	roles, _ := s.stor.Users().Roles(u.ID)
	roleNames := make([]string, 0, len(roles))
	for _, role := range roles {
		roleNames = append(roleNames, role.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": u.Username, "roles": roleNames})
}

func (s *Server) handleListOps(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	ops := s.dispatcher.ListOperations()
	out := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		allowed, _ := auth.Can(s.stor, username, op.Name)
		out = append(out, map[string]any{
			"name":       op.Name,
			"permission": op.Permission.String(),
			"risk":       op.Risk.String(),
			"allowed":    allowed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	opName := r.PathValue("name")
	if err := auth.Authorize(s.stor, username, opName); err != nil {
		s.writeAuthError(w, err)
		return
	}
	params, err := decodeParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	target, err := parseTargetBody(params, s.hosts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target: "+err.Error())
		return
	}
	delete(params, "target")
	if target.IsZero() {
		target = s.defaultTarget // fall back to server default target
	}
	if s.useSudo {
		target.Sudo = true // server policy: managed host needs privilege escalation
	}
	ctx := s.coreCtx(r, username, target, r.Context())
	plan, err := s.dispatcher.Plan(ctx, opName, params)
	if err != nil {
		if errors.Is(err, core.ErrOperationNotFound) {
			writeError(w, http.StatusNotFound, "operation not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "plan failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, serializePlan(plan))
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	opName := r.PathValue("name")
	if err := auth.Authorize(s.stor, username, opName); err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Phase 21 Operational Protection gate (ADR-048): kill switch / breaker /
	// concurrency / rate / timeout, in a FIXED order AFTER authn+authz. On
	// reject the gate has already appended the protection.* audit row; we only
	// surface the HTTP status.
	var adm *protection.Admission
	// Detach a long-running execution from the HTTP request lifecycle on the
	// gate-disabled path: keep the request's values but stop the request's
	// cancellation from killing the execution. The gate-enabled path re-derives
	// its deadline from the request via Gate.Check (DeadlineContext), so the
	// gate still owns timeout/cancel semantics. This fixes executions started
	// over HTTP being cancelled the moment the request handler returns.
	parentCtx := context.WithoutCancel(r.Context())
	if s.gate != nil {
		a, rej := s.gate.Check(r.Context(), opName, username)
		if rej != nil {
			writeError(w, rej.HTTPStatus, rej.Action)
			return
		}
		adm = a
		pc, cancel := adm.DeadlineContext(r.Context())
		parentCtx = pc
		defer cancel()
		defer adm.Release()
	}
	params, err := decodeParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	target, err := parseTargetBody(params, s.hosts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target: "+err.Error())
		return
	}
	delete(params, "target")
	if target.IsZero() {
		target = s.defaultTarget // fall back to server default target
	}
	if s.useSudo {
		target.Sudo = true // server policy: managed host needs privilege escalation
	}
	ctx := s.coreCtx(r, username, target, parentCtx)

	// Execute through the Runtime when present so the run is recorded (giving
	// it an execution id the client can later cancel) and cancellable. The
	// fallback keeps the Dispatcher-only path working for tests / minimal use.
	var execID string
	var result core.ExecutionResult
	if s.runtime != nil {
		plan, err := s.dispatcher.Plan(ctx, opName, params)
		if err != nil {
			if errors.Is(err, core.ErrOperationNotFound) {
				writeError(w, http.StatusNotFound, "operation not registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "plan failed: "+err.Error())
			return
		}
		rr := s.runtime.Run(ctx, plan)
		execID = rr.ID
		result = rr.Result
	} else {
		result = s.dispatcher.Execute(ctx, opName, params)
	}

	status := http.StatusOK
	if !result.Success && !result.Cancelled {
		status = http.StatusInternalServerError
	}
	// Phase 21: feed the breaker (protection feedback only — R21-10).
	if adm != nil {
		adm.RecordOutcome(!result.Success)
	}
	body := serializeResult(result)
	if execID != "" {
		body["execution_id"] = execID
	}
	writeJSON(w, status, body)
}

// handleBatch fans a single operation out to many hosts (Phase 2.5). The body
// carries the operation name, a host group and/or an explicit list of named
// hosts, and the operation parameters. The sub-operation is authorized once
// for the whole batch (homogeneous fan-out); each resolved target then gets its
// own Context and the operation runs independently, so a single unreachable
// host does not abort the others.
func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var body struct {
		Op      string         `json:"op"`
		Group   string         `json:"group"`
		Targets []string       `json:"targets"`
		Params  map[string]any `json:"params"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Op == "" {
		writeError(w, http.StatusBadRequest, "op is required")
		return
	}
	if err := auth.Authorize(s.stor, username, body.Op); err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Phase 21 protection gate (per-operation fan-out).
	var adm *protection.Admission
	if s.gate != nil {
		a, rej := s.gate.Check(r.Context(), body.Op, username)
		if rej != nil {
			writeError(w, rej.HTTPStatus, rej.Action)
			return
		}
		adm = a
		defer adm.Release()
	}
	if s.hosts == nil {
		writeError(w, http.StatusBadRequest, "host registry not configured")
		return
	}

	// Resolve the target set: a group, explicit named hosts, or both.
	var targets []core.TargetHost
	if body.Group != "" {
		g, err := hostregistry.ResolveGroup(s.hosts, body.Group)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resolve group: "+err.Error())
			return
		}
		targets = append(targets, g...)
	}
	for _, name := range body.Targets {
		t, err := hostregistry.ResolveTarget(s.hosts, name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resolve target "+name+": "+err.Error())
			return
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "no targets resolved (provide group and/or targets)")
		return
	}

	// Server policy: managed hosts may require privilege escalation.
	if s.useSudo {
		for i := range targets {
			targets[i].Sudo = true
		}
	}

	params := body.Params
	if params == nil {
		params = map[string]any{}
	}
	// Detach a long-running execution from the HTTP request lifecycle on the
	// gate-disabled path: keep the request's values but stop the request's
	// cancellation from killing the execution. The gate-enabled path re-derives
	// its deadline from the request via Gate.Check (DeadlineContext), so the
	// gate still owns timeout/cancel semantics. This fixes executions started
	// over HTTP being cancelled the moment the request handler returns.
	parentCtx := context.WithoutCancel(r.Context())
	if adm != nil {
		pc, cancel := adm.DeadlineContext(r.Context())
		parentCtx = pc
		defer cancel()
	}
	parent := s.coreCtx(r, username, s.defaultTarget, parentCtx)
	results := s.dispatcher.Batch(parent, body.Op, targets, params)
	// Phase 21: feed the breaker (protection feedback only — R21-10).
	if adm != nil {
		anyFailed := false
		for _, br := range results {
			if !br.Success {
				anyFailed = true
				break
			}
		}
		adm.RecordOutcome(anyFailed)
	}

	out := make([]map[string]any, 0, len(results))
	for _, br := range results {
		m := map[string]any{
			"address": br.Target.Address,
			"success": br.Success,
		}
		if br.Error != "" {
			m["error"] = br.Error
		}
		if br.Result.Output != "" {
			m["output"] = br.Result.Output
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"op": body.Op, "results": out})
}

// isAdmin reports whether the user holds the synced admin role.
func (s *Server) isAdmin(username string) bool {
	u, err := s.stor.Users().GetByUsername(username)
	if err != nil {
		return false
	}
	roles, _ := s.stor.Users().Roles(u.ID)
	for _, role := range roles {
		if role.Name == sync.DefaultAdminRole {
			return true
		}
	}
	return false
}

// handleAudit returns persisted audit events. Admin-only (Phase 1.6).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := s.stor.Audit().List(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit query failed")
		return
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{
			"timestamp": e.Timestamp,
			"actor":     e.Actor,
			"operation": e.Operation,
			"action":    e.Action,
			"target":    e.Target,
			"result":    e.Result,
			"detail":    e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// handleCapabilities returns the host's discovered capabilities (Phase 2.1).
// It is gated by the system.host.capability.list permission so it reuses the
// same RBAC graph as the Operation itself — no second authorization path.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := auth.Authorize(s.stor, username, "system.host.capability.list"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	ctx := core.NewContext().
		WithLogger(s.logger).
		WithStdContext(r.Context()).
		Build()
	writeJSON(w, http.StatusOK, map[string]any{
		"hostname":     ctx.Host().Hostname,
		"os":           ctx.Host().OS,
		"arch":         ctx.Host().Arch,
		"capabilities": corecap.Snapshot(ctx),
		// Phase 2.6 observation surface (ADR-009): the frozen, serializable
		// host identity and capability matrix, populated by Context.Build()
		// for local and by EnrichContextForTarget for remote targets.
		"host_snapshot":       ctx.HostSnapshot(),
		"capability_snapshot": ctx.CapabilitySnapshot(),
	})
}

// handleInventory returns the unified host view (Phase 2.7 closure): the single
// consumer of HostSnapshot + CapabilitySnapshot, via the inventory package. For
// GET /api/inventory the current target (server default, overridable per
// request) is used; for GET /api/inventory/{name} the named host is resolved
// from the Host Registry and probed. RBAC-gated by system.host.capability.list.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := auth.Authorize(s.stor, username, "system.host.capability.list"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Resolve the target: a named host (path) takes precedence over the server
	// default. An absent default + no name falls back to a zero (local) target.
	target := s.defaultTarget
	if name := r.PathValue("name"); name != "" {
		if s.hosts == nil {
			writeError(w, http.StatusNotFound, "host registry not enabled")
			return
		}
		t, err := hostregistry.ResolveTarget(s.hosts, name)
		if err != nil {
			writeError(w, http.StatusNotFound, "host not found: "+err.Error())
			return
		}
		target = t
	}
	// coreCtx enriches (SSH probe for remote targets) and content-addresses the
	// capability snapshot, so the returned inventory matches what was persisted.
	ctx := s.coreCtx(r, username, target, r.Context())
	view := inventory.Build(ctx)

	// Phase 2.9 closure: surface the real read-only op output as inventory
	// "detail" data. The collection runs each whitelisted op through the
	// EXISTING Execution stack (Dispatcher.Plan -> Runtime.Run -> Executor ->
	// Audit) — it never bypasses the SSOT. Per-op RBAC is enforced up front;
	// an unauthorized op is marked Skipped and a failing op is recorded as a
	// failed section, so the whole inventory never 500s on a partial target.
	if s.runtime != nil && s.dispatcher != nil {
		runner := func(ctx core.Context, opName string) ([]string, error) {
			if err := auth.Authorize(s.stor, username, opName); err != nil {
				return nil, inventory.ErrOpUnauthorized
			}
			plan, err := s.dispatcher.Plan(ctx, opName, nil)
			if err != nil {
				return nil, err
			}
			rr := s.runtime.Run(ctx, plan)
			if !rr.Result.Success {
				if rr.Result.Error != nil {
					return nil, rr.Result.Error
				}
				return nil, errors.New("op returned no result")
			}
			var out []string
			for _, sr := range rr.Result.Steps {
				if sr.Output != "" {
					out = append(out, sr.Output)
				}
			}
			return out, nil
		}
		view.Detail = inventory.Collect(ctx, runner)
	}

	writeJSON(w, http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// Execution API (Phase 2.1.4)
// ---------------------------------------------------------------------------

// canViewExecution reports whether username may see/cancel a given execution.
// Admins see everything; a non-admin user may only access their own runs.
func (s *Server) canViewExecution(username string, rec *execution.ExecutionRecord) bool {
	if s.isAdmin(username) {
		return true
	}
	return rec.UserName == username
}

// handleListExecutions returns recorded executions. Admins see all; non-admins
// see only their own. The Runtime/Store is required (serve mode wires it).
func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if s.runtime == nil {
		writeError(w, http.StatusNotFound, "execution tracking not enabled")
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	recs, err := s.runtime.Store().List(execution.Query{Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "execution query failed")
		return
	}
	// execution.list grants "see all"; without it a user sees only their
	// own runs (ownership), so non-admins can still introspect what
	// they started without a global listing permission.
	if seeAll, _ := auth.Can(s.stor, username, "execution.list"); !seeAll {
		filtered := make([]execution.ExecutionRecord, 0, len(recs))
		for _, rec := range recs {
			if rec.UserName == username {
				filtered = append(filtered, rec)
			}
		}
		recs = filtered
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, serializeExecution(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": out})
}

// handleGetExecution returns a single execution by id. RBAC-gated by ownership.
func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if s.runtime == nil {
		writeError(w, http.StatusNotFound, "execution tracking not enabled")
		return
	}
	id := r.PathValue("id")
	rec, err := s.runtime.Store().Get(id)
	if errors.Is(err, execution.ErrNotFound) {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "execution query failed")
		return
	}
	if err := auth.Authorize(s.stor, username, "execution.read"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !s.canViewExecution(username, rec) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, serializeExecution(*rec))
}

// handleCancelExecution requests cancellation of a running execution.
// Only admins or the run's owner may cancel, and only while it is RUNNING.
func (s *Server) handleCancelExecution(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if s.runtime == nil {
		writeError(w, http.StatusNotFound, "execution tracking not enabled")
		return
	}
	id := r.PathValue("id")
	rec, err := s.runtime.Store().Get(id)
	if errors.Is(err, execution.ErrNotFound) {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "execution query failed")
		return
	}
	if err := auth.Authorize(s.stor, username, "execution.cancel"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !s.canViewExecution(username, rec) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if rec.Status != execution.StatusRunning {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("execution not cancellable (status: %s)", rec.Status))
		return
	}
	if !s.runtime.Cancel(id) {
		writeError(w, http.StatusConflict, "execution not cancellable")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     id,
		"status": string(execution.StatusCancelRequested),
	})
}

// handleCreateExecution asynchronously submits an execution (Phase 2.1 /
// Round 3 Q3): it authorizes execution.create (and the underlying
// operation), plans it, then hands it to the ExecutionService which
// creates a PLANNING record and returns it immediately with 202
// Accepted + the new id. A background goroutine drives it to Running
// then a terminal state, publishing execution.* events onto the bus
// for the WebSocket stream.
func (s *Server) handleCreateExecution(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := auth.Authorize(s.stor, username, "execution.create"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	var body struct {
		Op     string         `json:"op"`
		Params map[string]any `json:"params"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Op == "" {
		writeError(w, http.StatusBadRequest, "op is required")
		return
	}
	// The submitter must also hold the operation's own permission:
	// execution.create gates "may you start a run", the per-operation
	// permission gates "may you run THIS operation".
	if err := auth.Authorize(s.stor, username, body.Op); err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Phase 21 protection gate. The per-operation permission already passed;
	// this enforces kill/breaker/concurrency/rate. For the async execution path
	// the admission is released immediately after Submit — the gate bounds
	// SUBMISSION concurrency, not in-flight duration (R21-14; async runtime
	// lifecycle hooks for in-flight bounding are a documented follow-up).
	var adm *protection.Admission
	// Detach a long-running execution from the HTTP request lifecycle on the
	// gate-disabled path: keep the request's values but stop the request's
	// cancellation from killing the execution. The gate-enabled path re-derives
	// its deadline from the request via Gate.Check (DeadlineContext), so the
	// gate still owns timeout/cancel semantics. This fixes executions started
	// over HTTP being cancelled the moment the request handler returns.
	parentCtx := context.WithoutCancel(r.Context())
	if s.gate != nil {
		a, rej := s.gate.Check(r.Context(), body.Op, username)
		if rej != nil {
			writeError(w, rej.HTTPStatus, rej.Action)
			return
		}
		adm = a
		pc, cancel := adm.DeadlineContext(r.Context())
		parentCtx = pc
		defer cancel()
		defer adm.Release()
	}
	params := body.Params
	if params == nil {
		params = map[string]any{}
	}
	target, err := parseTargetBody(params, s.hosts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target: "+err.Error())
		return
	}
	delete(params, "target")
	if target.IsZero() {
		target = s.defaultTarget
	}
	if s.useSudo {
		target.Sudo = true
	}
	ctx := s.coreCtx(r, username, target, parentCtx)
	plan, err := s.dispatcher.Plan(ctx, body.Op, params)
	if err != nil {
		if errors.Is(err, core.ErrOperationNotFound) {
			writeError(w, http.StatusNotFound, "operation not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "plan failed: "+err.Error())
		return
	}
	if s.runtime == nil {
		writeError(w, http.StatusNotFound, "execution tracking not enabled")
		return
	}
	rec, err := s.runtime.Service().Submit(ctx, plan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "submit failed: "+err.Error())
		return
	}
	// 202 Accepted: the run is queued (PLANNING), not finished.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     rec.ID,
		"status": string(rec.Status),
	})
}

// handleGetExecutionTimeline projects one execution's lifecycle into an
// ordered timeline (Phase 2.1.4). The source of truth is the
// ExecutionRecord's frozen timestamps (created/started/finished) plus
// its per-step records, so the projection is deterministic with no
// extra event log to keep in sync. RBAC: execution.read + ownership.
func (s *Server) handleGetExecutionTimeline(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if s.runtime == nil {
		writeError(w, http.StatusNotFound, "execution tracking not enabled")
		return
	}
	id := r.PathValue("id")
	rec, err := s.runtime.Store().Get(id)
	if errors.Is(err, execution.ErrNotFound) {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "execution query failed")
		return
	}
	if err := auth.Authorize(s.stor, username, "execution.read"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !s.canViewExecution(username, rec) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, serializeTimeline(*rec))
}

// handleExecutionsStream upgrades the request to a WebSocket and streams
// execution.* lifecycle events (Phase 2.1 / Round 3: "WS 同批进 2.1").
// Authorization (authn + execution.read) is enforced here, before the
// upgrade, so only authenticated, read-granted principals connect.
// The hub forwards every bus event to all subscribers; per-owner
// filtering is a follow-up.
func (s *Server) handleExecutionsStream(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := auth.Authorize(s.stor, username, "execution.read"); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.wsHub.ServeWS(w, r)
}

// serializeTimeline projects an ExecutionRecord's lifecycle into an ordered
// list of {ts, type, status, detail} events derived from its frozen
// timestamps and per-step records (Phase 2.1.4). Step timestamps are
// approximate (StartedAt + cumulative prior durations) since the store
// keeps only per-step DurationMs, not absolute step times.
func serializeTimeline(rec execution.ExecutionRecord) map[string]any {
	events := make([]map[string]any, 0, 4+len(rec.Steps))
	ev := func(ts time.Time, typ, status, detail string) {
		e := map[string]any{"type": typ, "status": status}
		if !ts.IsZero() {
			e["ts"] = ts.Format(time.RFC3339)
		}
		if detail != "" {
			e["detail"] = detail
		}
		events = append(events, e)
	}
	if !rec.CreatedAt.IsZero() {
		ev(rec.CreatedAt, "created", string(execution.StatusPlanning), "")
	}
	if rec.StartedAt != nil {
		ev(*rec.StartedAt, "started", string(execution.StatusRunning), "")
	}
	cum := time.Duration(0)
	for _, st := range rec.Steps {
		ts := time.Time{}
		if rec.StartedAt != nil {
			ts = rec.StartedAt.Add(cum)
		}
		ev(ts, "step:"+st.Name, string(st.Status), st.Output)
		cum += time.Duration(st.DurationMs) * time.Millisecond
	}
	if rec.FinishedAt != nil {
		ev(*rec.FinishedAt, "finished", string(rec.Status), rec.Error)
	}
	return map[string]any{
		"id":     rec.ID,
		"status": string(rec.Status),
		"events": events,
	}
}

// ---------------------------------------------------------------------------
// Host Registry (Phase 2.3)
// ---------------------------------------------------------------------------

// hostAdmin authenticates the caller and confirms the host registry is enabled
// and the caller holds the admin role. On failure it writes the appropriate
// status and returns ("", false); otherwise it returns (username, true).
func (s *Server) hostAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return "", false
	}
	if s.hosts == nil {
		writeError(w, http.StatusNotFound, "host registry not enabled")
		return "", false
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return "", false
	}
	return username, true
}

// handleListHosts returns registered hosts (optionally filtered by ?group=).
// Secrets are redacted in the projection.
func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hostAdmin(w, r); !ok {
		return
	}
	group := r.URL.Query().Get("group")
	var hosts []hostregistry.Host
	var err error
	if group != "" {
		hosts, err = s.hosts.ListByGroup(group)
	} else {
		hosts, err = s.hosts.List()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host query failed")
		return
	}
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, serializeHost(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

// handleGetHost returns a single registered host by name (secrets redacted).
func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hostAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	h, err := s.hosts.GetByName(name)
	if errors.Is(err, hostregistry.ErrHostNotFound) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host query failed")
		return
	}
	writeJSON(w, http.StatusOK, serializeHost(h))
}

// handleCreateHost registers (or overwrites) a named host.
func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hostAdmin(w, r); !ok {
		return
	}
	var body struct {
		Name     string            `json:"name"`
		Address  string            `json:"address"`
		Port     int               `json:"port"`
		User     string            `json:"user"`
		Password string            `json:"password"`
		KeyPath  string            `json:"key_path"`
		Insecure bool              `json:"insecure"`
		Sudo     bool              `json:"sudo"`
		Groups   []string          `json:"groups"`
		Labels   map[string]string `json:"labels"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "host name required")
		return
	}
	if body.Address == "" {
		writeError(w, http.StatusBadRequest, "host address required")
		return
	}
	h := hostregistry.Host{
		Name: body.Name,
		Target: core.TargetHost{
			Address:               body.Address,
			Port:                  body.Port,
			User:                  body.User,
			Password:              body.Password,
			KeyPath:               body.KeyPath,
			InsecureIgnoreHostKey: body.Insecure,
			Sudo:                  body.Sudo,
		},
		Groups: body.Groups,
		Labels: body.Labels,
	}
	if _, err := s.hosts.Save(h); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save host")
		return
	}
	writeJSON(w, http.StatusCreated, serializeHost(h))
}

// handleDeleteHost removes a registered host by name.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hostAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := s.hosts.Delete(name); err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// serializeHost projects a registered Host into JSON. Secrets (password and
// inline key bytes) are deliberately omitted; key_path is a filesystem path,
// not a secret, and is safe to surface.
func serializeHost(h hostregistry.Host) map[string]any {
	obj := map[string]any{
		"name":     h.Name,
		"address":  h.Target.Address,
		"port":     h.Target.Port,
		"user":     h.Target.User,
		"key_path": h.Target.KeyPath,
		"insecure": h.Target.InsecureIgnoreHostKey,
		"sudo":     h.Target.Sudo,
		"groups":   h.Groups,
	}
	if len(h.Labels) > 0 {
		obj["labels"] = h.Labels
	}
	return obj
}

// writeAuthError maps RBAC errors to HTTP status codes.
func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrOperationNotFound):
		writeError(w, http.StatusNotFound, "operation not found")
	case errors.Is(err, auth.ErrOperationDisabled):
		writeError(w, http.StatusForbidden, "operation disabled")
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		writeError(w, http.StatusForbidden, "authorization denied")
	}
}

// ---------------------------------------------------------------------------
// Phase 21 Operational Protection read surface (ADR-048)
// ---------------------------------------------------------------------------

// handleProtectionKills exposes the kill-switch tri-state and the persisted
// kill list. Admin-only — the kill switch is a sensitive operational control.
// Served on :8082 via Server.ProtectionReadMux() (R21-1 / ADR-048 B1 fix):
// colocated with the gate that owns the kill store, so the counters are real
// and not the harness read-surface's always-zero "fake green".
func (s *Server) handleProtectionKills(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}
	kills, err := s.gate.ListKills()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "kill list query failed")
		return
	}
	out := make([]map[string]any, 0, len(kills))
	for _, k := range kills {
		out = append(out, map[string]any{
			"capability_id":  k.CapabilityID,
			"principal":      k.Principal,
			"principal_hash": k.PrincipalHash,
			"killed":         k.Killed,
			"killed_at":      k.KilledAt,
			"killed_by":      k.KilledBy,
		})
	}
	// Phase 22.2 operator attribution (who/why/when) — dashboard-only, derived
	// from KillStore.opMeta, never authoritative for admission.
	opOut := make([]map[string]any, 0)
	for _, ok := range s.gate.KillStore().ListOperatorKills() {
		opOut = append(opOut, map[string]any{
			"capability_id": ok.CapabilityID,
			"operator":      ok.Operator,
			"reason":        ok.Reason,
			"at":            ok.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":         s.gate.KillState().String(),
		"kills":         out,
		"operator_kills": opOut,
	})
}

// handleProtectionMetrics exposes the Phase 21 exact counters (R21-7).
func (s *Server) handleProtectionMetrics(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}
	m := s.gate.SnapshotMetrics()
	writeJSON(w, http.StatusOK, map[string]any{
		"admitted":                m.Admitted,
		"killed":                  m.Killed,
		"principal_killed":        m.PrincipalKilled,
		"circuit_open":            m.CircuitOpen,
		"breaker_unknown":         m.BreakerUnknown,
		"concurrency_exceeded":    m.ConcurrencyExceeded,
		"quota_exceeded":          m.QuotaExceeded,
		"quota_evidence_unavailable": m.QuotaEvidenceUnavailable,
		"rate_limited":            m.RateLimited,
		"audit_write_failed":      m.AuditWriteFailed,
	})
}

// ProtectionReadMux returns the Phase 21 Operational Protection management read
// surface (R21-1 / ADR-048 B1 fix). Bound on :8082 by the Control Plane
// composition root (cmd/opscore), it is colocated with the gate that owns the
// kill store + metric counters, so the in-memory metrics are REAL — updated by
// the same process that performs the execution interception. Hanging it on the
// harness :8082 read-surface (no gate) would report always-zero "fake green".
func (s *Server) ProtectionReadMux() http.Handler {
	mux := http.NewServeMux()
	// Read surface (R21-1, colocated with the gate).
	mux.HandleFunc("GET /management/v1/protection/kills", s.handleProtectionKills)
	mux.HandleFunc("GET /management/v1/protection/metrics", s.handleProtectionMetrics)
	// Phase 23.2 Resource Quota Protection read + write surface (R23-3 single
	// owner; R23-4 definitions-only, no consumption projected). Admin-only; the
	// write routes are additionally CSRF fail-closed (P22-9 analog). :8080
	// never serves these.
	mux.HandleFunc("GET /management/v1/protection/quotas", s.handleProtectionQuotas)
	mux.HandleFunc("POST /management/v1/protection/quotas", s.handleProtectionQuotaSet)
	mux.HandleFunc("DELETE /management/v1/protection/quotas", s.handleProtectionQuotaClear)
	// Phase 22.2 write surface — the SINGLE mutation seam (P22-2), admin-only
	// and CSRF fail-closed (P22-9). Same-origin only; :8080 never serves these.
	mux.HandleFunc("POST /management/v1/protection/kills", s.handleProtectionKill)
	mux.HandleFunc("POST /management/v1/protection/kills/{id}/release", s.handleProtectionRelease)
	// Same-origin dashboard session login (P22-9): sets the httpOnly + SameSite=
	// Strict cookie so the SPA needs no CORS and never holds the token in JS.
	mux.HandleFunc("POST /management/v1/login", s.handleManagementLogin)
	// Public login shell (unauthenticated entry point — renders the SPA login
	// form, exposes no data). The protected console itself is admin-only.
	mux.HandleFunc("GET /login", s.handleLoginShell)
	// Embedded Operational Protection Console SPA (Phase 22.2). ADMIN-ONLY per
	// ADR-050 / R106=B: unauthenticated callers must not receive the shell.
	mux.HandleFunc("GET /dashboard", s.handleDashboard)
	// Phase 24.2 Protection Observability Hardening — read-only surfaces:
	//   • /decisions  — decision-log projection (R24-1/R24-5), carries only the
	//     principal hash + advisory refs (R24-7 secret boundary), with explicit
	//     completeness/truncation signals.
	//   • /alerts     — declarative alert state (R24-3): computed + exposed only.
	// Both admin-only and registered ONLY here (:8082), never on :8080 (R21-1).
	mux.HandleFunc("GET /management/v1/protection/decisions", s.handleProtectionDecisions)
	mux.HandleFunc("GET /management/v1/protection/alerts", s.handleProtectionAlerts)
	return mux
}

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

func serializePlan(p *core.ExecutionPlan) map[string]any {
	steps := make([]string, 0, len(p.Steps))
	for _, st := range p.Steps {
		steps = append(steps, st.Describe())
	}
	return map[string]any{
		"operation":  p.OperationName,
		"permission": p.Permission.String(),
		"risk":       p.Risk.String(),
		"steps":      steps,
	}
}

// serializeExecution projects an execution.ExecutionRecord into the JSON
// shape the UI/API consumers. Times are emitted as RFC3339 for portability.
func serializeExecution(rec execution.ExecutionRecord) map[string]any {
	steps := make([]map[string]any, 0, len(rec.Steps))
	for _, st := range rec.Steps {
		steps = append(steps, map[string]any{
			"id":          st.ID,
			"name":        st.Name,
			"index":       st.Index,
			"status":      string(st.Status),
			"success":     st.Success,
			"output":      st.Output,
			"error":       st.Error,
			"duration_ms": st.DurationMs,
		})
	}
	obj := map[string]any{
		"id":         rec.ID,
		"operation":  rec.Operation,
		"permission": rec.Permission,
		"risk":       rec.Risk,
		"status":     string(rec.Status),
		"user_id":    rec.UserID,
		"user_name":  rec.UserName,
		"target":     rec.Target,
		"created_at": rec.CreatedAt.Format(time.RFC3339),
		"error":      rec.Error,
		"steps":      steps,
	}
	if rec.StartedAt != nil {
		obj["started_at"] = rec.StartedAt.Format(time.RFC3339)
	}
	if rec.FinishedAt != nil {
		obj["finished_at"] = rec.FinishedAt.Format(time.RFC3339)
	}
	return obj
}

func serializeResult(res core.ExecutionResult) map[string]any {
	steps := make([]map[string]any, 0, len(res.Steps))
	for _, st := range res.Steps {
		steps = append(steps, map[string]any{
			"step":        st.StepName,
			"success":     st.Success,
			"output":      st.Output,
			"duration_ms": st.Duration.Milliseconds(),
			"error":       errString(st.Error),
		})
	}
	return map[string]any{
		"success":     res.Success,
		"cancelled":   res.Cancelled,
		"duration_ms": res.Duration.Milliseconds(),
		"error":       errString(res.Error),
		"steps":       steps,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// decodeParams reads a flat JSON object body as operation input params.
// An empty/absent body yields an empty (non-nil) map.
func decodeParams(r *http.Request) (map[string]any, error) {
	params := map[string]any{}
	if r.Body == nil {
		return params, nil
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		if err.Error() == "EOF" {
			return params, nil
		}
		return params, err
	}
	return params, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
