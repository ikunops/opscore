package server

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML string

// handleDashboard serves the embedded Operational Protection Console SPA
// (Phase 22.2). The shell is static and unauthenticated; the SPA authenticates
// via POST /management/v1/login (which sets the httpOnly cookie) and then polls
// the protection read API. No long-lived authoritative state ever lives in the
// UI — the server remains the single source of truth (P22-5 / P22-7).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(dashboardHTML))
}
