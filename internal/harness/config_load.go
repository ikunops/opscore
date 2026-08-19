package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// configSchemaVersion is the only supported deployment-config schema stamp.
// Unknown / unsupported values are rejected at load time (fail-closed, ADR-032 §3.6).
const configSchemaVersion = "1"

// rawConfig mirrors the on-disk JSON deployment config (ADR-032 §3.1). It is a
// pure operational description — NO Runtime / Policy / Plugin semantic switch
// is expressible through it (A-2).
type rawConfig struct {
	Version string `json:"version"`
	Server  struct {
		Listen string `json:"listen"`
		Probe  string `json:"probe"`
	} `json:"server"`
	Log struct {
		Level  string `json:"level"`
		Format string `json:"format"`
	} `json:"log"`
	Storage struct {
		PolicyStoreDir string `json:"policyStoreDir"`
		AuditStorePath string `json:"auditStorePath"`
	} `json:"storage"`
	// Management carries the write surface's DEPLOYMENT parameters only.
	//
	// There is deliberately no token field here. Combined with
	// DisallowUnknownFields, that makes a secret in the config file a PARSE ERROR
	// rather than a code-review finding — the credential is structurally
	// unrepresentable in a file that tends to get committed. It comes from
	// EnvManagementToken.
	Management struct {
		Listen    string `json:"listen"`
		Principal string `json:"principal"`
	} `json:"management"`
}

// DefaultConfig returns a HarnessConfig populated with all operational defaults.
// It is used for flag-only deployments (no on-disk config file).
func DefaultConfig() HarnessConfig {
	return HarnessConfig{
		ListenAddr:          defaultListenAddr,
		ProbeAddr:           defaultProbeAddr,
		PolicyStoreDir:      defaultPolicyStoreDir,
		Version:             configSchemaVersion,
		Logging:             LoggingConfig{Level: "info", Format: "text"},
		ManagementAddr:      defaultManagementAddr,
		ManagementPrincipal: defaultManagementPrincipal,
		AuditStorePath:      defaultAuditStorePath,
		// ManagementToken is intentionally absent: defaulting a credential would
		// make the write surface appear by accident. Callers opt in explicitly,
		// normally via LoadConfig reading EnvManagementToken.
	}
}

// LoadConfig reads, parses, and validates a deployment config file (JSON). It is
// fail-closed (ADR-032 §3.6):
//   - unknown keys are rejected (DisallowUnknownFields);
//   - an unsupported schema version is rejected;
//   - invalid log level / format values are rejected.
//
// On success it returns a fully-defaulted, validated HarnessConfig. It never
// opens an execution path or mutates capability semantics (A-2).
func LoadConfig(path string) (HarnessConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HarnessConfig{}, fmt.Errorf("%w: read %s: %v", ErrInvalidConfig, path, err)
	}
	var raw rawConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return HarnessConfig{}, fmt.Errorf("%w: parse %s: %v", ErrInvalidConfig, path, err)
	}

	cfg := DefaultConfig()
	if raw.Version != "" {
		cfg.Version = raw.Version
	}
	cfg.ListenAddr = raw.Server.Listen
	cfg.ProbeAddr = raw.Server.Probe
	cfg.Logging.Level = raw.Log.Level
	cfg.Logging.Format = raw.Log.Format
	cfg.PolicyStoreDir = raw.Storage.PolicyStoreDir
	cfg.AuditStorePath = raw.Storage.AuditStorePath
	cfg.ManagementAddr = raw.Management.Listen
	cfg.ManagementPrincipal = raw.Management.Principal
	// The credential comes from the environment, never from the file (ADR-036
	// §3.1). Absent ⇒ the write surface is not assembled at all (MUST-P17-14),
	// so "no token configured" is a complete, valid, read-only deployment rather
	// than a misconfiguration to report.
	cfg.ManagementToken = os.Getenv(EnvManagementToken)

	if err := cfg.Validate(); err != nil {
		return HarnessConfig{}, err
	}
	return cfg, nil
}

// Validate enforces the operational-only contract of HarnessConfig (ADR-032 A-2).
// It inspects ONLY deployment parameters — never Runtime / Policy / Plugin
// semantics.
func (c HarnessConfig) Validate() error {
	if c.Version != "" && c.Version != configSchemaVersion {
		return fmt.Errorf("%w: unsupported config schema version %q (want %q)",
			ErrInvalidConfig, c.Version, configSchemaVersion)
	}
	switch c.Logging.Level {
	case "", "info", "debug", "warn":
		// accepted
	default:
		return fmt.Errorf("%w: invalid log.level %q (want info|debug|warn)",
			ErrInvalidConfig, c.Logging.Level)
	}
	switch c.Logging.Format {
	case "", "json", "text":
		// accepted
	default:
		return fmt.Errorf("%w: invalid log.format %q (want json|text)",
			ErrInvalidConfig, c.Logging.Format)
	}
	return nil
}
