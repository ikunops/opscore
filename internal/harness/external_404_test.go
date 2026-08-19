package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/YuDong999/opscore/internal/external"
	"github.com/YuDong999/opscore/internal/platformview"
)

// notFoundGovernance is a minimal platformview.GovernanceReader whose QueryRules reports the policy
// does not exist, mirroring what governancepolicy.Reader returns for an unknown id (R79-A).
type notFoundGovernance struct{}

func (notFoundGovernance) QueryVerdict(context.Context, string) (*platformview.VerdictView, error) {
	return nil, nil
}

func (notFoundGovernance) QueryRules(context.Context, string) ([]platformview.RuleView, error) {
	return nil, platformview.ErrPolicyNotFound
}

// TestExternalPolicyUnknownReturns404 proves the full read chain down to the HTTP status: an unknown
// policy id must yield HTTP 404, never 200 with an empty shell (R79-A). It exercises the real
// platformview.Facade and external.Server mounted by newRouter.
func TestExternalPolicyUnknownReturns404(t *testing.T) {
	pv := platformview.New(platformview.Readers{Governance: notFoundGovernance{}})
	srv := external.NewServer(pv, nil, nil)
	mux := newRouter(srv)

	req := httptest.NewRequest(http.MethodGet, "/external/v1/policy/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /external/v1/policy/ghost: status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}
