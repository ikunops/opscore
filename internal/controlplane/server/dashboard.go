package server

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML string

// serveDashboardShell writes the embedded Operational Protection Console SPA.
// The shell is static and carries no protected state; all kill/metrics data is
// fetched only after the httpOnly session cookie is set by POST /management/v1/login.
func serveDashboardShell(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(dashboardHTML))
}

// handleLoginShell serves the SAME console SPA at a PUBLIC path so
// unauthenticated operators have a login entry (the SPA renders its login
// form). It exposes no data — the protected read surface stays admin-only.
func (s *Server) handleLoginShell(w http.ResponseWriter, r *http.Request) {
	serveDashboardShell(w)
}

// handleDashboard serves the console SPA, but ONLY to an authenticated admin
// (ADR-050 admin-only boundary — Phase 22.2 R106=B correction). The shell is
// static, yet the security boundary is enforced here so unauthenticated callers
// never receive it. The httpOnly session cookie then authorizes the subsequent
// API requests. When protection is disabled (gate==nil) the console has nothing
// to show, so we fail closed with 404 (matching the read surface).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
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
	serveDashboardShell(w)
}
