package management

import (
	"strings"
	"testing"
)

// TestRoutePatternsAreManagementScoped11 (P-9) proves Phase 20 grows the route
// set to exactly 11, all inside the management namespace, and the traces read
// route is present (ADR-045 §5, R20-1).
func TestRoutePatternsAreManagementScoped11(t *testing.T) {
	pats := RoutePatterns()
	if len(pats) != 11 {
		t.Fatalf("exported %d route patterns, want 11 (ADR-045 §5)", len(pats))
	}
	want := "GET " + RoutePrefix + "traces"
	found := false
	for _, p := range pats {
		if !strings.Contains(p, RoutePrefix) {
			t.Errorf("route %q is outside the %s namespace the harness isolates", p, RoutePrefix)
		}
		if p == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Phase 20 route %q missing from RoutePatterns()", want)
	}
}
