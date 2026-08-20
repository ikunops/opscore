package server

import (
	"errors"
	"net/http"

	"github.com/YuDong999/opscore/internal/protection"
)

// handleProtectionQuotas returns the configured quota DEFINITIONS (R23-3:
// definitions only — consumption is owned by the evidence source and is NEVER
// projected here, so this surface cannot leak or fabricate live usage).
// Admin-only (it is a sensitive operational policy surface). Served on :8082
// via ProtectionReadMux (R21-1, colocated with the gate that owns the store).
func (s *Server) handleProtectionQuotas(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil || s.gate.QuotaStore() == nil {
		writeError(w, http.StatusNotFound, "quota protection not enabled")
		return
	}
	defs := s.gate.QuotaStore().ListDefinitions()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"capability": d.Capability,
			"principal":  d.Principal,
			"rss_bytes":  d.RSSBytes,
			"cpu_secs":   d.CPUSecs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"quotas": out})
}

// quotaSetBody is the operator-supplied set request. The server derives the
// audit facts (operator, at) and writes through the single-owner QuotaStore —
// it never accepts consumption from the client (R23-3).
type quotaSetBody struct {
	Capability string  `json:"capability"`
	Principal  string  `json:"principal"`
	RSSBytes   int64   `json:"rss_bytes"`
	CPUSecs    float64 `json:"cpu_secs"`
}

// handleProtectionQuotaSet upserts a quota definition via the SINGLE mutation
// seam (P22-2 analog). Admin-only and CSRF fail-closed (P22-9): same-origin
// only. Served on :8082; :8080 never serves this.
func (s *Server) handleProtectionQuotaSet(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil || s.gate.QuotaStore() == nil {
		writeError(w, http.StatusNotFound, "quota protection not enabled")
		return
	}
	if !sameOriginOrFail(w, r) {
		return // sameOriginOrFail already wrote 403
	}
	var body quotaSetBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Capability == "" {
		writeError(w, http.StatusBadRequest, "capability required")
		return
	}
	mut := protection.NewOperatorQuotaMutation(s.gate.QuotaStore(), s.gate.Audit(), nil)
	res, err := mut.Set(r.Context(), protection.QuotaDefinition{
		Capability: body.Capability,
		Principal:  body.Principal, // "" = capability-wide default
		RSSBytes:   body.RSSBytes,
		CPUSecs:    body.CPUSecs,
	}, username)
	if errors.Is(err, protection.ErrAuditOutcomeFailed) {
		// R21-11 analog: the definition stands; the outcome observation failed.
		// Surfaces degraded:true so the caller knows audit is incomplete.
		writeJSON(w, http.StatusOK, map[string]any{
			"capability": res.Capability,
			"principal":  res.Principal,
			"action":     res.Action,
			"operator":   res.Operator,
			"at":         res.At.Format("2006-01-02T15:04:05Z07:00"),
			"degraded":   true,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota set failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capability": res.Capability,
		"principal":  res.Principal,
		"action":     res.Action,
		"operator":   res.Operator,
		"at":         res.At.Format("2006-01-02T15:04:05Z07:00"),
		"degraded":   false,
	})
}

// handleProtectionQuotaClear removes a quota definition via the SAME single
// mutation seam. Admin-only + CSRF fail-closed, same as set.
func (s *Server) handleProtectionQuotaClear(w http.ResponseWriter, r *http.Request) {
	username, err := s.subject(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.isAdmin(username) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.gate == nil || s.gate.QuotaStore() == nil {
		writeError(w, http.StatusNotFound, "quota protection not enabled")
		return
	}
	if !sameOriginOrFail(w, r) {
		return
	}
	var body struct {
		Capability string `json:"capability"`
		Principal  string `json:"principal"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Capability == "" {
		writeError(w, http.StatusBadRequest, "capability required")
		return
	}
	mut := protection.NewOperatorQuotaMutation(s.gate.QuotaStore(), s.gate.Audit(), nil)
	res, err := mut.Clear(r.Context(), body.Capability, body.Principal, username)
	if errors.Is(err, protection.ErrAuditOutcomeFailed) {
		writeJSON(w, http.StatusOK, map[string]any{
			"capability": res.Capability,
			"principal":  res.Principal,
			"action":     res.Action,
			"operator":   res.Operator,
			"at":         res.At.Format("2006-01-02T15:04:05Z07:00"),
			"degraded":   true,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota clear failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capability": res.Capability,
		"principal":  res.Principal,
		"action":     res.Action,
		"operator":   res.Operator,
		"at":         res.At.Format("2006-01-02T15:04:05Z07:00"),
		"degraded":   false,
	})
}
