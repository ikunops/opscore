// Package packaging defines the on-disk DISTRIBUTION unit for a third-party
// executable plugin. It is deliberately separate from the Runtime Manifest
// (manifest.json) and from the frozen Runtime Contract.
//
// Boundary frozen by Phase 7.2 (GPT Round 31 directive):
//
//	MUST-1  A Package is a DISTRIBUTION unit, NOT the Runtime Manifest. Its
//	        metadata never becomes part of a plugin's Runtime identity.
//	MUST-2  It reuses ecosystem/sdk and the opscore.isolation/v1 protocol; it
//	        introduces no new wire format.
//	MUST-3  Unpack -> DeploymentMap -> Run does NOT modify the Runtime. The
//	        isolation layer's AddFromPackage is the only bridge, and it emits a
//	        core.Handler — the same thing an in-process plugin emits.
//	MUST-4  It is SOURCE-AGNOSTIC: a Package is identical however it arrived
//	        (File / Git / OCI). Load(dir) only ever reads the unpacked directory.
//	MUST-5  Package metadata (plugin.json) is kept separate from the Runtime
//	        Manifest (manifest.json).
//
// Explicitly out of scope (forbidden): Registry Server / Marketplace / OCI
// push-pull / auto-install-upgrade / dependency resolution / .so / WASM.
//
// This package depends only on the standard library and ecosystem/sdk, so a
// packaging tool compiles without the Runtime (mirrors Phase 7.1 MUST-4:
// the SDK is the single source of truth for the protocol).
package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/YuDong999/opscore/ecosystem/sdk"
)

// manifestFile is the package metadata filename. It is kept distinct from the
// Runtime manifest.json (MUST-5): the Runtime never reads plugin.json and the
// package never reads manifest.json.
const manifestFile = "plugin.json"

// Package is the distribution metadata for one third-party executable plugin,
// parsed from plugin.json inside an unpacked package directory.
type Package struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	SDKVersion  string `json:"sdkVersion"`
	Description string `json:"description,omitempty"`

	// Operations maps an operation id to its in-package deployment descriptor.
	Operations map[string]RunSpec `json:"operations"`

	// Checksums maps a relative executable path to its expected hex sha256.
	// Optional: when present, Validate verifies each matching executable.
	Checksums map[string]string `json:"checksums,omitempty"`

	// dir is the unpacked directory this Package was loaded from. It resolves
	// package-relative executable/working-dir paths and is filled by Load.
	dir string
}

// RunSpec is the per-operation deployment descriptor inside a package. It
// parallels the host-side isolation.Deployment struct but is owned by the
// package format and may carry package-relative paths.
type RunSpec struct {
	Executable         string   `json:"executable"`
	Args               []string `json:"args,omitempty"`
	Env                []string `json:"env,omitempty"`
	Dir                string   `json:"dir,omitempty"`
	ExecTimeoutSeconds float64  `json:"execTimeoutSeconds,omitempty"`
	MaxResponseMB      int64    `json:"maxResponseMB,omitempty"`
}

// Dir returns the unpacked directory this Package was loaded from. The host
// bridge resolves relative RunSpec paths against it.
func (p *Package) Dir() string { return p.dir }

// Load reads plugin.json from dir and parses it. It does NOT validate: call
// Validate once dir is the final unpack location (after any checksum-bearing
// transport has placed the files). Load is source-agnostic (MUST-4) — the
// bytes could have arrived via File, Git or OCI; only the result matters.
func Load(dir string) (*Package, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return nil, fmt.Errorf("packaging: read %s: %w", manifestFile, err)
	}
	var p Package
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("packaging: parse %s: %w", manifestFile, err)
	}
	p.dir = dir
	return &p, nil
}

// Validate enforces the package-level contract against the unpacked directory:
//
//   - sdkVersion must equal the protocol the SDK speaks (MUST-2);
//   - every operation must name an executable that exists after unpack;
//   - when declared, each executable's sha256 must match Checksums.
//
// Validate never touches the Runtime Contract (MUST-3) — it only stat()s files
// and hashes bytes on disk.
func (p *Package) Validate() error {
	if p.SDKVersion != sdk.ProtocolVersion {
		return fmt.Errorf("packaging: unsupported sdkVersion %q (this host speaks %q)",
			p.SDKVersion, sdk.ProtocolVersion)
	}
	if len(p.Operations) == 0 {
		return fmt.Errorf("packaging: package %q declares no operations", p.Name)
	}
	for op, rs := range p.Operations {
		if rs.Executable == "" {
			return fmt.Errorf("packaging: operation %q has empty executable", op)
		}
		exe := rs.Executable
		if !filepath.IsAbs(exe) {
			exe = filepath.Join(p.dir, exe)
		}
		if _, err := os.Stat(exe); err != nil {
			return fmt.Errorf("packaging: operation %q executable %q missing: %w",
				op, rs.Executable, err)
		}
		if want, ok := p.Checksums[rs.Executable]; ok {
			got, err := sha256OfFile(exe)
			if err != nil {
				return fmt.Errorf("packaging: operation %q checksum read: %w", op, err)
			}
			if !strings.EqualFold(got, want) {
				return fmt.Errorf("packaging: operation %q executable %q checksum mismatch (want %s, got %s)",
					op, rs.Executable, want, got)
			}
		}
	}
	return nil
}

// RunSpec returns the deployment descriptor for operation, or (zero, false) if
// the package does not declare it. The host resolves relative paths against
// Dir via the isolation bridge (MUST-3).
func (p *Package) RunSpec(operation string) (RunSpec, bool) {
	rs, ok := p.Operations[operation]
	return rs, ok
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
