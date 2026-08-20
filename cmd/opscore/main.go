package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/builtin/capability"
	"github.com/YuDong999/opscore/internal/builtin/disk"
	"github.com/YuDong999/opscore/internal/builtin/firewall"
	"github.com/YuDong999/opscore/internal/builtin/host"
	"github.com/YuDong999/opscore/internal/builtin/journal"
	"github.com/YuDong999/opscore/internal/builtin/package"
	"github.com/YuDong999/opscore/internal/builtin/process"
	"github.com/YuDong999/opscore/internal/builtin/service"
	"github.com/YuDong999/opscore/internal/builtin/user"
	"github.com/YuDong999/opscore/internal/controlplane/audit"
	"github.com/YuDong999/opscore/internal/controlplane/hostregistry"
	"github.com/YuDong999/opscore/internal/controlplane/server"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/demo"
	pluginrt "github.com/YuDong999/opscore/internal/plugin/runtime"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
	"github.com/YuDong999/opscore/internal/storage/memory"
	"github.com/YuDong999/opscore/internal/storage/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "list":
		cmdList()
	case "plan":
		cmdPlan(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "host":
		cmdHost(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

// newLogger builds the standard text logger.
func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// newCore wires Registry + AuditSink + Executor + Dispatcher + Runtime.
// This is the composition root for the Kernel. Shared by CLI and serve.
// The AuditSink is injected so serve can persist audit to Storage while the
// CLI keeps the dependency-free LogSink. exeStore backs the Execution Core
// (Recorder writes + Execution API reads); serve passes a SQLite-backed store,
// non-durable modes pass execution.NewMemoryStore(). The Executor only knows
// the Recorder interface, so swapping the backend is a one-line change here.
//
// It also builds the Plugin runtime.Manager, injecting the control-plane's
// SyncPlugin as the Permission-Sync callback. The Manager package stays free
// of the sync package (no import cycle) — only this composition root knows
// both (GPT Round 7/8: dependence direction must be runtime -> core, never
// runtime -> controlplane).
func newCore(logger *slog.Logger, stor storage.Storage, sink core.AuditSink, exeStore execution.Store) (*core.Registry, *core.Dispatcher, *core.Runtime, *pluginrt.Manager) {
	registry := core.NewRegistry()

	// Register builtin operations via the Module contract (Round6: every builtin
	// is a compile-time plugin; the kernel never knows builtin from plugin).
	// Adding a new operation family is just appending its Module here — there is
	// never an "if builtin" branch in the kernel.
	builtin.RegisterAll(registry,
		service.NewModule(),
		firewall.NewModule(),
		journal.NewModule(),
		host.NewModule(),
		process.NewModule(),
		capability.NewModule(),
		packageop.NewModule(),
		userop.NewModule(),
		diskop.NewModule(),
	)

	executor := core.NewExecutor(sink)
	// Phase 2.1.2/2.1.3: the Runtime bundles the executor with the execution
	// Store (Recorder + reads) and a cancel registry, so serve-mode runs are
	// recorded and cancellable by id. The Store is injected by the caller
	// (SQLite in serve mode, in-memory elsewhere) — the Executor only knows
	// the Recorder interface.
	runtime := core.NewRuntime(executor, exeStore)
	dispatcher := core.NewDispatcher(registry, executor)

	// Plugin runtime: Manager with injected SyncFunc (Permission Sync).
	// sync.New(...).SyncPlugin matches the runtime.SyncFunc signature.
	syncFn := sync.New(registry, stor).SyncPlugin
	manager := pluginrt.NewManager(registry, stor, syncFn)
	return registry, dispatcher, runtime, manager
}

// setupCore wires up the entire Core: Context + Dispatcher.
func setupCore() (core.Context, *core.Dispatcher) {
	logger := newLogger()
	ctx := core.NewContext().
		WithLogger(logger).
		Build()
	stor := storage.NewMemoryStorage()
	_, dispatcher, _, _ := newCore(logger, stor, core.NewLogSink(logger), execution.NewMemoryStore())
	return ctx, dispatcher
}

// ---------------------------------------------------------------------------
// Phase 21 Operational Protection wiring (ADR-048)
//
// The protection package defines its storage/audit interfaces; this composition
// root supplies the concrete adapters so the server can be constructed with a
// ready Gate. The Gate is FAIL-CLOSED: if the kill store fails to bootstrap,
// every capability is treated as killed and the server still starts (it simply
// refuses to admit operations it cannot verify protection state for).
// ---------------------------------------------------------------------------

// storageAuditWriter adapts storage.AuditStore to protection.AuditWriter. It
// records protection.* reject decisions as audit observations. R21-9: these are
// evidence of a decision, not an authorization prerequisite.
type storageAuditWriter struct {
	store storage.AuditStore
}

func (a *storageAuditWriter) WriteEvent(_ context.Context, ev protection.ProtectionEvent) error {
	_, err := a.store.Append(storage.AuditEvent{
		Timestamp: ev.Timestamp,
		Actor:     "system",
		Operation: ev.CapID,
		Action:    ev.Action,
		Target:    ev.CapID,
		Result:    "failure",
		Detail:    ev.Detail,
	})
	return err
}

// auditFailureReader adapts storage.AuditStore to protection.FailureEvidenceReader.
// It counts recent EXECUTION failures (Action=="execute", Result=="failure") for
// the capability within the window. Crucially it EXCLUDES protection.* rows, so
// a protection reject can never feed the breaker that produced it (no
// self-reinforcing loop — R21-8 fail-closed stays stable).
type auditFailureReader struct {
	store storage.AuditStore
	now   func() time.Time
}

func (r *auditFailureReader) RecentFailures(capabilityID string, window time.Duration) (protection.FailureWindow, error) {
	page, err := r.store.Query(storage.AuditQuery{
		Result: "failure",
		Action: "execute",
		Limit:  storage.MaxAuditQueryLimit,
	})
	if err != nil {
		return protection.FailureWindow{}, err
	}
	cutoff := r.now().Add(-window)
	count := 0
	for _, e := range page.Events {
		if e.Operation == capabilityID && !e.Timestamp.Before(cutoff) {
			count++
		}
	}
	return protection.FailureWindow{Count: count, Truncated: page.Truncated}, nil
}

// buildProtectionGate constructs the Phase 21 Operational Protection Gate for
// the active storage backend. The kill store is backend-specific (sqlite or
// in-memory); the breaker/rate/timeout are backend-agnostic.
func buildProtectionGate(stor storage.Storage, logger *slog.Logger) *protection.Gate {
	var killPersist protection.KillPersistence
	if s, ok := stor.(*sqlite.SQLiteStorage); ok {
		killPersist = sqlite.NewProtectionStore(s.DB())
	} else {
		killPersist = memory.NewProtectionStore()
	}
	ks := protection.NewKillStore(killPersist, time.Now)
	if err := ks.Bootstrap(); err != nil {
		logger.Warn("protection kill store bootstrap failed — fail closed (all capabilities killed)", "err", err)
	}

	// Phase 23.2 Resource Quota Protection (ADR-051). QuotaStore owns DEFINITIONS
	// only (R23-3); consumption lives in the evidence source (R23-3). Until live
	// telemetry is wired into the evidence reader, every definition reads Unknown
	// ⇒ fail-closed per R23-1 (evidence unavailable ⇒ reject, never substitute
	// zero/default — R23-4). This is the conservative default posture.
	var quotaPersist protection.QuotaPersistence
	if s, ok := stor.(*sqlite.SQLiteStorage); ok {
		quotaPersist = sqlite.NewQuotaStore(s.DB())
	} else {
		quotaPersist = memory.NewQuotaStore()
	}
	qs := protection.NewQuotaStore(quotaPersist, time.Now)
	if err := qs.Bootstrap(); err != nil {
		logger.Warn("protection quota store bootstrap failed — definitions treated as absent (no quota enforcement)", "err", err)
	}
	evidence := memory.NewQuotaEvidenceReader()

	return protection.New(protection.Config{
		KillStore: ks,
		Breaker: protection.NewBreakerSet(
			&auditFailureReader{store: stor.Audit(), now: time.Now},
			protection.DefaultBreakerConfig(),
			time.Now,
		),
		Sem:     protection.NewSemaphoreSet(8),
		Buckets: protection.NewTokenBucketSet(protection.TokenBucketConfig{Capacity: 100, Refill: 10}, time.Now),
		Quotas:  qs,
		Evidence: evidence,
		Audit:   &storageAuditWriter{store: stor.Audit()},
		Timeout: protection.NewTimeoutConfig(),
	})
}

// loadConfigFile reads a minimal KEY=VALUE config (one per line; # starts a
// comment; blank lines ignored). It lets deployment settings live in a file
// instead of a long command line. Missing file is not an error (config is
// optional). Env vars (OPESCORE_<KEY>) take precedence over the file, and
// explicit CLI flags take precedence over both.
func loadConfigFile(path string) (map[string]string, error) {
	vals := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return vals, nil
		}
		return vals, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		vals[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return vals, nil
}

// cfgVal resolves a string flag's default: file value -> OPESCORE_<KEY> env -> def.
func cfgVal(vals map[string]string, key, def string) string {
	if v, ok := vals[key]; ok && v != "" {
		return v
	}
	envKey := "OPESCORE_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

// cfgInt resolves an int flag's default the same way as cfgVal.
func cfgInt(vals map[string]string, key string, def int) int {
	if v, ok := vals[key]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	envKey := "OPESCORE_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// cfgBool resolves a bool flag's default: file value -> OPESCORE_<KEY> env -> def.
// Accepted truthy strings: 1/true/yes/on/y (case-insensitive).
func cfgBool(vals map[string]string, key string, def bool) bool {
	resolve := func(s string) (bool, bool) {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "on", "y":
			return true, true
		case "0", "false", "no", "off", "n":
			return false, true
		}
		return false, false
	}
	if v, ok := vals[key]; ok {
		if b, ok2 := resolve(v); ok2 {
			return b
		}
	}
	envKey := "OPESCORE_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if v := os.Getenv(envKey); v != "" {
		if b, ok2 := resolve(v); ok2 {
			return b
		}
	}
	return def
}

// cmdServe runs the Control Plane HTTP server (Phase 1.5).
func cmdServe(args []string) {
	// Stage 1: discover the config path (default opscore.env if present) so
	// file/env values can seed the real flag defaults below.
	probe := flag.NewFlagSet("probe", flag.ContinueOnError)
	probe.SetOutput(io.Discard)
	configPath := probe.String("config", "opscore.env", "config file (KEY=VALUE)")
	_ = probe.Parse(args)
	vals, err := loadConfigFile(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	// Stage 2: real flags; defaults come from file/env/constant (CLI overrides).
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = fs.String("config", "opscore.env", "config file (KEY=VALUE)")
	addr := fs.String("addr", cfgVal(vals, "addr", ":8080"), "listen address")
	mgmtAddr := fs.String("mgmt-addr", cfgVal(vals, "mgmt-addr", ":8082"), "protection management read surface listen address (:8082)")
	storageKind := fs.String("storage", cfgVal(vals, "storage", "memory"), "storage backend: memory | sqlite")
	dbPath := fs.String("db", cfgVal(vals, "db", "opscore.db"), "sqlite db path (when storage=sqlite)")
	adminUser := fs.String("admin-user", cfgVal(vals, "admin-user", "admin"), "bootstrap admin username")
	adminPass := fs.String("admin-pass", cfgVal(vals, "admin-pass", "admin"), "bootstrap admin password")
	jwtSecret := fs.String("jwt-secret", cfgVal(vals, "jwt-secret", "change-me-in-prod"), "JWT signing secret")
	allowRegister := fs.Bool("allow-register", cfgBool(vals, "allow-register", false), "allow open self-registration (default off; security surface)")
	// Default SSH target (every operation executes here unless overridden per-request).
	tHost := fs.String("target-host", cfgVal(vals, "target-host", ""), "default remote host (empty = local). Operations run over SSH to this host")
	tPort := fs.Int("target-port", cfgInt(vals, "target-port", 22), "default remote SSH port")
	tUser := fs.String("target-user", cfgVal(vals, "target-user", "root"), "default remote SSH user")
	tPass := fs.String("target-pass", cfgVal(vals, "target-pass", ""), "default remote SSH password")
	tKey := fs.String("target-key", cfgVal(vals, "target-key", ""), "path to default remote SSH private key (PEM)")
	tInsecure := fs.Bool("target-insecure", cfgBool(vals, "target-insecure", false), "skip SSH host key verification (dev/test only)")
	tSudo := fs.Bool("target-sudo", cfgBool(vals, "target-sudo", false), "run remote commands via 'sudo -n' (for unprivileged SSH users; ignored with --demo)")
	// Demo mode: spin up an in-process fake Linux host (SSH) so the service UI
	// can be exercised locally without a real systemd target. The fake host
	// becomes the default target.
	demoMode := fs.Bool("demo", cfgBool(vals, "demo", false), "start an embedded fake SSH host and use it as the default target")
	fs.Parse(args)

	logger := newLogger()

	// Security gate (P0-2): the default JWT secret is well-known, so anyone
	// could forge tokens if it ships to a real deployment. Refuse to start in
	// any non-trivial mode (persistent storage or non-demo) until a strong
	// secret is supplied. Demo + memory (local debugging) is exempt.
	const defaultJWTSecret = "change-me-in-prod"
	if *jwtSecret == defaultJWTSecret && !*demoMode && *storageKind != "memory" {
		logger.Error("refusing to start: --jwt-secret is still the insecure default; set a strong secret for non-demo / non-memory deployments")
		os.Exit(1)
	}

	// 1. Storage
	var stor storage.Storage
	switch *storageKind {
	case "sqlite":
		s, err := sqlite.NewSQLiteStorage(*dbPath)
		if err != nil {
			logger.Error("failed to open sqlite", "err", err)
			os.Exit(1)
		}
		stor = s
	default:
		stor = storage.NewMemoryStorage()
	}
	defer stor.Close()

	// 2. Kernel (audit persisted to Storage via StorageAuditSink). The
	// Execution Core shares the same SQLite *sql.DB via sqlite.NewExecutionStore
	// when storage=sqlite; otherwise it falls back to the in-memory store.
	exeStore := execution.Store(execution.NewMemoryStore())
	if s, ok := stor.(*sqlite.SQLiteStorage); ok {
		exeStore = sqlite.NewExecutionStore(s.DB())
	}
	registry, dispatcher, runtime, manager := newCore(logger, stor, audit.NewStorageAuditSink(stor, logger), exeStore)

	// 2a. Host Registry (Phase 2.3): in-memory default; populated at runtime
	// via POST /api/hosts. Enables named-target references ("target":"web-01").
	hostStore := hostregistry.NewMemoryHostStore()

	// 2b. Demo mode: start an in-process fake host and make it the default target.
	var defaultTarget core.TargetHost
	var demoOn bool
	if *demoMode {
		dt, stop, err := demo.StartFakeHost("demo")
		if err != nil {
			logger.Error("failed to start demo host", "err", err)
			os.Exit(1)
		}
		// The fake host lives for the process lifetime; tear it down on exit.
		defer stop()
		defaultTarget = dt
		demoOn = true
		logger.Info("demo mode: embedded fake host ready", "target", dt.Address, "port", dt.Port)
	}

	// 3. Sync metadata + admin role into storage (Phase 1.3)
	if err := sync.New(registry, stor).Sync(); err != nil {
		logger.Error("sync failed", "err", err)
		os.Exit(1)
	}

	// 3a. Plugin Bootstrap (Phase 3.2): the single plugin-startup
	// composition root, shared by CLI/serve/tests. The StaticLoader is the
	// contract/test loader (no .so — anti-slide, GPT Round 6/7/8); a
	// real FileLoader/OCIRegistryLoader plugs in HERE unchanged. Registry
	// restores STATE (enabled), the Loader supplies DEFINITION (ADR-010).
	pluginLoader := pluginrt.NewStaticLoader(map[string]pluginrt.Module{})
	bootCtx := context.Background()
	bootEnabled, bootErrs := manager.Bootstrap(bootCtx, pluginLoader, nil, pluginrt.BootstrapPolicy{AutoEnableNewPlugin: true})
	if len(bootErrs) > 0 {
		for _, e := range bootErrs {
			logger.Warn("plugin bootstrap issue", "err", e)
		}
	} else if len(bootEnabled) > 0 {
		logger.Info("plugins enabled at boot", "count", len(bootEnabled), "names", bootEnabled)
	}

	// 4. HTTP server with first-run admin bootstrap
	if *tHost != "" {
		defaultTarget = core.TargetHost{
			Address:               *tHost,
			Port:                  *tPort,
			User:                  *tUser,
			Password:              *tPass,
			KeyPath:               *tKey,
			InsecureIgnoreHostKey: *tInsecure,
		}
		logger.Info("default SSH target configured", "host", *tHost, "port", *tPort, "user", *tUser, "insecure", *tInsecure)
	}
	srv, err := server.New(server.Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		Runtime:        runtime,
		HostStore:      hostStore,
		AccessSecret:   *jwtSecret + ":access",
		RefreshSecret:  *jwtSecret + ":refresh",
		Logger:         logger,
		DefaultTarget:  defaultTarget,
		DemoMode:       demoOn,
		UseSudo:        *tSudo && !demoOn,
		AllowRegister:  *allowRegister,
		BootstrapAdmin: &server.BootstrapAdmin{Username: *adminUser, Password: *adminPass},
		Gate:           buildProtectionGate(stor, logger),
	})
	if err != nil {
		logger.Error("server init failed", "err", err)
		os.Exit(1)
	}

	// Phase 21 (R21-1 / ADR-048 B1 fix): the Protection management read surface
	// lives on :8082, colocated with the gate (this Control Plane process owns
	// the kill store + metric counters). Binding it here — not on the harness
	// :8082 read-surface — keeps the counters real (same process intercepts
	// executions and updates them). Exposed via server.ProtectionReadMux.
	// Deployment caveat (R97): exactly one Control Plane instance may bind
	// :8082 — do NOT also run the harness management surface on the same :8082
	// on one host, or the protection read surface collides ("correct code,
	// wrong topology").
	go func() {
		logger.Info("OpsCore Protection management read surface listening", "addr", *mgmtAddr)
		if err := http.ListenAndServe(*mgmtAddr, srv.ProtectionReadMux()); err != nil {
			logger.Error("protection management read surface stopped", "err", err)
		}
	}()

	logger.Info("OpsCore Control Plane listening", "addr", *addr, "storage", *storageKind)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func cmdList() {
	_, dispatcher := setupCore()
	ops := dispatcher.ListOperations()

	if len(ops) == 0 {
		fmt.Println("(no operations registered)")
		return
	}

	fmt.Printf("%-35s %-25s %-10s\n", "OPERATION", "PERMISSION", "RISK")
	fmt.Println("-------------------------------------------------------------------------")
	for _, op := range ops {
		fmt.Printf("%-35s %-25s %-10s\n",
			op.Name,
			op.Permission.String(),
			op.Risk.String(),
		)
	}
}

// kvFlags accumulates repeated `-arg key=value` pairs into a map, so the CLI
// can pass arbitrary operation inputs without a flag per operation.
type kvFlags struct{ m map[string]string }

func (k *kvFlags) String() string {
	if k.m == nil {
		return "{}"
	}
	return fmt.Sprintf("%v", k.m)
}

func (k *kvFlags) Set(s string) error {
	if k.m == nil {
		k.m = map[string]string{}
	}
	kv := strings.SplitN(s, "=", 2)
	if len(kv) != 2 {
		return fmt.Errorf("invalid -arg %q (want key=value)", s)
	}
	k.m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	return nil
}

// defaultHostsFile is where the CLI's local Host Registry lives (the same
// file-backed store the `host` subcommand manages). It mirrors the server's
// in-memory HostStore but persists to disk so named targets survive restarts.
func defaultHostsFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "hosts.json"
	}
	return filepath.Join(home, ".opscore", "hosts.json")
}

// cliHost is the on-disk projection of a registered host (file format only).
type cliHost struct {
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

func (c cliHost) toCore() hostregistry.Host {
	return hostregistry.Host{
		Name: c.Name,
		Target: core.TargetHost{
			Address:               c.Address,
			Port:                  c.Port,
			User:                  c.User,
			Password:              c.Password,
			KeyPath:               c.KeyPath,
			InsecureIgnoreHostKey: c.Insecure,
			Sudo:                  c.Sudo,
		},
		Groups: c.Groups,
		Labels: c.Labels,
	}
}

func fromCore(h hostregistry.Host) cliHost {
	return cliHost{
		Name:     h.Name,
		Address:  h.Target.Address,
		Port:     h.Target.Port,
		User:     h.Target.User,
		Password: h.Target.Password,
		KeyPath:  h.Target.KeyPath,
		Insecure: h.Target.InsecureIgnoreHostKey,
		Sudo:     h.Target.Sudo,
		Groups:   h.Groups,
		Labels:   h.Labels,
	}
}

// loadHostStoreFromFile reads the local hosts file into a MemoryHostStore.
// A missing file yields an empty (but valid) store.
func loadHostStoreFromFile(path string) (*hostregistry.MemoryHostStore, error) {
	store := hostregistry.NewMemoryHostStore()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	var list []cliHost
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, c := range list {
		if _, err := store.Save(c.toCore()); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// saveHostStoreToFile writes the store back to disk (sorted by name).
func saveHostStoreToFile(path string, store *hostregistry.MemoryHostStore) error {
	hosts, err := store.List()
	if err != nil {
		return err
	}
	list := make([]cliHost, 0, len(hosts))
	for _, h := range hosts {
		list = append(list, fromCore(h))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// resolveCLITarget picks the target for a CLI operation. When --target <name>
// is given, it resolves a named host from the local hosts file; otherwise it
// builds an inline TargetHost from the --target-* flags (empty = local).
func resolveCLITarget(name, hostsFile, tHost string, tPort int, tUser, tPass, tKey string, tInsecure, tSudo bool) (core.TargetHost, error) {
	if name != "" {
		store, err := loadHostStoreFromFile(hostsFile)
		if err != nil {
			return core.TargetHost{}, fmt.Errorf("load hosts file %s: %w", hostsFile, err)
		}
		h, err := store.GetByName(name)
		if err != nil {
			return core.TargetHost{}, fmt.Errorf("host %q not found in %s", name, hostsFile)
		}
		return h.ToTarget(), nil
	}
	t := core.TargetHost{}
	if tHost != "" {
		t = core.TargetHost{
			Address:               tHost,
			Port:                  tPort,
			User:                  tUser,
			Password:              tPass,
			KeyPath:               tKey,
			InsecureIgnoreHostKey: tInsecure,
			Sudo:                  tSudo,
		}
	}
	return t, nil
}

// cmdPlan handles: opscore plan <operation> [-arg key=value ...] [--target-* ...]
// The operation name is the first positional arg; remaining args are generic
// key=value inputs plus optional remote-target flags.
func cmdPlan(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: operation name required (e.g. plan system.process.kill -arg pid=123)")
		os.Exit(1)
	}

	opName := args[0]
	flagArgs := args[1:]

	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	var inputs kvFlags
	fs.Var(&inputs, "arg", "operation input as key=value (repeatable, e.g. -arg pid=123)")
	name := fs.String("name", "", "service name (legacy alias; prefer -arg name=nginx)")
	tHost := fs.String("target-host", "", "remote host for this op (empty = local)")
	tPort := fs.Int("target-port", 22, "remote SSH port")
	tUser := fs.String("target-user", "root", "remote SSH user")
	tPass := fs.String("target-pass", "", "remote SSH password")
	tKey := fs.String("target-key", "", "remote SSH key path")
	tInsecure := fs.Bool("target-insecure", false, "skip host key verification (dev only)")
	tSudo := fs.Bool("target-sudo", false, "run remote command via sudo -n")
	tName := fs.String("target", "", "named host from the local hosts file (e.g. web-01)")
	hostsFile := fs.String("hosts-file", defaultHostsFile(), "local hosts JSON file")
	fs.Parse(flagArgs)

	input := map[string]any{}
	for k, v := range inputs.m {
		input[k] = v
	}
	if *name != "" {
		input["name"] = *name
	}

	target, err := resolveCLITarget(*tName, *hostsFile, *tHost, *tPort, *tUser, *tPass, *tKey, *tInsecure, *tSudo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "target error:", err)
		os.Exit(1)
	}
	ctx := core.NewContext().WithLogger(newLogger()).WithTarget(target).Build()
	_, dispatcher := setupCore()

	plan, err := dispatcher.Plan(ctx, opName, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Execution Plan (Dry Run) ===")
	fmt.Printf("Operation: %s\n", plan.OperationName)
	fmt.Printf("Permission: %s\n", plan.Permission.String())
	fmt.Printf("Risk: %s\n", plan.Risk.String())
	fmt.Printf("Steps (%d):\n", len(plan.Steps))
	for i, step := range plan.Steps {
		fmt.Printf("  %d. %s\n", i+1, step.Describe())
	}
	fmt.Println("\nNo changes made — this was a dry run.")
}

func cmdRun(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: operation name required (e.g. run system.process.kill -arg pid=123)")
		os.Exit(1)
	}

	opName := args[0]
	flagArgs := args[1:]

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var inputs kvFlags
	fs.Var(&inputs, "arg", "operation input as key=value (repeatable, e.g. -arg pid=123)")
	name := fs.String("name", "", "service name (legacy alias; prefer -arg name=nginx)")
	tHost := fs.String("target-host", "", "remote host for this op (empty = local)")
	tPort := fs.Int("target-port", 22, "remote SSH port")
	tUser := fs.String("target-user", "root", "remote SSH user")
	tPass := fs.String("target-pass", "", "remote SSH password")
	tKey := fs.String("target-key", "", "remote SSH key path")
	tInsecure := fs.Bool("target-insecure", false, "skip host key verification (dev only)")
	tSudo := fs.Bool("target-sudo", false, "run remote command via sudo -n")
	tName := fs.String("target", "", "named host from the local hosts file (e.g. web-01)")
	hostsFile := fs.String("hosts-file", defaultHostsFile(), "local hosts JSON file")
	fs.Parse(flagArgs)

	input := map[string]any{}
	for k, v := range inputs.m {
		input[k] = v
	}
	if *name != "" {
		input["name"] = *name
	}

	target, err := resolveCLITarget(*tName, *hostsFile, *tHost, *tPort, *tUser, *tPass, *tKey, *tInsecure, *tSudo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "target error:", err)
		os.Exit(1)
	}
	ctx := core.NewContext().WithLogger(newLogger()).WithTarget(target).Build()
	_, dispatcher := setupCore()

	result := dispatcher.Execute(ctx, opName, input)

	fmt.Println("=== Execution Result ===")
	if result.Success {
		fmt.Printf("Status: SUCCESS (%v)\n", result.Duration)
	} else {
		fmt.Printf("Status: FAILED (%v)\n", result.Duration)
		fmt.Printf("Error: %v\n", result.Error)
	}

	fmt.Printf("\nSteps (%d):\n", len(result.Steps))
	for i, step := range result.Steps {
		status := "OK"
		if !step.Success {
			status = "FAIL"
		}
		fmt.Printf("  %d. [%s] %s (%v)\n", i+1, status, step.StepName, step.Duration)
		if step.Output != "" {
			fmt.Printf("     output: %s\n", truncate(step.Output, 200))
		}
	}
}

// cmdHost manages the CLI's local Host Registry (a file-backed store that
// mirrors the server's HostStore). It lets operators register named targets
// once and reference them via `run --target <name>` / `plan --target <name>`.
func cmdHost(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: host subcommand required (list | add | remove)")
		os.Exit(1)
	}
	path := defaultHostsFile()

	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("host-list", flag.ExitOnError)
		fs.StringVar(&path, "file", path, "hosts JSON file")
		_ = fs.Parse(args[1:])
		store, err := loadHostStoreFromFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		hosts, _ := store.List()
		if len(hosts) == 0 {
			fmt.Printf("(no hosts registered in %s)\n", path)
			return
		}
		fmt.Printf("%-20s %-30s %-6s %-10s %s\n", "NAME", "ADDRESS", "PORT", "USER", "GROUPS")
		for _, h := range hosts {
			fmt.Printf("%-20s %-30s %-6d %-10s %s\n",
				h.Name, h.Target.Address, h.Target.PortOrDefault(), h.Target.User, strings.Join(h.Groups, ","))
		}

	case "add":
		fs := flag.NewFlagSet("host-add", flag.ExitOnError)
		name := fs.String("name", "", "host name (required)")
		addr := fs.String("address", "", "SSH address (required)")
		port := fs.Int("port", 22, "SSH port")
		user := fs.String("user", "root", "SSH user")
		pass := fs.String("password", "", "SSH password")
		key := fs.String("key-path", "", "path to PEM private key")
		insecure := fs.Bool("insecure", false, "skip host key verification (dev only)")
		sudo := fs.Bool("sudo", false, "run remote commands via sudo -n")
		groups := fs.String("group", "", "comma-separated groups (for batch fan-out)")
		fs.StringVar(&path, "file", path, "hosts JSON file")
		_ = fs.Parse(args[1:])
		if *name == "" || *addr == "" {
			fmt.Fprintln(os.Stderr, "error: --name and --address are required")
			os.Exit(1)
		}
		var grp []string
		if *groups != "" {
			for _, g := range strings.Split(*groups, ",") {
				if g = strings.TrimSpace(g); g != "" {
					grp = append(grp, g)
				}
			}
		}
		store, err := loadHostStoreFromFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if _, err := store.Save(hostregistry.Host{
			Name: *name,
			Target: core.TargetHost{
				Address:               *addr,
				Port:                  *port,
				User:                  *user,
				Password:              *pass,
				KeyPath:               *key,
				InsecureIgnoreHostKey: *insecure,
				Sudo:                  *sudo,
			},
			Groups: grp,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err := saveHostStoreToFile(path, store); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("host %q saved to %s\n", *name, path)

	case "remove":
		fs := flag.NewFlagSet("host-remove", flag.ExitOnError)
		name := fs.String("name", "", "host name to remove (required)")
		fs.StringVar(&path, "file", path, "hosts JSON file")
		_ = fs.Parse(args[1:])
		if *name == "" {
			fmt.Fprintln(os.Stderr, "error: --name is required")
			os.Exit(1)
		}
		store, err := loadHostStoreFromFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err := store.Delete(*name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err := saveHostStoreToFile(path, store); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("host %q removed from %s\n", *name, path)

	default:
		fmt.Fprintf(os.Stderr, "unknown host subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func printUsage() {
	fmt.Println(`OpsCore — Operations Control Plane

Usage:
  opscore <command> [flags]

Commands:
  list              List all registered operations
  plan <op>         Show execution plan (dry-run, no changes made)
  run <op>          Execute operation
  host <sub>        Manage the local Host Registry (list | add | remove)
  serve             Run the Control Plane HTTP API (Phase 1.5)
  help              Show this help

Examples:
  opscore list
  opscore plan system.service.restart -arg name=nginx
  opscore run system.process.kill -arg pid=123 -arg signal=KILL
  opscore host add --name web-01 --address 192.168.94.20 --user root --insecure --group web
  opscore run system.host.info --target web-01
  opscore serve --addr :8080 --storage memory --admin-user admin --admin-pass admin
  opscore serve --target-host 192.168.94.20 --target-user root --target-pass '***' --target-insecure

Host Registry (Phase 2.3):
  Register named targets once (CLI 'host add' or POST /api/hosts), then run
  operations against them by name: --target <name> (CLI) or "target":"<name>"
  (API). Groups enable Phase 2.5 Batch fan-out. Secrets are never echoed back.

Architecture:
  CLI -> Dispatcher -> Handler.Plan -> ExecutionPlan -> Executor -> CommandStep -> (local | SSH) -> AuditSink
  HTTP: Bearer JWT -> RBAC Authorize -> core.Context -> Dispatcher
  Remote: core.Context.Target => CommandStep runs over SSH (golang.org/x/crypto/ssh)

Phase: 0 (Core) + Phase 1 (Control Plane) + Phase 2 (Builtin modules: host/process)`)
}
