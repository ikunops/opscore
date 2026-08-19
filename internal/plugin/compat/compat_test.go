package compat

import (
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

func TestDefaultGate_Compatible(t *testing.T) {
	g := DefaultGate{}
	man := &manifest.Manifest{Name: "mysql", Version: "1.0.0", PluginAPI: "opscore.plugin/v1", MinKernel: "0.1.0"}
	res, err := g.Check(man, KernelInfo{Version: "0.2.0", SupportedAPIs: []string{"opscore.plugin/v1"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Compatible {
		t.Fatalf("expected compatible, got reason %q", res.Reason)
	}
}

func TestDefaultGate_KernelTooOld(t *testing.T) {
	g := DefaultGate{}
	man := &manifest.Manifest{Name: "mysql", Version: "1.0.0", MinKernel: "0.2.0"}
	res, err := g.Check(man, KernelInfo{Version: "0.1.0"})
	if err == nil {
		t.Fatal("expected error for too-old kernel")
	}
	if res == nil || res.Compatible {
		t.Fatal("expected incompatible result")
	}
	if !strings.Contains(res.Reason, "requires kernel >= 0.2.0") {
		t.Fatalf("reason should mention required kernel, got %q", res.Reason)
	}
}

func TestDefaultGate_UnsupportedAPI(t *testing.T) {
	g := DefaultGate{}
	man := &manifest.Manifest{Name: "mysql", Version: "1.0.0", PluginAPI: "opscore.plugin/v2"}
	res, err := g.Check(man, KernelInfo{Version: "0.2.0", SupportedAPIs: []string{"opscore.plugin/v1"}})
	if err == nil {
		t.Fatal("expected error for unsupported API")
	}
	if res == nil || res.Compatible {
		t.Fatal("expected incompatible result")
	}
	if !strings.Contains(res.Reason, "PluginAPI") {
		t.Fatalf("reason should mention PluginAPI, got %q", res.Reason)
	}
}

func TestDefaultGate_NoConstraints(t *testing.T) {
	g := DefaultGate{}
	man := &manifest.Manifest{Name: "mysql", Version: "1.0.0"}
	res, err := g.Check(man, KernelInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Compatible {
		t.Fatalf("expected compatible, got %q", res.Reason)
	}
}

func TestDefaultGate_NilManifest(t *testing.T) {
	g := DefaultGate{}
	if _, err := g.Check(nil, KernelInfo{Version: "0.2.0"}); err == nil {
		t.Fatal("expected error for nil manifest")
	}
}

// TestDefaultGate_UnconfiguredKernelRejectsMinKernel documents the strict
// contract: if the kernel version is unconfigured (empty, treated as 0.0.0),
// any plugin that declares a MinKernel is rejected. In production the kernel
// version is always injected from build info; this guards against silently
// loading on an unknown kernel.
func TestDefaultGate_UnconfiguredKernelRejectsMinKernel(t *testing.T) {
	g := DefaultGate{}
	man := &manifest.Manifest{Name: "mysql", Version: "1.0.0", MinKernel: "0.1.0"}
	res, err := g.Check(man, KernelInfo{})
	if err == nil {
		t.Fatal("expected rejection when kernel version is unconfigured")
	}
	if res == nil || res.Compatible {
		t.Fatal("expected incompatible result")
	}
}

// TestDefaultGate_ResultCode verifies the GPT Round 13 SHOULD: each rejection
// path populates a STABLE, machine-readable Result.Code (so UIs/audit can
// group rejections without parsing Reason text), while a compatible result
// carries an empty Code.
func TestDefaultGate_ResultCode(t *testing.T) {
	g := DefaultGate{}

	cases := []struct {
		name string
		man  *manifest.Manifest
		kern KernelInfo
		code string
	}{
		{"kernel_too_old", &manifest.Manifest{Name: "a", Version: "1.0.0", MinKernel: "0.2.0"}, KernelInfo{Version: "0.1.0"}, "min_kernel"},
		{"unsupported_api", &manifest.Manifest{Name: "a", Version: "1.0.0", PluginAPI: "opscore.plugin/v2"}, KernelInfo{Version: "0.2.0", SupportedAPIs: []string{"opscore.plugin/v1"}}, "plugin_api"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, _ := g.Check(c.man, c.kern)
			if res == nil {
				t.Fatal("nil result")
			}
			if res.Compatible {
				t.Fatal("expected incompatible")
			}
			if res.Code != c.code {
				t.Fatalf("Code = %q, want %q (reason=%q)", res.Code, c.code, res.Reason)
			}
		})
	}

	// Compatible result carries no Code.
	res, err := g.Check(&manifest.Manifest{Name: "a", Version: "1.0.0"}, KernelInfo{Version: "0.2.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Code != "" {
		t.Fatalf("compatible result should have empty Code, got %q", res.Code)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.0", 1},
		{"0.2.0", "0.2.0", 0},
		{"0.2", "0.2.0", 0},
		{"v0.2.0", "0.2.0", 0},
		{"1.0.0", "0.9.9", 1},
		{"", "0.1.0", -1},
	}
	for _, c := range cases {
		got, err := compareSemver(c.a, c.b)
		if err != nil {
			t.Fatalf("compareSemver(%q,%q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compareSemver(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
