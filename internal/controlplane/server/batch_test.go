package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/YuDong999/opscore/internal/controlplane/hostregistry"
	"github.com/YuDong999/opscore/internal/core"
)

// seedGroup puts web-01 and a new web-02 into the "web" group so group fan-out
// can be exercised.
func seedGroup(t *testing.T, store *hostregistry.MemoryHostStore) {
	t.Helper()
	_, _ = store.Save(hostregistry.Host{
		Name:   "web-01",
		Target: core.TargetHost{Address: "10.0.0.1", Port: 22, User: "root", InsecureIgnoreHostKey: true},
		Groups: []string{"web"},
	})
	_, _ = store.Save(hostregistry.Host{
		Name:   "web-02",
		Target: core.TargetHost{Address: "10.0.0.2", Port: 22, User: "root", InsecureIgnoreHostKey: true},
		Groups: []string{"web"},
	})
}

func TestBatch_FanOutByGroup(t *testing.T) {
	srv, store := newTestServerWithHosts(t)
	seedGroup(t, store)

	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/batch", token, map[string]any{
		"op":    "demo.ok",
		"group": "web",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Op      string `json:"op"`
		Results []struct {
			Address string `json:"address"`
			Success bool   `json:"success"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Op != "demo.ok" {
		t.Fatalf("op = %q", resp.Op)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results for group web, got %d: %s", len(resp.Results), rec.Body.String())
	}
	for _, r := range resp.Results {
		if !r.Success {
			t.Fatalf("target %s failed: %s", r.Address, rec.Body.String())
		}
	}
}

func TestBatch_FanOutByExplicitTargets(t *testing.T) {
	srv, store := newTestServerWithHosts(t)
	seedGroup(t, store)

	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/batch", token, map[string]any{
		"op":      "demo.ok",
		"targets": []string{"web-01", "web-02"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			Address string `json:"address"`
			Success bool   `json:"success"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %s", len(resp.Results), rec.Body.String())
	}
}

func TestBatch_RequiresTargets(t *testing.T) {
	srv, _ := newTestServerWithHosts(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/batch", token, map[string]any{"op": "demo.ok"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for no targets, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBatch_AuthorizesSubOpBeforeFanOut confirms the sub-operation is
// authorized (admin holds every synced op, so demo.ok passes and the batch
// proceeds to fan out) — the authorize call runs before any target is touched.
func TestBatch_AuthorizesSubOpBeforeFanOut(t *testing.T) {
	srv, store := newTestServerWithHosts(t)
	seedGroup(t, store)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/batch", token, map[string]any{
		"op":      "demo.ok",
		"targets": []string{"web-01", "web-02"},
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin should be authorized for demo.ok: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after authz, got %d: %s", rec.Code, rec.Body.String())
	}
}
