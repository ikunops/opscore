// Package compat defines the Version Compatibility Policy for the OpsCore
// third-party Executable Plugin ecosystem (Phase 7.5, GPT Round 34).
//
// It is a SPECIFICATION package. It models how a Package declares the Runtime
// it targets and exposes the validation rules that decide whether a Package is
// compatible with a given Runtime. It NEVER downloads, installs, upgrades, or
// migrates anything, and it never touches the frozen Runtime Core.
//
// Dependency direction (pinned by an AST guard forbidding internal/):
//
//	compat → ecosystem/packaging  (reads SDKVersion / Version of a loaded Package)
//	compat → ecosystem/registry    (reads the PackageRef compatibility window)
//
// Both are sibling ecosystem packages, not the Runtime Core. The Policy answers
// exactly one question: is this Package compatible with this Runtime?
package compat

import (
	"fmt"
	"strings"

	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/ecosystem/registry"
)

// DefaultSDK is the isolation protocol every Runtime in this lineage speaks.
const DefaultSDK = "opscore.isolation/v1"

// RuntimeSpec describes the host Runtime a Package would load into.
type RuntimeSpec struct {
	// SupportedSDK is the set of isolation protocols the Runtime speaks.
	// When empty, DefaultSDK is assumed.
	SupportedSDK []string
	// Version is the Runtime's own semantic version, e.g. "1.4.0".
	Version string
}

// PackageSpec is the compatibility declaration a Package makes about the
// Runtime it targets. It is derived from packaging.Package /
// registry.PackageRef by the From* adapters below; it never re-implements
// those models.
type PackageSpec struct {
	// SDKVersion is the isolation protocol the Package was built against.
	SDKVersion string
	// Version is the Package's own semantic version.
	Version string
	// MinRuntime / MaxRuntime are the inclusive Runtime version window the
	// Package supports. Empty means "no lower / upper bound".
	MinRuntime string
	MaxRuntime string
}

// FromPackage extracts the compatibility declaration from a loaded Package.
// The loaded Package does not yet carry a runtime window, so only the SDK
// protocol is asserted here; a host may enrich the spec from its own policy.
func FromPackage(pkg *packaging.Package) PackageSpec {
	return PackageSpec{
		SDKVersion: pkg.SDKVersion,
		Version:    pkg.Version,
	}
}

// FromRef extracts the compatibility declaration from a Registry PackageRef,
// including the inclusive runtime window the Ref advertises.
func FromRef(ref registry.PackageRef) PackageSpec {
	return PackageSpec{
		SDKVersion: ref.SDKVersion,
		Version:    ref.LatestVersion,
		MinRuntime: ref.MinRuntime,
		MaxRuntime: ref.MaxRuntime,
	}
}

// Code is a machine-readable compatibility decision, mirroring the DecisionCode
// style used by the Sandbox envelope and Signature verdicts. Callers (Registry,
// Marketplace, CLI, UI) should switch on Code rather than parse Reasons.
type Code string

const (
	// CodeOK means the Package is compatible with the Runtime.
	CodeOK Code = "ok"
	// CodeSDKMismatch means the Package's isolation protocol is not spoken by
	// the Runtime.
	CodeSDKMismatch Code = "sdk-mismatch"
	// CodeRuntimeTooOld means the Runtime version is below the Package's
	// declared MinRuntime.
	CodeRuntimeTooOld Code = "runtime-too-old"
	// CodeRuntimeTooNew means the Runtime version is above the Package's
	// declared MaxRuntime.
	CodeRuntimeTooNew Code = "runtime-too-new"
	// CodeInvalidVersion means a version string (Runtime, Min, or Max) could
	// not be parsed by the intentionally-minimal parser.
	CodeInvalidVersion Code = "invalid-version"
)

// Result is the outcome of a compatibility check.
type Result struct {
	// Compatible is true only when Reasons contains no blocking entry.
	Compatible bool
	// Code is the machine-readable primary decision; empty only when the check
	// produced no blocking reason and was not (yet) marked compatible.
	Code Code
	// Reasons lists every rule outcome — blocking reasons when incompatible,
	// a single confirmation when compatible.
	Reasons []string
}

func (r *Result) add(format string, a ...any) {
	r.Reasons = append(r.Reasons, fmt.Sprintf(format, a...))
}

// fail records a blocking reason and, on the first such reason, pins the
// machine-readable Code so callers get a stable primary decision.
func (r *Result) fail(code Code, format string, a ...any) {
	if r.Code == "" {
		r.Code = code
	}
	r.add(format, a...)
}

// Policy judges compatibility between a Package and a Runtime.
type Policy struct {
	supportedSDK []string
}

// NewPolicy returns a Policy that accepts the given SDK protocols. When none
// are supplied it accepts only DefaultSDK.
func NewPolicy(supportedSDK ...string) *Policy {
	if len(supportedSDK) == 0 {
		supportedSDK = []string{DefaultSDK}
	}
	return &Policy{supportedSDK: supportedSDK}
}

// Check decides whether spec is compatible with rt. Two rule families apply:
//
//  1. SDK protocol match — spec.SDKVersion must be in the Runtime's accepted
//     set. Forward/backward compatibility across SDK protocols is exercised by
//     widening supportedSDK, not by silently accepting a mismatch.
//  2. Runtime version window — rt.Version must lie within [MinRuntime,
//     MaxRuntime] when those bounds are declared.
func (p *Policy) Check(spec PackageSpec, rt RuntimeSpec) Result {
	var res Result

	if !contains(p.supportedSDK, spec.SDKVersion) {
		res.fail(CodeSDKMismatch, "SDK protocol %q not supported by runtime (accepts %v)", spec.SDKVersion, p.supportedSDK)
	}

	rtV, err := parseVersion(rt.Version)
	if err != nil {
		res.fail(CodeInvalidVersion, "runtime version %q is not a valid semantic version: %v", rt.Version, err)
	} else {
		if spec.MinRuntime != "" {
			minV, e := parseVersion(spec.MinRuntime)
			if e != nil {
				res.fail(CodeInvalidVersion, "package MinRuntime %q is not a valid semantic version: %v", spec.MinRuntime, e)
			} else if compareVersion(rtV, minV) < 0 {
				res.fail(CodeRuntimeTooOld, "runtime %s is below package minimum %s", rt.Version, spec.MinRuntime)
			}
		}
		if spec.MaxRuntime != "" {
			maxV, e := parseVersion(spec.MaxRuntime)
			if e != nil {
				res.fail(CodeInvalidVersion, "package MaxRuntime %q is not a valid semantic version: %v", spec.MaxRuntime, e)
			} else if compareVersion(rtV, maxV) > 0 {
				res.fail(CodeRuntimeTooNew, "runtime %s is above package maximum %s", rt.Version, spec.MaxRuntime)
			}
		}
	}

	res.Compatible = len(res.Reasons) == 0
	if res.Compatible {
		res.Code = CodeOK
		res.add("package %s (sdk %s) is compatible with runtime %s", spec.Version, spec.SDKVersion, rt.Version)
	}
	return res
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Version is a minimal semantic version (major.minor.patch).
type Version struct {
	Major, Minor, Patch int
}

// parseVersion parses "vMAJOR.MINOR.PATCH" or "MAJOR.MINOR.PATCH", tolerating a
// leading "v" and a semver range prefix (>=, <=, ^, =). It does not import any
// third-party module — stdlib only, per the Phase 7.5 freeze.
func parseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"v", ">=", "<=", "^", "="} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid semantic version %q", s)
	}
	var v Version
	if _, err := fmt.Sscanf(parts[0], "%d", &v.Major); err != nil {
		return Version{}, fmt.Errorf("invalid major in %q: %w", s, err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &v.Minor); err != nil {
		return Version{}, fmt.Errorf("invalid minor in %q: %w", s, err)
	}
	if len(parts) == 3 {
		if _, err := fmt.Sscanf(parts[2], "%d", &v.Patch); err != nil {
			return Version{}, fmt.Errorf("invalid patch in %q: %w", s, err)
		}
	}
	return v, nil
}

// compareVersion returns -1 / 0 / +1 comparing a to b lexicographically on
// major, minor, patch.
func compareVersion(a, b Version) int {
	for _, pair := range [3]struct {
		x, y int
	}{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair.x != pair.y {
			if pair.x < pair.y {
				return -1
			}
			return 1
		}
	}
	return 0
}
