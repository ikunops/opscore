package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/governancepolicy"
)

// maxBodyBytes caps a mutation body. A write boundary that will read anything
// a client sends is a denial-of-service surface, and no legitimate policy
// document approaches this size.
const maxBodyBytes = 1 << 20 // 1 MiB

// RuleDTO is the wire form of a policy rule. It mirrors governancepolicy.Rule
// (itself governance.Rule) field for field — no field is invented and none is
// dropped, so the wire contract cannot drift away from the stored model.
type RuleDTO struct {
	RuleID   string `json:"rule_id"`
	Priority int    `json:"priority"`
	Kind     string `json:"kind"`
	Param    string `json:"param,omitempty"`
}

// PolicyRequest is the create/update body. There is no Status field: lifecycle
// is repository-owned, and letting a caller post a status would be a lifecycle
// decision taken at the write boundary (P17-4).
type PolicyRequest struct {
	PolicyID string    `json:"policy_id"`
	Rules    []RuleDTO `json:"rules"`
}

// PolicyResponse is the representation returned by every successful mutation.
// Revision is the value the caller must send back as If-Match next time, which
// is why it is also echoed as an ETag header.
type PolicyResponse struct {
	PolicyID    string     `json:"policy_id"`
	Revision    int        `json:"revision"`
	Status      string     `json:"status"`
	Rules       []RuleDTO  `json:"rules"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

// toResponse projects a stored record onto the wire. A zero ActivatedAt is
// omitted rather than serialised as year 1 — "never activated" and "activated
// at the zero instant" must not look alike.
func toResponse(rec governancepolicy.PolicyRecord) PolicyResponse {
	out := PolicyResponse{
		PolicyID:  rec.PolicyID,
		Revision:  rec.Revision,
		Status:    string(rec.Status),
		Rules:     make([]RuleDTO, 0, len(rec.Rules)),
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
	for _, r := range rec.Rules {
		out.Rules = append(out.Rules, RuleDTO{
			RuleID:   r.RuleID,
			Priority: r.Priority,
			Kind:     string(r.Kind),
			Param:    r.Param,
		})
	}
	if !rec.ActivatedAt.IsZero() {
		t := rec.ActivatedAt
		out.ActivatedAt = &t
	}
	return out
}

// admittedRuleKinds is the closed enum declared by internal/governance, reached
// here through the governancepolicy aliases so the literals are not duplicated.
var admittedRuleKinds = map[governancepolicy.RuleKind]bool{
	governancepolicy.RuleMaintenanceWindow: true,
	governancepolicy.RuleChangeFreeze:      true,
	governancepolicy.RuleRequireApproval:   true,
	governancepolicy.RuleTenantScope:       true,
	governancepolicy.RuleGroupAllow:        true,
}

// validateRules performs WRITE-TIME structural validation and nothing more
// (ADR-036 §3.4 / §4.5.2: Engine.Evaluate is not reachable from here, and
// write-time validation is a different concern from runtime evaluation).
//
// What is checked, and why each check is a STRUCTURAL invariant already
// declared elsewhere rather than a new rule invented at the boundary:
//
//   - Kind must be one of the five declared RuleKind values. The enum is closed
//     (internal/governance/model.go). An unknown kind is silently ignored by
//     the engine, which would let an operator store a policy that reads as if
//     it does something and does nothing.
//   - RuleID must be non-empty and unique within the policy. The engine
//     documents RuleID as the Explain/Evidence handle and as the tie-breaker
//     that makes ordering deterministic ("ties are broken by RuleID
//     ascending"). Duplicates break that stated determinism.
//
// What is deliberately NOT checked: whether Param is required for a given Kind.
// governance documents Param as "empty for parameter-less rules" but never says
// which kinds require it, and the engine assigns meaning to an empty Param
// (tenant-scope with an empty Param denies every tenant). Rejecting it here
// would be the Management surface inventing policy semantics — exactly what
// P17-4 forbids. Flagged as a known, deliberate gap rather than papered over.
func validateRules(in []RuleDTO) ([]governancepolicy.Rule, *apiError) {
	out := make([]governancepolicy.Rule, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, r := range in {
		id := strings.TrimSpace(r.RuleID)
		if id == "" {
			return nil, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
				"rules[%d]: rule_id must not be empty", i)
		}
		if seen[id] {
			return nil, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
				"rules[%d]: duplicate rule_id %q; rule ids must be unique within a policy", i, id)
		}
		seen[id] = true

		kind := governancepolicy.RuleKind(r.Kind)
		if !admittedRuleKinds[kind] {
			return nil, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
				"rules[%d]: unknown kind %q", i, r.Kind)
		}
		out = append(out, governancepolicy.Rule{
			RuleID:   id,
			Priority: r.Priority,
			Kind:     kind,
			Param:    r.Param,
		})
	}
	return out, nil
}

// validatePolicyID checks the shape the boundary is responsible for. Storage
// safety (path traversal) stays the repository's job — it owns the filesystem
// encoding and returns ErrInvalidID, which maps to 422. Re-implementing that
// check here would create two definitions of "safe id" that can drift.
func validatePolicyID(id string) (string, *apiError) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest, "policy_id must not be empty")
	}
	return trimmed, nil
}

// parseIfMatch reads the expected revision for an EXISTING policy.
//
// Both bare (`If-Match: 3`) and ETag-quoted (`If-Match: "3"`, `W/"3"`) forms are
// accepted, because the header is echoed back as an ETag and a conforming
// client will quote it. A wildcard `If-Match: *` is rejected: it means "any
// current version", which is precisely the lost-update semantics P17-8 exists
// to prevent.
//
// A missing header is 422, not "assume latest". Defaulting would turn every
// client that forgets the header into a silent last-write-wins client.
func parseIfMatch(r *http.Request) (int, *apiError) {
	raw := strings.TrimSpace(r.Header.Get(HeaderIfMatch))
	if raw == "" {
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"%s header is required and must carry the expected revision", HeaderIfMatch)
	}
	if raw == "*" {
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"%s: * is not accepted; an explicit expected revision is required", HeaderIfMatch)
	}
	rev, err := strconv.Atoi(unquoteETag(raw))
	if err != nil {
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"%s: %q is not a revision number", HeaderIfMatch, raw)
	}
	if rev < 1 {
		// Revision 0 means "must not exist" and belongs to create only. Allowing
		// it on update/lifecycle would let a caller express a precondition the
		// verb cannot satisfy.
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"%s: revision must be >= 1 for an existing policy", HeaderIfMatch)
	}
	return rev, nil
}

// rejectIfMatchOnCreate enforces that create carries no stale precondition. The
// expected revision for create is fixed at 0 by CAS-1 ("must not already
// exist"), so the only accepted values are absent or 0.
func rejectIfMatchOnCreate(r *http.Request) *apiError {
	raw := strings.TrimSpace(r.Header.Get(HeaderIfMatch))
	if raw == "" || unquoteETag(raw) == "0" {
		return nil
	}
	return newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
		"%s must be absent or 0 on create; the expected revision is fixed at 0 (must not already exist)", HeaderIfMatch)
}

// unquoteETag strips the optional weak marker and surrounding quotes.
func unquoteETag(s string) string {
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}

// etag renders a revision as an ETag value for the response.
func etag(revision int) string { return `"` + strconv.Itoa(revision) + `"` }
