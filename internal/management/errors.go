package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/YuDong999/opscore/internal/governancepolicy"
)

// Stable machine-readable error codes. The HTTP status says how to react; the
// code says what happened. Codes are part of the contract — clients branch on
// them, so they may be added but not renamed.
const (
	codeUnauthenticated  = "unauthenticated"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeConflict         = "revision_conflict"
	codeReplayConflict   = "replay_conflict"
	codeIllegal          = "illegal_transition"
	codeInvalidRequest   = "invalid_request"
	codeAuditUnavailable = "audit_unavailable"
	codeAuditUnrecorded  = "audit_outcome_unrecorded"
	codeInternal         = "internal_error"
	// codeEvidenceUnavailable is the single failure code for every READ of the
	// audit trail (Phase 18, ADR-040 §3.2). It is paired with 503, not 500,
	// because the request was well-formed and the server is not broken — the
	// answer is currently UNKNOWABLE. 503 tells an operator "retry, do not
	// conclude"; 500 invites "the tool is buggy, ignore it"; and `200 []`, the
	// Phase 17.3 behaviour, tells them "all clear", which is a lie.
	//
	// Distinct from codeAuditUnavailable, which is a WRITE-side condition: the
	// mutation was refused because its intent could not be recorded.
	codeEvidenceUnavailable = "evidence_unavailable"
	// codeMetricsUnavailable is the failure code for the metrics read when the
	// collector is unavailable (Phase 19, S-1 / R19-7). Paired with 503, not
	// 200-with-empty: a Prometheus scraper that sees 200 would record an
	// all-zero sample that masks a real collector outage, which is the Phase 18
	// false-clean defect migrated into the consumer.
	codeMetricsUnavailable = "metrics_unavailable"
	// codeTraceEvidenceUnavailable is the failure code for the traces read when
	// the trace ring is unavailable (Phase 20, ADR-045 §5 / R20-10). Paired
	// with 503, not 200-with-empty: a missing ring is an UNKNOWABLE answer, not
	// evidence of absence. The same honest-discipline rule as codeMetricsUnavailable.
	codeTraceEvidenceUnavailable = "trace_evidence_unavailable"
)

// errorEnvelope is the frozen response shape (ADR-036 §3.5):
// {"error": {"code": "...", "message": "..."}}.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiError is a transport-shaped failure. It carries the status, the stable
// code and an operator-facing message.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("management: %d %s: %s", e.status, e.code, e.message)
}

func newAPIError(status int, code, format string, args ...interface{}) *apiError {
	return &apiError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

var (
	errUnauthenticated = &apiError{
		status:  http.StatusUnauthorized,
		code:    codeUnauthenticated,
		message: "missing or invalid " + HeaderToken,
	}
	errForbidden = &apiError{
		status:  http.StatusForbidden,
		code:    codeForbidden,
		message: "principal lacks capability " + CapabilityPolicyManage,
	}
)

// repositoryError maps a repository sentinel onto the frozen error contract
// (ADR-036 §3.5).
//
// illegalStatus is a PARAMETER because ADR-036 assigns two different codes to
// the one ErrIllegalTransition sentinel, depending on the verb:
//
//   - §3.4 "update rejects an Active target with 422 (or 409)" — update is a
//     validation failure: the caller sent content for a record that is not in a
//     writable state, and no revision would fix it.
//   - §4 "Anything outside it — Archived -> Active, Archived -> Draft — is
//     ErrIllegalTransition ⇒ 409" — a lifecycle move is a state conflict:
//     the request is well-formed, the record is simply not where the caller
//     believes it is.
//
// Encoding that difference at the call site keeps both frozen statements true
// instead of quietly picking one and contradicting the other.
func repositoryError(err error, illegalStatus int) *apiError {
	switch {
	case errors.Is(err, governancepolicy.ErrNotFound):
		return newAPIError(http.StatusNotFound, codeNotFound, "policy not found")
	case errors.Is(err, governancepolicy.ErrRevisionConflict):
		return newAPIError(http.StatusConflict, codeConflict,
			"revision conflict: the stored revision does not match %s; refetch and retry", HeaderIfMatch)
	case errors.Is(err, governancepolicy.ErrIllegalTransition):
		return newAPIError(illegalStatus, codeIllegal, "lifecycle transition not admitted for the policy's current status")
	case errors.Is(err, governancepolicy.ErrInvalidID):
		return newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest, "invalid policy id")
	default:
		// The store failed for a reason that is not part of the contract (disk,
		// permissions, corruption). It is reported as an internal error WITHOUT
		// echoing the raw error to the caller — the audit outcome row carries
		// the detail for the operator.
		return newAPIError(http.StatusInternalServerError, codeInternal, "policy store failure")
	}
}

// writeError emits the frozen envelope. It never leaks a Go error string that
// was not deliberately composed for a caller.
func writeError(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Code: e.code, Message: e.message}})
}

// writeJSON emits a success body.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
