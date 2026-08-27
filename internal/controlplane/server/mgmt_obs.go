package server

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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

// handleProtectionAlertsHistory (Phase 29) exposes the bounded alert-transition
// ring as a READ-ONLY projection (R24-5). It reuses the EXACT auth + 404 envelope
// of /alerts: it never re-evaluates the alert, never triggers anything, and never
// becomes a Source of Truth. Truncated follows P29-M1 (true ONLY when overflow
// dropped records: dropped>0), never merely because the ring reached capacity.
func (s *Server) handleProtectionAlertsHistory(w http.ResponseWriter, r *http.Request) {
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

	limit := 10 // default "recent N" for the dashboard panel
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			limit = n
		}
	}

	txns := s.alertTracker.Transitions() // copy (P29-I6), NEWEST-FIRST (P29-S2)
	if limit < len(txns) {
		txns = txns[:limit] // take the most recent N (newest-first)
	}
	out := make([]map[string]any, 0, len(txns))
	for _, t := range txns {
		out = append(out, map[string]any{
			"at":            t.At.Format(time.RFC3339), // observation time (P29-S1)
			"from":          t.From,
			"to":            t.To,
			"kind":          transitionKind(t.From, t.To), // "FIRING" | "CLEAR"
			"unknown_rate":  t.UnknownRate,
			"threshold":     t.Threshold,
		})
	}
	hs := s.alertTracker.HistoryStats()
	writeJSON(w, http.StatusOK, map[string]any{
		"transitions": out,
		"history_stats": map[string]any{
			"capacity":                    hs.Capacity,
			"retained":                    hs.Retained,
			"dropped":                     hs.Dropped,
			"truncated":                   hs.Truncated, // P29-M1 / Phase 30
			"file_dropped":                hs.FileDropped,
			"retention_meta_inconsistent": hs.RetentionMetaInconsistent, // P30-I10
			"available":                   hs.Available,                 // P30-I11
			"load_error":                  hs.LoadError,                 // P30-I11
		},
	})
}

// transitionKind maps an edge (from,to) to its semantic label. Only the two
// genuine edges are named; anything else falls back to "unknown" (defensive —
// the ring should never hold a non-edge, but the API stays self-describing).
func transitionKind(from, to bool) string {
	if !from && to {
		return "FIRING"
	}
	if from && !to {
		return "CLEAR"
	}
	return "unknown"
}

// handleProtectionDecisionsExport (Phase 28) exposes a STATIC RETENTION SNAPSHOT
// of the provenance store for audit export. It is the SAME read projection as
// /decisions, extended to return the FULL buffered buffer (Recent(capacity),
// not the default limit=100) and to serialize it as JSON or CSV.
//
// Honesty boundary (R24-7 / R127-fix-1): the export is a CURRENT RETENTION
// SNAPSHOT of the bounded in-memory ring — NOT a complete forensic history.
// The envelope therefore carries export_completeness = "current-retention-snapshot"
// plus the raw capacity/buffered/dropped/truncated from store.Stats() unchanged.
//
// Completeness is never recomputed (R127-fix-2 / I3): an export omission is NOT
// a new provenance loss. CSV trailing '#' lines are export-format metadata
// (R127-fix-3 / I4), not data rows; consumers may ignore them.
//
// Surface/freeze (R127 / I5): registered ONLY on :8082 — never :8080, never
// external/v1, no frozen packages, no go.mod change. Projection only (I6):
// ProvenanceStore -> serialization -> JSON/CSV; no Gate re-evaluation, no Audit.
func (s *Server) handleProtectionDecisionsExport(w http.ResponseWriter, r *http.Request) {
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

	// Full retention snapshot: Recent(capacity) returns ALL buffered records.
	stats := store.Stats()
	recs := store.Recent(stats.Capacity)

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		s.writeDecisionsExportJSON(w, recs, stats)
	case "csv":
		s.writeDecisionsExportCSV(w, recs, stats)
	default:
		writeError(w, http.StatusBadRequest, "unsupported format (use json|csv)")
	}
}

func (s *Server) writeDecisionsExportJSON(w http.ResponseWriter, recs []protection.DecisionProvenance, stats protection.ProvenanceStats) {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"schema":              "provenance/export/v1",
		"exported_at":         time.Now().UTC().Format(time.RFC3339),
		"export_completeness": "current-retention-snapshot",
		"decisions":           out,
		"provenance_stats": map[string]any{
			"capacity":  stats.Capacity,
			"buffered":  stats.Buffered,
			"dropped":   stats.Dropped,
			"truncated": stats.Truncated,
		},
	})
}

func (s *Server) writeDecisionsExportCSV(w http.ResponseWriter, recs []protection.DecisionProvenance, stats protection.ProvenanceStats) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=decisions-export-%s.csv", time.Now().UTC().Format("20060102T150405Z")))
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	cw.Write([]string{"trace_id", "capability_id", "principal_hash", "guard", "decision", "action", "threshold", "observed", "detail", "latency_micros", "at"})
	for _, p := range recs {
		cw.Write([]string{
			p.TraceID,
			p.CapabilityID,
			p.PrincipalHash,
			p.Guard,
			p.Decision,
			p.Action,
			p.Threshold,
			p.Observed,
			p.Detail,
			strconv.FormatInt(p.LatencyMicros, 10),
			p.At.Format(time.RFC3339),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return // headers already sent; nothing safe to do
	}
	// Machine-readable metadata (R127-fix-3 / I4): leading '#' lines are
	// export-format metadata, NOT data rows; consumers may ignore them.
	io.WriteString(w, "# schema=provenance/export/v1\n")
	io.WriteString(w, fmt.Sprintf("# exported_at=%s\n", time.Now().UTC().Format(time.RFC3339)))
	io.WriteString(w, "# export_completeness=current-retention-snapshot\n")
	io.WriteString(w, fmt.Sprintf("# capacity=%d\n", stats.Capacity))
	io.WriteString(w, fmt.Sprintf("# buffered=%d\n", stats.Buffered))
	io.WriteString(w, fmt.Sprintf("# dropped=%d\n", stats.Dropped))
	io.WriteString(w, fmt.Sprintf("# truncated=%v\n", stats.Truncated))
}
