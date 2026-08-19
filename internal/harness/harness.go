package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/YuDong999/opscore/internal/cluster"
	"github.com/YuDong999/opscore/internal/clusterprojection"
	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/enterprise"
	"github.com/YuDong999/opscore/internal/external"
	"github.com/YuDong999/opscore/internal/governance"
	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/management"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/platformview"
	"github.com/YuDong999/opscore/internal/storage/sqlite"
	"github.com/YuDong999/opscore/internal/tracing"
)

// capabilities bundles the real, assembled capability instances. The Harness holds these
// REFERENCES only (MUST-3) — it never copies their domain state.
type capabilities struct {
	obs     *observability.Collector
	cl      *cluster.Manager
	ent     *enterprise.Service
	gov     *governance.Engine
	polRepo governancepolicy.Repository
	// traceRing is the bounded causal-trace span store (Phase 20, ADR-045). It is
	// shared by the tracing adapter (bridge) and the management read surface so a
	// span ingested by the bridge is exactly what the read surface serves.
	traceRing *tracing.TraceRing
	// bridge is the Phase 20 causal-tracing adapter over obs + traceRing. The
	// harness owns it as the single ingestion point; the controlplane execution
	// bus (a separate process in the deployment topology) subscribes to it.
	bridge *observability.TracingBridge
}

// Harness is the sole composition root (SHOULD-1). It holds no domain state of its own (MUST-3):
// only the assembled read Server, the operational probe server, and the transport. It assembles;
// it does not become a capability.
type Harness struct {
	server *external.Server
	probe  *http.Server
	cap    *capabilities
	pv     *platformview.Facade
	corr   *correlation.Correlator
	cfg    HarnessConfig
	http   *http.Server
	// mgmt is the management/v1 write bind. It is NIL when no token was
	// configured — not a server that rejects everything, but no server at all
	// (MUST-P17-14). Every use site therefore has to confront the nil, which is
	// exactly the reminder we want: the write surface is optional infrastructure.
	mgmt *http.Server
	// auditDB backs the management audit log; nil whenever mgmt is nil.
	auditDB *sqlite.SQLiteStorage
	// shutdownOnce makes Shutdown idempotent (SHOULD-8).
	shutdownOnce sync.Once
	shutdownErr  error
}

// Build constructs the real Reader graph from the frozen capabilities, injects it into
// external.Server, mounts the operational probe server, and returns a ready-to-serve Harness.
// It creates NO execution entry (MUST-0): every constructed piece is a read query API or a
// transport.
func Build(ctx context.Context, cfg HarnessConfig) (*Harness, error) {
	// fail-closed: validate operational config before any wiring (ADR-032 §3.6).
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// PolicyStoreDir wiring: select the EXISTING governancepolicy.Repository location
	// (A-5). NewFileRepository creates the dir and fails closed on an empty path
	// (ErrInvalidID). This also fixes the previously-unwired polRepo — it was never
	// assigned to capabilities, leaving the governance reader nil in production.
	storeDir := cfg.PolicyStoreDir
	if storeDir == "" {
		storeDir = defaultPolicyStoreDir
	}
	polRepo, err := governancepolicy.NewFileRepository(storeDir)
	if err != nil {
		return nil, fmt.Errorf("policy store init %q: %w", storeDir, err)
	}

	obs := observability.NewCollector()
	ring := tracing.NewTraceRing(tracing.DefaultTraceRingCapacity)
	cap := &capabilities{
		obs:       obs,
		cl:        cluster.NewManager(),
		ent:       enterprise.NewService(),
		gov:       governance.NewEngine(),
		polRepo:   polRepo,
		traceRing: ring,
		bridge:    observability.NewTracingBridge(obs, ring),
	}

	pvReaders, corrReaders := realReaders(cap)

	pv := platformview.New(pvReaders)
	corr := correlation.New(corrReaders)

	// nil authn → external.NoAuthAuthenticator (v1 single-tenant stub, ADR-024 MUST-5).
	srv := external.NewServer(pv, corr, nil)

	h := &Harness{
		server: srv,
		cap:    cap,
		pv:     pv,
		corr:   corr,
		cfg:    cfg,
		http:   &http.Server{Addr: listenAddr(cfg), Handler: newRouter(srv)},
	}
	// Probe server is mounted AFTER h is constructed so it can observe the
	// already-built read models (A-3). Separate bind from external/v1 (A-10).
	h.probe = &http.Server{Addr: probeAddr(cfg), Handler: h.newProbeRouter()}

	if err := h.assembleManagement(cfg, polRepo); err != nil {
		return nil, err
	}
	return h, nil
}

// assembleManagement wires the management/v1 write surface, or deliberately
// wires nothing (ADR-036 §3.6, MUST-P17-14).
//
// The two outcomes are asymmetric on purpose:
//
//   - No token ⇒ NO surface, and that is a SUCCESS. A read-only deployment is a
//     legitimate, complete deployment; refusing to boot would be punishing the
//     safe configuration.
//   - Token present but the audit store cannot be opened ⇒ Build FAILS. The
//     operator explicitly asked for the write surface; the only alternatives
//     would be to serve writes with no durable audit (violating MUST-P17-13) or
//     to silently drop the surface they asked for. Both are worse than a boot
//     error that names the problem.
//
// Ordering matters here as well: the audit store opens BEFORE the authenticator,
// so a deployment can never reach a state where writes are authenticated and
// accepted while their intent rows have nowhere to land.
func (h *Harness) assembleManagement(cfg HarnessConfig, polRepo governancepolicy.Repository) error {
	if !managementEnabled(cfg) {
		return nil
	}

	auditPath := auditStorePath(cfg)
	if dir := filepath.Dir(auditPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("audit store dir %q: %w", dir, err)
		}
	}
	db, err := sqlite.NewSQLiteStorage(auditPath)
	if err != nil {
		return fmt.Errorf("management audit store %q: %w", auditPath, err)
	}

	authn, err := management.NewTokenAuthenticator(cfg.ManagementToken, managementPrincipal(cfg))
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("management authn: %w", err)
	}

	srv, err := management.New(management.Config{
		Repo:          polRepo,
		Audit:         db.Audit(),
		Authenticator: authn,
		// Phase 19 (ADR-042 §3.1): the exact-aggregate metrics collector is a
		// required dependency. Reuse the SAME instance the host sinks feed
		// (h.cap.obs) so /management/v1/metrics reflects real observations
		// rather than an always-empty collector.
		Collector: h.cap.obs,
		// Phase 20 (ADR-045 §5, R20-10): the bounded trace ring is OPTIONAL at
		// the surface level but the harness always wires one, so the traces
		// read surface serves exactly the spans the bridge ingests. A nil ring
		// here would make the surface answer 503 evidence_unavailable.
		TraceRing: h.cap.traceRing,
	})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("management surface: %w", err)
	}

	h.auditDB = db
	h.mgmt = &http.Server{Addr: managementAddr(cfg), Handler: srv.Handler()}

	// Best-effort, non-blocking startup reconciliation scan (ADR-038 §3.5).
	// It reads only and must never block :8082 from binding or fail the
	// process; errors are logged by the surface. There is no scheduler,
	// worker, or controller — reconciliation is on-demand (GET) or this one
	// startup pass.
	go srv.ScanAtStartup(context.Background())
	return nil
}

// realReaders builds the REAL, non-nil Reader implementations from the frozen capabilities
// (SHOULD-2 — no nil-Readers in production). The same concrete adapter satisfies both the
// platformview and the correlation reader interface for its capability.
func realReaders(cap *capabilities) (platformview.Readers, correlation.Readers) {
	obs := &observabilityAdapter{c: cap.obs}
	cl := clusterprojection.NewReader(cap.cl)
	ent := &enterpriseAdapter{s: cap.ent}
	gov := governancepolicy.NewReader(cap.polRepo)

	return platformview.Readers{Obs: obs, Cluster: cl, Enterprise: ent, Governance: gov},
		correlation.Readers{Obs: obs, Cluster: cl, Enterprise: ent, Governance: gov}
}

// Serve mounts both HTTP surfaces and blocks until the first server errors or
// Shutdown is called:
//   - external/v1 (read contract, unchanged)
//   - operational probe bind (health/readiness/version, separate bind)
//
// It is deployment lifecycle only (SHOULD-5). Cancelling ctx triggers a graceful
// Shutdown (drain + flush).
func (h *Harness) Serve(ctx context.Context) error {
	// Tie the process deployment lifecycle to the supplied context: cancelling ctx
	// shuts both servers down gracefully (SHOULD-5 / lifecycle ownership stays in the Harness).
	go func() {
		<-ctx.Done()
		_ = h.Shutdown(context.Background())
	}()

	// The management bind joins the set only when it was assembled. Deriving the
	// list from the assembled servers (instead of a hard-coded count) is what
	// keeps "no token ⇒ nothing is listening on :8082" true at RUNTIME and not
	// just at construction time.
	servers := h.boundServers()
	errc := make(chan error, len(servers))
	for _, s := range servers {
		s := s
		go func() { errc <- s.ListenAndServe() }()
	}

	// Return on the FIRST non-graceful error. A graceful close (ErrServerClosed)
	// is expected when Shutdown runs; consume them all and return nil.
	for range servers {
		if err := <-errc; err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}

// boundServers lists the HTTP surfaces this deployment actually listens on.
func (h *Harness) boundServers() []*http.Server {
	servers := []*http.Server{h.http, h.probe}
	if h.mgmt != nil {
		servers = append(servers, h.mgmt)
	}
	return servers
}

// Shutdown is deployment-lifecycle only (SHOULD-5) and idempotent (SHOULD-8):
// repeated calls return the first result and never produce a secondary side
// effect. It drains in-flight reads (both servers stop accepting NEW requests)
// and flushes the policy store (A-4). It is NEVER mapped to Execution Cancel /
// Apply / capability operations — no new execution entry (P15-0/A-4).
func (h *Harness) Shutdown(ctx context.Context) error {
	h.shutdownOnce.Do(func() {
		var firstErr error
		for _, s := range h.boundServers() {
			if err := s.Shutdown(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		// Close the audit DB only AFTER the management listener has drained.
		// Reversing this would let an in-flight write find the audit store gone
		// mid-request — turning a clean shutdown into precisely the unaudited
		// mutation MUST-P17-13 forbids.
		if h.auditDB != nil {
			if err := h.auditDB.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		// Flush the existing PolicyStoreDir Repository. The file-backed
		// implementation needs no close; closeIfNeeded honours the contract only
		// if a future backend implements it (no lifecycle-semantics change, A-5).
		if err := closeIfNeeded(h.cap.polRepo); err != nil && firstErr == nil {
			firstErr = err
		}
		h.shutdownErr = firstErr
	})
	return h.shutdownErr
}

// closeIfNeeded flushes the repository only if it implements a Close method. It
// is a safe, no-op-preserving bridge that does NOT form an execution path (A-5).
func closeIfNeeded(repo governancepolicy.Repository) error {
	type closer interface{ Close() error }
	if c, ok := repo.(closer); ok {
		return c.Close()
	}
	return nil
}

// ExternalAddr returns the effective external/v1 listen address (for diagnostics).
func (h *Harness) ExternalAddr() string { return h.http.Addr }

// ProbeAddr returns the effective operational probe bind (for diagnostics).
func (h *Harness) ProbeAddr() string { return h.probe.Addr }

// ManagementAddr returns the management/v1 write bind, or "" when the surface was
// not assembled. The empty string is the honest answer: there is no address,
// because there is no listener (MUST-P17-14).
func (h *Harness) ManagementAddr() string {
	if h.mgmt == nil {
		return ""
	}
	return h.mgmt.Addr
}

// ManagementEnabled reports whether the write surface was assembled.
func (h *Harness) ManagementEnabled() bool { return h.mgmt != nil }
