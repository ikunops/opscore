package server

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

// TestRenderUI_InjectsValidConfig is the authoritative guard against the
// Phase 1.9.1 regression where the config was injected but left a trailing
// `{}`, producing invalid JS that killed the entire <script> (every button
// dead). It runs in `go test` with no port/network dependency, so it is
// deterministic and cannot flake the way an HTTP smoke test can.
func TestRenderUI_InjectsValidConfig(t *testing.T) {
	cases := []struct {
		name   string
		demo   bool
		target core.TargetHost
	}{
		{"demo, no target", true, core.TargetHost{}},
		{"real target", false, core.TargetHost{Address: "192.168.94.20", Port: 22, User: "root"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := renderUI(tc.demo, tc.target)

			// 1) The real UI (Services tab) must be present, not a dev console.
			if !strings.Contains(html, "系统服务") {
				t.Fatal("rendered UI is missing the Services tab")
			}

			// 2) The placeholder token must be fully replaced, fallback `{}` and all.
			if strings.Contains(html, "__OPSCORE_UI_CONFIG__") {
				t.Fatal("placeholder token not replaced — injection broken")
			}

			// 3) Exactly one `window.__OPSCORE__ = {...};` with valid JSON.
			re := regexp.MustCompile(`window\.__OPSCORE__\s*=\s*(\{.*?\});`)
			m := re.FindStringSubmatch(html)
			if m == nil {
				t.Fatal("window.__OPSCORE__ assignment missing or malformed")
			}
			var cfg uiConfig
			if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
				t.Fatalf("injected config is not valid JSON (trailing {}?): %v", err)
			}
			if cfg.Demo != tc.demo {
				t.Fatalf("demo flag mismatch: want %v got %v", tc.demo, cfg.Demo)
			}
		})
	}
}

// TestRenderUI_NoTrailingBraces explicitly fails if a stray `{}` is left after
// the injected object, which would render as `window.__OPSCORE__ = {...}{};`
// and break the whole <script>.
func TestRenderUI_NoTrailingBraces(t *testing.T) {
	html := renderUI(true, core.TargetHost{})
	if regexp.MustCompile(`__OPSCORE__\s*=\s*\{.*?\}\{\};`).MatchString(html) {
		t.Fatal("detected trailing `{}` after injected config — would break <script>")
	}
}
