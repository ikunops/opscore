package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// handleProtectionDecisions exposes the Phase 24.2 decision-log projection
// (R24-1 Decision-Time Provenance, R24-5 Projection Only). It is a READ
// projection of the Gate's provenance sink — it never becomes a Source of
// Truth and never reconstructs decisions after the fact.
//
// Secret boundary (R24-7): provenance carries ONLY the principal hash + advisory
// refs (threshold/observed/detail); it holds no token, cookie, or key, so the
// log is safe to ship to an observability backend. The provenance_stats block
// exposes completeness + truncation so a consumer can detect dropped records
// honestly (R24-7 provenance loss honesty) rather than trusting a partial log.
//
// Supported query params: limit (default 100), trace_id, capability. When
// trace_id or capability is given, results are filtered to that ref.
func (s *Server) handleProtectionDecisions(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}
	store := s.gate.ProvenanceStore()
	if store == nil {
		writeError(w, http.StatusNotFound, "provenance not enabled")
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			limit = n
		}
	}

	var recs []protection.DecisionProvenance
	switch {
	case r.URL.Query().Get("trace_id") != "":
		recs = store.ByTraceID(r.URL.Query().Get("trace_id"))
	case r.URL.Query().Get("capability") != "":
		recs = store.ByCapability(r.URL.Query().Get("capability"))
	default:
		recs = store.Recent(limit)
	}

	out := make([]map[string]any, 0, len(recs))
	for _, p := range recs {
		out = append(out, map[string]any{
			"trace_id":       p.TraceID,
			"capability_id":  p.CapabilityID,
			"principal_hash": p.PrincipalHash,
			"guard":          p.Guard,
			"decision":       p.Decision,
			"action":         p.Action,
			"threshold":      p.Threshold,
			"observed":       p.Observed,
			"detail":         p.Detail,
			"latency_micros": p.LatencyMicros,
			"at":             p.At,
		})
	}

	stats := store.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"decisions": out,
		"provenance_stats": map[string]any{
			"capacity":  stats.Capacity,
			"buffered":  stats.Buffered,
			"dropped":   stats.Dropped,
			"truncated": stats.Truncated,
		},
	})
}

// handleProtectionAlerts exposes the Phase 24.2 declarative alert state
// (R24-3 Alerting Declarative: the server computes + exposes the alert state
// but never transports it or triggers any execution). The tracker's Since is
// the only state held (the firing-entry time); all computation is pure
// (protection.ComputeAlertCondition) over the RateHistory projection.
func (s *Server) handleProtectionAlerts(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil || s.alertTracker == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}

	st := s.alertTracker.State()
	since := ""
	if !st.Since.IsZero() {
		since = st.Since.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"firing":         st.Firing,
		"since":          since,
		"unknown_rate":   st.UnknownRate,
		"threshold":      st.Threshold,
		"window_seconds": int64(s.alertPolicy.Window.Seconds()),
	})
}
