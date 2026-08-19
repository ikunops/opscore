package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
)

// sessionCookieName is the httpOnly, SameSite=Strict session cookie (P22-9). It
// carries ONLY the signed access token; the SPA never reads it in JS, so an XSS
// compromise cannot exfiltrate the credential. The browser attaches it
// automatically on same-site requests (the dashboard + protection API share the
// :8082 host).
const sessionCookieName = "opscore_at"

// sessionCookieMaxAge mirrors auth.defaultAccessTTL (15 min). The cookie and the
// signed token expire together; on expiry the SPA re-logs in.
const sessionCookieMaxAge = 900

// setSessionCookie issues the httpOnly + SameSite=Strict session cookie (P22-9).
// Secure is enabled only when the connection is TLS-terminated so the dev
// http:// surface still works; production behind TLS MUST set it true.
func setSessionCookie(w http.ResponseWriter, r *http.Request, accessToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   sessionCookieMaxAge,
	})
}

// sameOriginOrFail enforces P22-9's CSRF defense (fail-closed): any state-
// changing request must carry an Origin (or Referer fallback) whose host matches
// the request host. A missing or cross-site Origin is rejected with 403 — we
// never assume "safe" when the header is absent. This is the second pillar of
// P22-9 alongside the httpOnly cookie; together they make a forged cross-site
// POST useless (no ambient credential + wrong Origin).
func sameOriginOrFail(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host // host:port as seen by the server
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer") // Referer is the fallback; still host-checked
	}
	if origin == "" {
		writeError(w, http.StatusForbidden, "csrf: missing Origin/Referer")
		return false
	}
	ou, err := url.Parse(origin)
	if err != nil || ou.Host == "" {
		writeError(w, http.StatusForbidden, "csrf: malformed Origin/Referer")
		return false
	}
	if !strings.EqualFold(ou.Host, host) {
		writeError(w, http.StatusForbidden, "csrf: cross-site request blocked")
		return false
	}
	return true
}

// handleManagementLogin issues a SAME-ORIGIN session for the dashboard SPA
// (Phase 22.2). It mirrors POST /api/auth/login but lives on the :8082
// management surface so the SPA never crosses origins (no CORS; the cookie stays
// httpOnly + SameSite=Strict per P22-9). Bearer API clients keep using :8080.
func (s *Server) handleManagementLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	access, refresh, _, err := s.auth.Login(c.Username, c.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	setSessionCookie(w, r, access)
	writeJSON(w, http.StatusOK, tokenPair{AccessToken: access, RefreshToken: refresh})
}

// handleProtectionKill is the operator-initiated kill WRITE entry (P22-2). It is
// the SINGLE mutation seam: it delegates to protection.OperatorMutation, which
// writes solely through the Gate's KillStore (no second kill state) and records
// protection.kill audit observations. The kill flag is NOT executed here — the
// Gate still evaluates normally on the next admission (P22-4). Every audit field
// is server-derived (P22-10); the request body carries no authoritative state.
func (s *Server) handleProtectionKill(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if !sameOriginOrFail(w, r) {
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}
	var body struct {
		Capability string `json:"capability"`
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Capability == "" {
		writeError(w, http.StatusBadRequest, "capability is required")
		return
	}
	if body.Reason == "" {
		body.Reason = "operator-initiated kill"
	}
	mut := protection.NewOperatorMutation(s.gate.KillStore(), s.gate.Audit(), time.Now)
	res, err := mut.Kill(r.Context(), body.Capability, body.Reason, username)
	if errors.Is(err, protection.ErrAuditOutcomeFailed) {
		// P22-8: KillStore mutation succeeded but the audit OUTCOME could not be
		// persisted. We do NOT roll back (the audit + kill domains share no
		// transaction); we return degraded so the caller knows the intent-without-
		// outcome is queryable via the audit trail.
		writeJSON(w, http.StatusOK, map[string]any{
			"capability_id": res.CapabilityID,
			"prev_killed":   res.PrevKilled,
			"new_killed":    res.NewKilled,
			"operator":      res.Operator,
			"at":            res.At,
			"degraded":      true,
			"detail":        "kill applied; audit outcome not persisted (intent retained)",
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "kill failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capability_id": res.CapabilityID,
		"prev_killed":   res.PrevKilled,
		"new_killed":    res.NewKilled,
		"operator":      res.Operator,
		"at":            res.At,
		"degraded":      false,
	})
}

// handleProtectionRelease removes an operator kill (P22-4: does NOT restore
// execution — the next admission is re-evaluated by the Gate; a breaker Open
// still blocks). The capability is taken from the path {id}; the request body
// carries no authoritative state (P22-10: all audit fields server-derived).
func (s *Server) handleProtectionRelease(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if !sameOriginOrFail(w, r) {
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "protection not enabled")
		return
	}
	capID := r.PathValue("id")
	if capID == "" {
		writeError(w, http.StatusBadRequest, "capability id required")
		return
	}
	mut := protection.NewOperatorMutation(s.gate.KillStore(), s.gate.Audit(), time.Now)
	res, err := mut.Release(r.Context(), capID, username)
	if errors.Is(err, protection.ErrAuditOutcomeFailed) {
		writeJSON(w, http.StatusOK, map[string]any{
			"capability_id": res.CapabilityID,
			"prev_killed":   res.PrevKilled,
			"new_killed":    res.NewKilled,
			"operator":      res.Operator,
			"at":            res.At,
			"degraded":      true,
			"detail":        "release applied; audit outcome not persisted (intent retained)",
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "release failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capability_id": res.CapabilityID,
		"prev_killed":   res.PrevKilled,
		"new_killed":    res.NewKilled,
		"operator":      res.Operator,
		"at":            res.At,
		"degraded":      false,
	})
}
