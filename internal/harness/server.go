package harness

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/YuDong999/opscore/internal/external"
	"github.com/YuDong999/opscore/internal/management"
)

// newRouter mounts the external.Server read contract over HTTP. The Harness adds NO new resource
// or method beyond what external.Server already exposes (ADR-026 §4, MUST-4) — byte-identical to
// the Phase 11 external/v1 contract.
func newRouter(srv *external.Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/external/v1/execution/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/external/v1/execution/")
		v, err := srv.GetExecution(r.Context(), id)
		writeJSON(w, v, err)
	})
	mux.HandleFunc("/external/v1/host/", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/external/v1/host/")
		v, err := srv.GetHost(r.Context(), ref)
		writeJSON(w, v, err)
	})
	mux.HandleFunc("/external/v1/policy/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/external/v1/policy/")
		v, err := srv.GetPolicy(r.Context(), id)
		writeJSON(w, v, err)
	})
	mux.HandleFunc("/external/v1/correlation", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := srv.GetCorrelation(r.Context(), external.ScopeDTO{
			Kind: q.Get("kind"),
			Ref:  q.Get("ref"),
		})
		writeJSON(w, v, err)
	})
	assertNoManagementRoutes(mux)
	return mux
}

// assertNoManagementRoutes enforces ADR-036 §3.6 surface isolation at the moment
// the read router is built.
//
// The separation is easy to state and easy to lose: someone adds a policy write
// endpoint "next to the other policy endpoints" on :8080, and the read contract
// silently becomes a write contract. Comments do not prevent this; a probe does.
//
// Mechanically, we ask the assembled read mux to route each management pattern's
// concrete path. Go 1.22's Handler() returns the matched pattern, so an empty
// pattern means genuinely unroutable — a much stronger statement than comparing
// registration strings, because it also catches a prefix route that would
// SWALLOW a management path without ever naming it.
//
// It panics rather than returning an error on purpose. No configuration, input,
// or environment can produce this failure — only a source change can — so it is
// a broken build, not a runtime condition a caller could sensibly handle.
func assertNoManagementRoutes(mux *http.ServeMux) {
	for _, pattern := range management.RoutePatterns() {
		path := pattern
		if i := strings.IndexByte(path, ' '); i >= 0 {
			path = path[i+1:] // strip the Go 1.22 "METHOD " prefix
		}
		// Wildcards never appear in a real request path; a concrete segment does.
		path = strings.ReplaceAll(path, "{id}", "probe")

		req, err := http.NewRequest(http.MethodPost, path, nil)
		if err != nil {
			panic("harness: cannot build isolation probe for " + pattern + ": " + err.Error())
		}
		if _, matched := mux.Handler(req); matched != "" {
			panic("harness: external/v1 mux routes management path " + path +
				" via pattern " + matched + " — ADR-036 §3.6 requires the write surface " +
				"to exist only on its own bind")
		}
	}
}

// writeJSON encodes a value object (or an error / not-found) as JSON. A nil value (the facade
// returns nil when no reader produced data) is a 404, never a 200 with "null".
func writeJSON(w http.ResponseWriter, v interface{}, err error) {
	if err != nil {
		if errors.Is(err, external.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONStatus writes v as JSON with an explicit HTTP status code. It is used
// by the operational probe handlers; external/v1 always uses writeJSON.
func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
