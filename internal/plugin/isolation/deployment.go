package isolation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/internal/core"
)

// Deployment describes how to run ONE plugin operation out-of-process, as a
// HELPER BINARY.
//
// This is HOST-SIDE deployment policy, NOT part of the plugin Manifest (Round
// 28 ruling (b)): whether a plugin runs in-process or in a helper process is a
// deployment decision, not part of the plugin's identity. The Runtime Contract
// is therefore unchanged — Manager / Registry / Reload / Watcher never import
// this package and only ever receive the core.Handler produced below.
type Deployment struct {
	Operation string `json:"operation"`

	// Path is the helper executable; Args/Env/Dir are passed through verbatim.
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
	Env  []string `json:"env,omitempty"`
	Dir  string   `json:"dir,omitempty"`

	// ExecTimeoutSeconds bounds the whole invocation (0 => DefaultExecTimeout).
	// On expiry the helper is KILLED (MUST-3).
	ExecTimeoutSeconds float64 `json:"execTimeoutSeconds,omitempty"`
	// MaxResponseMB caps the declared response frame size (0 => default).
	MaxResponseMB int64 `json:"maxResponseMB,omitempty"`
}

// DeploymentMap is an operation-keyed registry of helper deployments, built by
// the host at startup (from a config file, environment, or code). It is the
// single mechanism that lets an existing plugin be moved out-of-process
// WITHOUT editing its Manifest.
type DeploymentMap map[string]Deployment

// Handler returns a process-isolated core.Handler for operation, or
// (nil, false) when no deployment maps it. In the latter case the caller
// should fall back to the plugin's ordinary in-process handler — Manager never
// needs to know which path was taken.
func (m DeploymentMap) Handler(operation string) (core.Handler, bool) {
	d, ok := m[operation]
	if !ok {
		return nil, false
	}
	cfg := Config{
		Path: d.Path,
		Args: d.Args,
		Env:  d.Env,
		Dir:  d.Dir,
	}
	if d.ExecTimeoutSeconds > 0 {
		cfg.ExecTimeout = time.Duration(d.ExecTimeoutSeconds * float64(time.Second))
	}
	if d.MaxResponseMB > 0 {
		cfg.MaxResponseBytes = d.MaxResponseMB << 20
	}
	return NewHandler(operation, cfg), true
}

// LoadDeploymentMap reads a DeploymentMap (JSON) from path. JSON keeps the
// package stdlib-only (offline constraint unchanged); a host that sources
// deployments from another config system can build the same struct directly.
func LoadDeploymentMap(path string) (DeploymentMap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m DeploymentMap
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// AddFromPackage bridges a loaded Package into this host-side deployment map.
// It is the ONLY point where a Package influences how an operation runs.
//
// It resolves the package-relative executable/working-dir against the package
// directory and inserts a Deployment — the same struct that Handler() turns
// into a core.Handler. That makes the bridge Phase 7.2 MUST-3 concrete:
// Unpack (Load) -> DeploymentMap (this method) -> Run (Handler()) never
// mutates the Runtime Manifest, Manager, Registry, Reload or Watcher. The
// visible output is a Deployment whose Handler() yields a core.Handler
// indistinguishable from an in-process plugin's, so the Runtime Contract is
// untouched.
func (m DeploymentMap) AddFromPackage(op string, pkg *packaging.Package) error {
	rs, ok := pkg.RunSpec(op)
	if !ok {
		return fmt.Errorf("isolation: package %q does not declare operation %q", pkg.Name, op)
	}
	exe := rs.Executable
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(pkg.Dir(), exe)
	}
	dir := rs.Dir
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(pkg.Dir(), dir)
	}
	m[op] = Deployment{
		Operation:          op,
		Path:               exe,
		Args:               rs.Args,
		Env:                rs.Env,
		Dir:                dir,
		ExecTimeoutSeconds: rs.ExecTimeoutSeconds,
		MaxResponseMB:      rs.MaxResponseMB,
	}
	return nil
}
