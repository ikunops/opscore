package harness

import (
	"net/http"
)

// newProbeRouter builds the operational health/readiness/version HTTP handler.
// It is READ-ONLY (A-3): handlers observe the already-built read models and
// never trigger execution, repair, or scheduling. It is served on a SEPARATE
// bind from external/v1 (A-10).
func (h *Harness) newProbeRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/versionz", h.handleVersionz)
	return mux
}

// handleHealthz reports liveness: the process is up and the composition root was
// built successfully (otherwise we would not be serving). It performs no read
// and no side effect.
func (h *Harness) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"}, nil)
}

// handleReadyz reports readiness: the policy persistence read source is
// reachable. It performs a PURE read (Repository.List) — if that fails it
// reports not-ready. It NEVER attempts to fix, re-run, or trigger anything (A-3).
func (h *Harness) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if h.cap == nil || h.cap.polRepo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"status": "not ready", "reason": "policy store unconfigured"})
		return
	}
	if _, err := h.cap.polRepo.List(); err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"status": "not ready", "reason": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "ready"}, nil)
}

// handleVersionz exposes build/version metadata for observability only (A-7).
// It is never an event-ownership or control channel.
func (h *Harness) handleVersionz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, buildVersionInfo(), nil)
}
