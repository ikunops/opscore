package main

// T401~T403 (ADR-091, maintenance round): the control-plane config seeding
// contract. loadConfigFile's documented contract says env vars take
// precedence over the file ("Env vars (…_<KEY>) take precedence over the
// file, and explicit CLI flags take precedence over both") — the code did the
// opposite, which silently defeats secret rotation via env whenever a stale
// key sits in a config file. These tests pin the corrected chain:
//
//	canonical OPSCORE_<KEY> -> legacy OPESCORE_<KEY> -> file value -> default
//
// with CLI flags still winning over all of it (they replace the default
// wholesale, which is outside these helpers' reach by construction).

import "testing"

// T401: env beats the file — for strings, ints and bools alike.
func TestT401EnvBeatsFile(t *testing.T) {
	file := map[string]string{
		"storage":         "memory",
		"target-port":     "22",
		"target-insecure": "false",
	}
	t.Setenv("OPESCORE_STORAGE", "sqlite")
	t.Setenv("OPESCORE_TARGET_PORT", "2222")
	t.Setenv("OPESCORE_TARGET_INSECURE", "true")

	if got := cfgVal(file, "storage", "def"); got != "sqlite" {
		t.Fatalf("T401: env must beat the file, got %q", got)
	}
	if got := cfgInt(file, "target-port", 0); got != 2222 {
		t.Fatalf("T401: env must beat the file for ints, got %d", got)
	}
	if got := cfgBool(file, "target-insecure", false); got != true {
		t.Fatalf("T401: env must beat the file for bools, got %v", got)
	}
}

// T402: the canonical OPSCORE_<KEY> prefix works; the legacy OPESCORE_<KEY>
// spelling keeps working; and the canonical one wins when both are set.
func TestT402CanonicalPrefixWinsLegacyKept(t *testing.T) {
	// Canonical only.
	t.Setenv("OPSCORE_STORAGE", "sqlite")
	if got := cfgVal(nil, "storage", "def"); got != "sqlite" {
		t.Fatalf("T402: canonical prefix must be honored, got %q", got)
	}

	// Both set: canonical wins.
	t.Setenv("OPESCORE_STORAGE", "memory")
	if got := cfgVal(nil, "storage", "def"); got != "sqlite" {
		t.Fatalf("T402: canonical prefix must win over the legacy alias, got %q", got)
	}

	// Legacy only keeps working (existing deployments must not break).
	t.Setenv("OPSCORE_STORAGE", "")
	if got := cfgVal(nil, "storage", "def"); got != "memory" {
		t.Fatalf("T402: the legacy prefix must keep working, got %q", got)
	}
}

// T403: the fall-through chain stays predictable — an unparsable env value
// falls to the file, then to the default; an empty env value counts as absent.
func TestT403FallThroughChain(t *testing.T) {
	file := map[string]string{"target-port": "2222", "target-insecure": "true"}

	t.Setenv("OPESCORE_TARGET_PORT", "not-a-number")
	if got := cfgInt(file, "target-port", 0); got != 2222 {
		t.Fatalf("T403: an unparsable env value must fall to the file, got %d", got)
	}
	if got := cfgInt(nil, "target-port", 99); got != 99 {
		t.Fatalf("T403: an unparsable env value with no file must fall to the default, got %d", got)
	}

	t.Setenv("OPESCORE_TARGET_INSECURE", "maybe")
	if got := cfgBool(file, "target-insecure", false); got != true {
		t.Fatalf("T403: an unparsable bool env must fall to the file, got %v", got)
	}

	// Empty env = absent (both spellings).
	t.Setenv("OPSCORE_STORAGE", "")
	t.Setenv("OPESCORE_STORAGE", "")
	if got := cfgVal(map[string]string{"storage": "file-value"}, "storage", "def"); got != "file-value" {
		t.Fatalf("T403: empty env values count as absent, got %q", got)
	}
}
