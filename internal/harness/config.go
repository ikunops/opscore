// Package harness is the Phase 12.2 Deployment Harness — the sole recommended
// composition root (ADR-026 SHOULD-1) over the frozen six-tier baseline.
//
// It ASSEMBLES; it does not BECOME a capability. Per ADR-026 the Harness:
//   - MUST-0: opens no execution path, mutates no capability semantics, is not a Control Plane.
//   - MUST-1: imports only public facade / external / capability READ APIs (never the frozen
//     execution path: core/execution, plugin/runtime, plugin/isolation, controlplane/*,
//     builtin/*); it performs DI wiring only.
//   - MUST-2: owns no capability; composes existing interfaces via Readers.
//   - MUST-3: replicates no domain state — it holds only references to the capability instances
//     it assembled (the capabilities own their own stores; the Harness never copies them).
//   - MUST-4: mounts external/v1 only; it does not own or evolve that contract.
//   - MUST-5: single-node is normative; multi-node is a reserved, inert seam.
//
// Reader wiring (SHOULD-2): the Harness injects REAL, non-nil Reader
// implementations built from the frozen capabilities. observability / enterprise have real
// query backends and are delegated truthfully; cluster / governance currently expose no
// host-centric / policy-storage query API (cluster is ClusterID-scoped, governance.Engine is a
// stateless evaluator with no store), so their adapters are real (non-nil) types that return
// empty views — honestly reflecting the frozen API shape without modifying the closed packages.
package harness

import (
	"strings"
	"time"
)

// HarnessConfig carries DEPLOYMENT parameters only (ADR-026 SHOULD-4). It MUST NOT change
// Runtime / Policy semantics — it configures the wiring and the transport, nothing else.
type HarnessConfig struct {
	// ListenAddr is the HTTP listen address for the external/v1 read contract (e.g. ":8080").
	ListenAddr string
	// EnabledSurfaces lists the mounted read contracts. v1 ships only "external/v1".
	EnabledSurfaces []string
	// Storage is reserved for the multi-node seam (shared external state). Inert in v1.
	Storage StorageConfig
	// Logging carries deployment logging parameters only.
	Logging LoggingConfig
	// PolicyStoreDir is the directory backing the Governance Policy Repository
	// (Phase 14, ADR-029/030). It is an explicit, single-owner store (B6); when
	// empty a sane default is used so the deployment always has a concrete,
	// traceable policy store rather than implicit in-memory state.
	PolicyStoreDir string
	// Version is the deployment-config schema stamp (Phase 15.2, ADR-032). It is
	// operational metadata only — never a Runtime / Policy / Plugin semantic
	// switch (A-2). Unsupported values are rejected at load time (fail-closed).
	Version string
	// ProbeAddr is the operational health/readiness/version bind (e.g. ":8081"),
	// SEPARATE from the external/v1 contract (A-10). Empty → defaultProbeAddr.
	ProbeAddr string

	// ManagementAddr is the management/v1 WRITE bind (e.g. ":8082"), separate from
	// both external/v1 and the probe (ADR-036 §3.6). Empty →
	// defaultManagementAddr. It is only ever bound when ManagementToken is set.
	ManagementAddr string

	// ManagementToken is the shared secret for the management/v1 write surface.
	//
	// EMPTY MEANS THE SURFACE DOES NOT EXIST: no listener, no routes, nothing to
	// probe (MUST-P17-14). This is deliberately stronger than registering the
	// routes behind a guard that always answers 401 — an always-401 surface is
	// still a surface, and it advertises that policy mutation lives here.
	//
	// It is NOT loadable from the deployment config file. rawConfig has no field
	// for it and LoadConfig rejects unknown keys, so writing a token into the JSON
	// is not merely discouraged — it fails to parse. The value comes from
	// EnvManagementToken instead, because config files get committed, baked into
	// images, and copied between environments; the process environment does not.
	ManagementToken string

	// ManagementPrincipal is the audit actor recorded for authenticated writes.
	// Empty → defaultManagementPrincipal. With a single shared token there is
	// exactly one identity, and the audit log says so plainly rather than
	// inventing per-request identities it cannot substantiate.
	ManagementPrincipal string

	// AuditStorePath is the SQLite file backing the management audit log. It must
	// be durable: MUST-P17-13's intent row is only evidence of a crash window if
	// it survives the crash, so an in-memory audit store would quietly void the
	// guarantee the intent/outcome protocol exists to provide.
	// Empty → defaultAuditStorePath.
	AuditStorePath string
}

// EnvManagementToken is the only supported source for the management/v1 shared
// secret. See HarnessConfig.ManagementToken for why it is not a config key.
const EnvManagementToken = "OPSCORE_MANAGEMENT_TOKEN"

// StorageConfig is reserved for the multi-node seam. It is inert in single-node v1 (SHOULD-3).
type StorageConfig struct {
	Backend string // e.g. "memory" (v1), "postgres" (reserved)
	DSN     string // connection string for a shared external-state store (reserved)
}

// LoggingConfig carries deployment logging parameters only — never a capability behavior switch.
type LoggingConfig struct {
	Level  string // "info" | "debug" | "warn"
	Format string // "json" | "text" — observability only (A-7)
}

// defaultListenAddr is used when HarnessConfig.ListenAddr is empty.
const defaultListenAddr = ":8080"

// defaultProbeAddr is used when HarnessConfig.ProbeAddr is empty. It serves the
// operational health/readiness/version bind, separate from external/v1 (A-10).
const defaultProbeAddr = ":8081"

// defaultPolicyStoreDir is used when HarnessConfig.PolicyStoreDir is empty. It
// is a concrete, traceable on-disk store — never implicit in-memory state (B6).
const defaultPolicyStoreDir = ".opscore/policies"

// defaultManagementAddr is the management/v1 write bind (ADR-036 §1). Note there
// is deliberately no default TOKEN to pair with it: an address has a safe
// default, a credential never does.
const defaultManagementAddr = ":8082"

// defaultAuditStorePath is the durable audit log used when AuditStorePath is empty.
const defaultAuditStorePath = ".opscore/audit.db"

// defaultManagementPrincipal is the audit actor for the single shared-token identity.
const defaultManagementPrincipal = "management-operator"

// listenAddr resolves the effective external/v1 HTTP listen address.
func listenAddr(cfg HarnessConfig) string {
	if cfg.ListenAddr == "" {
		return defaultListenAddr
	}
	return cfg.ListenAddr
}

// probeAddr resolves the effective operational health/readiness bind (A-10).
func probeAddr(cfg HarnessConfig) string {
	if cfg.ProbeAddr == "" {
		return defaultProbeAddr
	}
	return cfg.ProbeAddr
}

// managementAddr resolves the effective management/v1 write bind (ADR-036 §3.6).
func managementAddr(cfg HarnessConfig) string {
	if cfg.ManagementAddr == "" {
		return defaultManagementAddr
	}
	return cfg.ManagementAddr
}

// auditStorePath resolves the effective durable audit log location.
func auditStorePath(cfg HarnessConfig) string {
	if cfg.AuditStorePath == "" {
		return defaultAuditStorePath
	}
	return cfg.AuditStorePath
}

// managementPrincipal resolves the audit actor for the shared-token identity.
func managementPrincipal(cfg HarnessConfig) string {
	if cfg.ManagementPrincipal == "" {
		return defaultManagementPrincipal
	}
	return cfg.ManagementPrincipal
}

// managementEnabled reports whether the write surface should be assembled at all.
//
// The predicate is the TOKEN, not an "enabled" flag, and that is the point: a
// separate boolean would let a deployment ask for the surface without supplying
// a credential, and something would then have to decide what to do about the
// contradiction — bind insecurely, or bind and refuse everything. Deriving the
// answer from the credential removes the contradictory state from the config
// space entirely (MUST-P17-14).
func managementEnabled(cfg HarnessConfig) bool {
	return strings.TrimSpace(cfg.ManagementToken) != ""
}

// now is the clock the Harness exposes to its wiring (kept injectable for tests; unused today).
var now = time.Now
