// Package tooling is the offline Packaging / Release Tool (Phase 7.3). It turns
// a plugin's Go source into a verified package directory (dist/) containing the
// built helper executable(s), plugin.json, and release.json.
//
// Boundary frozen by GPT Round 32:
//
//	MUST-1  It reuses ecosystem/sdk and ecosystem/packaging — it introduces no
//	        new protocol or package format; it only composes them.
//	MUST-2  It must NEVER touch the Runtime Core (Manifest / Provider / Loader /
//	        Manager / Compatibility / Execution stay frozen). It emits a
//	        packaging.Package that the host later bridges via the single legal
//	        entry isolation.AddFromPackage.
//	MUST-3  The Builder composes plugin.json; the CLI must never hand-assemble
//	        that JSON. Build = build -> checksum -> write plugin.json -> copy
//	        resources -> write release.json -> verify.
//
// This package depends only on the standard library plus ecosystem/sdk and
// ecosystem/packaging (verified by an AST guard forbidding any internal/
// import), so the tool compiles without the Runtime.
package tooling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/ecosystem/sdk"
)

// BuildSpec is the author's input to the packaging tool. It is NOT the
// generated plugin.json — the Builder produces that. Keeping the two apart
// honors Phase 7.2 MUST-5 (distribution metadata is owned by the tooling, the
// author never hand-rolls the file the host consumes).
type BuildSpec struct {
	Name        string
	Version     string
	Description string

	// OutDir is the package directory written (GPT Round 32: "dist/", not a
	// compressed archive — zip/tar.gz can come later).
	OutDir string

	// Ops maps an operation id to its build + runtime descriptor.
	Ops map[string]OpSpec

	// Resources are extra files/dirs copied verbatim into OutDir.
	Resources []string
}

// OpSpec describes one operation's helper binary: how to build it and how it
// runs out-of-process. ExecPath is relative to the package directory.
type OpSpec struct {
	ExecPath string
	// SourceDir is the Go module root containing the helper's go.mod.
	SourceDir string
	// BuildPkg is the package passed to `go build` (e.g. "./cmd/helper").
	BuildPkg string

	Args               []string
	Env                []string
	Dir                string
	ExecTimeoutSeconds float64
	MaxResponseMB      int64
}

// BuildFunc builds one op's executable to outPath. Production uses
// DefaultGoBuild (shells out to `go build`); tests inject a stub so the suite
// stays hermetic and offline.
type BuildFunc func(ctx context.Context, op OpSpec, outPath string) error

// DefaultGoBuild returns a BuildFunc that compiles op.BuildPkg with the host
// Go toolchain. GOTOOLCHAIN=local / GOSUMDB=off keep a missing toolchain or sum
// db from triggering a network fetch mid-build (offline discipline).
func DefaultGoBuild() BuildFunc {
	return func(ctx context.Context, op OpSpec, outPath string) error {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("tooling: mkdir for %s: %w", outPath, err)
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, op.BuildPkg)
		cmd.Dir = op.SourceDir
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOSUMDB=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tooling: go build %s: %w\n%s", op.BuildPkg, err, out)
		}
		return nil
	}
}

// Build turns a BuildSpec into a verified package directory at spec.OutDir.
// The Builder composes plugin.json (the CLI never hand-assembles it).
func Build(ctx context.Context, spec BuildSpec, build BuildFunc) error {
	if spec.Name == "" || spec.Version == "" {
		return fmt.Errorf("tooling: BuildSpec requires Name and Version")
	}
	if len(spec.Ops) == 0 {
		return fmt.Errorf("tooling: BuildSpec requires at least one Op")
	}
	if err := os.MkdirAll(spec.OutDir, 0o755); err != nil {
		return fmt.Errorf("tooling: mkdir out dir: %w", err)
	}

	pkg := &packaging.Package{
		Name:        spec.Name,
		Version:     spec.Version,
		SDKVersion:  sdk.ProtocolVersion,
		Description: spec.Description,
		Operations:  make(map[string]packaging.RunSpec, len(spec.Ops)),
		Checksums:   make(map[string]string, len(spec.Ops)),
	}

	for opID, op := range spec.Ops {
		if op.ExecPath == "" {
			return fmt.Errorf("tooling: op %q missing ExecPath", opID)
		}
		outPath := filepath.Join(spec.OutDir, op.ExecPath)
		if err := build(ctx, op, outPath); err != nil {
			return fmt.Errorf("tooling: build op %q: %w", opID, err)
		}
		sum, err := sha256OfFile(outPath)
		if err != nil {
			return fmt.Errorf("tooling: checksum op %q: %w", opID, err)
		}
		pkg.Operations[opID] = packaging.RunSpec{
			Executable:         op.ExecPath,
			Args:               op.Args,
			Env:                op.Env,
			Dir:                op.Dir,
			ExecTimeoutSeconds: op.ExecTimeoutSeconds,
			MaxResponseMB:      op.MaxResponseMB,
		}
		pkg.Checksums[op.ExecPath] = sum
	}

	// Copy resources verbatim (templates, scripts, assets).
	for _, r := range spec.Resources {
		if err := copyTree(r, spec.OutDir); err != nil {
			return fmt.Errorf("tooling: copy resource %q: %w", r, err)
		}
	}

	// Write plugin.json (generated, never hand-assembled by the caller).
	pj, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("tooling: marshal plugin.json: %w", err)
	}
	pjPath := filepath.Join(spec.OutDir, "plugin.json")
	if err := os.WriteFile(pjPath, pj, 0o644); err != nil {
		return fmt.Errorf("tooling: write plugin.json: %w", err)
	}

	// Release manifest (Release Tool's concern, NOT Runtime).
	rel := releaseManifest{
		Version:    spec.Version,
		BuiltAt:    time.Now().UTC().Format(time.RFC3339),
		SDKVersion: sdk.ProtocolVersion,
		Checksum:   sha256Hex(pj),
	}
	relJSON, err := json.MarshalIndent(rel, "", "  ")
	if err != nil {
		return fmt.Errorf("tooling: marshal release.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(spec.OutDir, "release.json"), relJSON, 0o644); err != nil {
		return fmt.Errorf("tooling: write release.json: %w", err)
	}

	// Verify: the produced directory must load + validate as a Package — the
	// same check the host runs before isolation.AddFromPackage.
	produced, err := packaging.Load(spec.OutDir)
	if err != nil {
		return fmt.Errorf("tooling: load produced package: %w", err)
	}
	if err := produced.Validate(); err != nil {
		return fmt.Errorf("tooling: produced package invalid: %w", err)
	}
	return nil
}

type releaseManifest struct {
	Version    string `json:"version"`
	BuiltAt    string `json:"builtAt"`
	SDKVersion string `json:"sdkVersion"`
	Checksum   string `json:"checksum"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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

// copyTree copies a file or directory tree from src into dst (dst is the
// package root; src's basename is preserved as the entry under dst).
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, filepath.Base(src)), data, 0o644)
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
