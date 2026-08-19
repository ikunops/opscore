package management

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/storage"
	"github.com/YuDong999/opscore/internal/tracing"
)

// RoutePrefix is the URL space owned by the Management surface. It is exported
// so the composition root can assert mechanically that NOTHING under it is ever
// mounted on the external/v1 mux (ADR-036 §3.6 surface isolation).
const RoutePrefix = "/management/v1/"

// Config is the constructor input. Every field is a dependency the surface
// consumes; none of them is state it owns (P17-3).
type Config struct {
	// Repo is the single owner of policy state. Management mutates ONLY
	// through its two revision-aware primitives.
	Repo governancepolicy.Repository
	// Audit is the durable, error-returning audit store. It is deliberately
	// NOT core.AuditSink: that returns void and hard-codes Action "execute",
	// which makes MUST-P17-13 unsatisfiable and corrupts the trail (§4.6.3).
	Audit storage.AuditStore
	// Authenticator is mandatory. A nil value is a startup failure, not a
	// default-open fallback (MUST-P17-14).
	Authenticator Authenticator
	// Authorizer defaults to CapabilityAuthorizer, which is default-deny.
	Authorizer Authorizer
	// NewCorrelationID is injectable so tests can assert on exact chains.
	NewCorrelationID func() string
	// Collector is the exact-aggregate metrics source (Phase 19 S-1 / R19-7).
	// It is REQUIRED: a nil collector is a startup failure, not a default-empty
	// metrics answer, because the consumer must never record an all-zero sample
	// that masks a collector outage (the Phase 18 false-clean defect, migrated
	// into the evidence consumer).
	Collector *observability.Collector
	// TraceRing is the bounded causal-trace span store (Phase 20, ADR-045). It
	// is OPTIONAL: a nil ring is NOT a startup failure — it makes the traces
	// read surface answer 503 trace_evidence_unavailable at request time, so a
	// missing ring is a retryable "unknown", never a 200-with-empty false-clean
	// (R20-10). Keeping it optional preserves the existing harness wiring tests,
	// which construct the surface without a ring.
	TraceRing *tracing.TraceRing
}

// Server is the Management API surface.
type Server struct {
	repo             governancepolicy.Repository
	audit            storage.AuditStore
	authn            Authenticator
	authz            Authorizer
	newCorrelationID func() string
	// reconciler is the read-only audit/reconciliation observer (ADR-038
	// §3.2). It is constructed from the same audit + repo the surface already
	// holds; it never writes.
	reconciler *Reconciler
	// collector is the exact-aggregate metrics source (Phase 19 S-1). It is
	// wired from Config.Collector and is never nil once the surface is built.
	collector *observability.Collector
	// traceRing is the bounded causal-trace span store (Phase 20, ADR-045). It
	// may be nil if the harness did not wire one; the traces read surface then
	// answers 503 (R20-10 evidence unavailable).
	traceRing *tracing.TraceRing
}

// New builds the surface and FAILS CLOSED (MUST-P17-14). A missing
// authentication prerequisite returns ErrAuthPrerequisiteMissing so the
// composition root can decline to bind :8082 at all — rather than binding a
// port that answers 401 to everything while looking exactly as healthy as the
// read surface (§4.5.3).
func New(cfg Config) (*Server, error) {
	if cfg.Repo == nil {
		return nil, errors.New("management: policy repository is required")
	}
	if cfg.Audit == nil {
		return nil, errors.New("management: audit store is required")
	}
	if cfg.Authenticator == nil {
		return nil, ErrAuthPrerequisiteMissing
	}
	if cfg.Collector == nil {
		return nil, errors.New("management: observability collector is required")
	}
	authz := cfg.Authorizer
	if authz == nil {
		authz = CapabilityAuthorizer{}
	}
	gen := cfg.NewCorrelationID
	if gen == nil {
		gen = defaultCorrelationID
	}
	return &Server{
		repo:             cfg.Repo,
		audit:            cfg.Audit,
		authn:            cfg.Authenticator,
		authz:            authz,
		newCorrelationID: gen,
		reconciler:       newReconciler(cfg.Audit, cfg.Repo),
		collector:        cfg.Collector,
		traceRing:        cfg.TraceRing,
	}, nil
}

// RoutePatterns is the exact, complete set of patterns this surface registers.
// It exists so a wiring test can assert the external mux carries none of them,
// instead of a human comparing two lists by eye.
func RoutePatterns() []string {
	return []string{
		"POST " + RoutePrefix + "policies",
		"PUT " + RoutePrefix + "policies/{id}",
		"POST " + RoutePrefix + "policies/{id}/activate",
		"POST " + RoutePrefix + "policies/{id}/deactivate",
		"POST " + RoutePrefix + "policies/{id}/archive",
		// Phase 17.3 read-only audit surface (ADR-038 §3.3, R17-6). GET only;
		// all routes live under the management namespace and inherit the same
		// AuthN+AuthZ gate as the mutations, so an unauthenticated GET is 401.
		"GET " + RoutePrefix + "audit",
		"GET " + RoutePrefix + "reconciliation",
		// Phase 19 read-only evidence consumers (ADR-042 §3.5, R19-1). All three
		// are GET, :8082-only, token-gated, and NON-mutating. They summarize
		// evidence without ever weakening the meaning of absence:
		//   metrics                — EXACT lifetime counters (never the window)
		//   projections/policy-activity — read-time derivation, carries truncation
		//   reconciliation/history — the bounded, non-authoritative scan ring
		"GET " + RoutePrefix + "metrics",
		"GET " + RoutePrefix + "projections/policy-activity",
		"GET " + RoutePrefix + "reconciliation/history",
		// Phase 20 causal-tracing read surface (ADR-045 §5, R20-1). GET,
		// :8082-only, token-gated, NON-mutating. Advisory execution_id -> trace_id
		// resolution via Refs; never a derived/synth trace (R20-10).
		"GET " + RoutePrefix + "traces",
	}
}

// Handler returns the fully wrapped surface: AuthN and AuthZ enclose the WHOLE
// mux, not individual routes.
//
// That placement is deliberate. Wrapping per-route leaves 404 and 405 outside
// the gate, so an unauthenticated caller could map the surface by probing
// paths. Wrapping the mux means an unauthenticated request gets 401 whatever it
// asks for, and there is structurally no route that could be added later
// without inheriting the gate — "no path of the form HTTP -> Handler ->
// Repository that bypasses AuthN/AuthZ" (ADR-036 §2) becomes a property of the
// wiring rather than a promise about future diligence.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+RoutePrefix+"policies", s.handleCreatePolicy)
	mux.HandleFunc("PUT "+RoutePrefix+"policies/{id}", s.handleUpdatePolicy)
	mux.HandleFunc("POST "+RoutePrefix+"policies/{id}/activate", s.handleActivatePolicy)
	mux.HandleFunc("POST "+RoutePrefix+"policies/{id}/deactivate", s.handleDeactivatePolicy)
	mux.HandleFunc("POST "+RoutePrefix+"policies/{id}/archive", s.handleArchivePolicy)
	// Phase 17.3 read-only audit surface (ADR-038 §3.3). Both routes invoke the
	// reconciler / audit read paths ONLY — no write occurs (R17-6 / MUST-17.3-B).
	mux.HandleFunc("GET "+RoutePrefix+"audit", s.handleListAudit)
	mux.HandleFunc("GET "+RoutePrefix+"reconciliation", s.handleReconcile)
	// Phase 19 read-only evidence consumers (ADR-042 §3.5). Each is GET,
	// token-gated by the shared guard, and performs no write (R19-1 / R19-6 /
	// R19-8): metrics exposes exact counters, the policy-activity projection is
	// derived at read time, and scan-history reflects the bounded in-memory ring.
	mux.HandleFunc("GET "+RoutePrefix+"metrics", s.handleMetrics)
	mux.HandleFunc("GET "+RoutePrefix+"projections/policy-activity", s.handlePolicyActivity)
	mux.HandleFunc("GET "+RoutePrefix+"reconciliation/history", s.handleScanHistory)
	// Phase 20 causal-tracing read surface (ADR-045 §5). Advisory resolution via
	// Refs; returns 503 when no ring is wired (R20-10 evidence unavailable).
	mux.HandleFunc("GET "+RoutePrefix+"traces", s.handleTraces)
	return s.guard(mux)
}

// guard is the AuthN + AuthZ gate.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authn.Authenticate(r)
		if err != nil {
			writeError(w, errUnauthenticated)
			return
		}
		if err := s.authz.Authorize(p, CapabilityPolicyManage); err != nil {
			writeError(w, errForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// ---------------------------------------------------------------------------
// The pipeline
// ---------------------------------------------------------------------------

// mutation is everything a verb contributes to the frozen pipeline. Note what
// is NOT here: the audit writes, their order, and the failure mapping. Handlers
// supply the single CAS call; they do not own the steps around it, so they
// cannot skip or reorder them.
type mutation struct {
	action   string // frozen policy.* vocabulary
	policyID string
	expected int // the If-Match revision; 0 for create (CAS-1)
	okStatus int // status for the plain success path

	// illegalStatus is the code for ErrIllegalTransition, which ADR-036 maps
	// differently per verb: 422 for update-on-Active (§3.4), 409 for an
	// inadmissible lifecycle move (§4). See repositoryError.
	illegalStatus int

	// commit performs the ONE compare-and-swap call. It is the only place in
	// this package where a write happens.
	commit func() (governancepolicy.PolicyRecord, error)

	// onConflict is consulted ONLY after commit reported ErrRevisionConflict.
	// It performs a READ-ONLY Get and never writes, which is what keeps the
	// create-replay branch outside the forbidden Get -> check -> Save
	// (ADR-036 §3.4: the mutation attempt already happened and was refused
	// INSIDE the repository, so no lost update is possible; the read only
	// shapes the response).
	onConflict func() conflictVerdict
}

// conflictVerdict is how a verb resolves a refused CAS.
type conflictVerdict struct {
	// record/status/replayed: the request was an idempotent replay and must be
	// answered as a success with this record and status.
	record   governancepolicy.PolicyRecord
	status   int
	replayed bool
	// override replaces the default 409 with a more precise failure.
	override *apiError
}

// serveMutation is the single implementation of ADR-036 §2, steps 4-7.
func (s *Server) serveMutation(w http.ResponseWriter, r *http.Request, m mutation) {
	actor := principalFrom(r.Context()).ID

	// --- correlationID replay guard (MUST-17.3-A, ADR-038 §3.4) -----------------
	// Placement: BEFORE STEP 1 (INTENT). The caller MAY supply a key via the
	// Idempotency-Key header; if they do, a request whose key already has a
	// TERMINAL outcome is strictly rejected with 409 — there is NO allow mode.
	// The repository CAS remains the authoritative atomic gate for true
	// concurrent double-applies; this guard stops *obvious* replays.
	supplied := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if supplied != "" {
		if tail, _ := s.audit.ListByCorrelation(supplied, 4); hasTerminalOutcome(tail) {
			writeError(w, newAPIError(http.StatusConflict, codeReplayConflict,
				"correlation id %q already has a terminal outcome; replay rejected", supplied))
			return
		}
	}
	corr := s.newCorrelationID()
	if supplied != "" {
		corr = supplied
	}
	// Emitted before anything can fail, so even a 503 or a degraded 500 hands
	// the operator the key they need to find the rows.
	w.Header().Set(HeaderCorrelationID, corr)

	// STEP 1 — durable, error-checked INTENT. Revision carries the EXPECTED
	// revision (0 for create), per the frozen table in §3.3.2.1.
	intent := auditRecord{
		actor:         actor,
		action:        m.action,
		policyID:      m.policyID,
		result:        resultIntent,
		revision:      m.expected,
		correlationID: corr,
		detail:        detailf("phase", "intent", "expected_revision", itoa(m.expected)),
	}
	if _, err := s.audit.Append(intent.event()); err != nil {
		// No durable intent ⇒ mutation FORBIDDEN. This is the one place the
		// surface refuses work it could otherwise perform, and it is the whole
		// content of MUST-P17-13's first clause.
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeAuditUnavailable,
			"audit intent could not be recorded; the mutation was NOT attempted"))
		return
	}

	// STEP 2 — the only mutation point.
	rec, err := m.commit()

	// STEP 3 — durable, error-checked OUTCOME.
	if err != nil {
		s.finishFailure(w, m, actor, corr, err)
		return
	}
	s.finishSuccess(w, m, actor, corr, rec, m.okStatus, "committed")
}

// finishSuccess records the success outcome and answers.
//
// Note what the audit chain already encodes without a special marker: a CT-8
// no-op (activate on an already-Active policy) commits with the revision
// UNCHANGED, so an intent row with expected=N followed by a success row with
// committed=N is, by itself, the proof that nothing moved. The pair is the
// evidence; no extra flag is needed and none is invented.
func (s *Server) finishSuccess(w http.ResponseWriter, m mutation, actor, corr string, rec governancepolicy.PolicyRecord, status int, note string) {
	outcome := auditRecord{
		actor:         actor,
		action:        m.action,
		policyID:      m.policyID,
		result:        resultSuccess,
		revision:      rec.Revision,
		correlationID: corr,
		detail: detailf(
			"phase", "outcome",
			"expected_revision", itoa(m.expected),
			"committed_revision", itoa(rec.Revision),
			"note", note,
		),
	}
	if _, err := s.audit.Append(outcome.event()); err != nil {
		// Permitted, identifiable degraded state — NOT a rollback and not a
		// transaction (MUST-P17-13). The mutation stands; the caller is told
		// so, and told how to find the dangling intent row.
		writeError(w, degraded(corr, true))
		return
	}
	w.Header().Set("ETag", etag(rec.Revision))
	writeJSON(w, status, toResponse(rec))
}

// finishFailure resolves a refused mutation, records the failure outcome and
// answers.
func (s *Server) finishFailure(w http.ResponseWriter, m mutation, actor, corr string, err error) {
	apiErr := repositoryError(err, m.illegalStatus)

	if errors.Is(err, governancepolicy.ErrRevisionConflict) && m.onConflict != nil {
		v := m.onConflict()
		if v.replayed {
			s.finishSuccess(w, m, actor, corr, v.record, v.status, "idempotent-replay-no-mutation")
			return
		}
		if v.override != nil {
			apiErr = v.override
		}
	}

	// R76 requires the actual observed revision "when obtainable". Obtaining it
	// costs one READ-ONLY Get and no write follows on this path.
	actual := s.observeRevision(m.policyID)
	outcome := auditRecord{
		actor:         actor,
		action:        m.action,
		policyID:      m.policyID,
		result:        resultFailure,
		revision:      actual,
		correlationID: corr,
		detail: detailf(
			"phase", "outcome",
			"expected_revision", itoa(m.expected),
			"actual_revision", itoa(actual),
			"reason", apiErr.code,
		),
	}
	if _, aerr := s.audit.Append(outcome.event()); aerr != nil {
		// The mutation did NOT happen, so the degraded state here is a dangling
		// intent with no state change — trivial to reconcile, but still
		// reported rather than hidden behind the original 409.
		writeError(w, degraded(corr, false))
		return
	}
	writeError(w, apiErr)
}

// observeRevision reads the currently stored revision for the audit trail. It
// is READ-ONLY and never followed by a write on any call path.
func (s *Server) observeRevision(policyID string) int {
	rec, ok, err := s.repo.Get(policyID)
	if err != nil || !ok {
		return revisionUnknown
	}
	return rec.Revision
}

// degraded describes the cross-store gap honestly, in the caller's response.
func degraded(corr string, applied bool) *apiError {
	what := "was NOT applied"
	if applied {
		what = "WAS applied"
	}
	return newAPIError(http.StatusInternalServerError, codeAuditUnrecorded,
		"the mutation %s, but its audit outcome could not be recorded; an intent row exists with no outcome row — reconcile using correlation id %s",
		what, corr)
}

// ---------------------------------------------------------------------------
// Handlers — each supplies exactly one CAS call
// ---------------------------------------------------------------------------

// handleCreatePolicy: POST /management/v1/policies → CompareAndSave(rec, 0).
func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	if apiErr := rejectIfMatchOnCreate(r); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	req, apiErr := decodePolicyRequest(w, r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	id, apiErr := validatePolicyID(req.PolicyID)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	rules, apiErr := validateRules(req.Rules)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}

	candidate := governancepolicy.PolicyRecord{PolicyID: id, Rules: rules}
	s.serveMutation(w, r, mutation{
		action:   ActionCreate,
		policyID: id,
		expected: 0,
		okStatus: http.StatusCreated,
		// A create can only trip ErrIllegalTransition through a malformed
		// target, which is a request problem, not a state conflict.
		illegalStatus: http.StatusUnprocessableEntity,
		commit: func() (governancepolicy.PolicyRecord, error) {
			return s.repo.CompareAndSave(candidate, 0)
		},
		onConflict: func() conflictVerdict {
			// The store-derived idempotency branch (ADR-036 §3.4). CAS-1 held
			// literally: the write was refused inside the repository. This read
			// only decides 200 vs 409.
			existing, ok, err := s.repo.Get(id)
			if err != nil {
				return conflictVerdict{override: newAPIError(http.StatusInternalServerError, codeInternal, "policy store failure")}
			}
			if !ok {
				// The record existed when the CAS refused and is gone now.
				// Nothing here is safe to call a replay, so the refusal stands.
				return conflictVerdict{}
			}
			if governancepolicy.SameContent(existing, candidate) {
				return conflictVerdict{record: existing, status: http.StatusOK, replayed: true}
			}
			return conflictVerdict{override: newAPIError(http.StatusConflict, codeConflict,
				"policy %q already exists with different content; create never overwrites", id)}
		},
	})
}

// handleUpdatePolicy: PUT /management/v1/policies/{id} →
// CompareAndSave(rec, ifMatch). Draft-only; the repository enforces it.
func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id, apiErr := validatePolicyID(r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	expected, apiErr := parseIfMatch(r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	req, apiErr := decodePolicyRequest(w, r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	// A body id, if present, must agree with the path. Silently preferring one
	// would let a caller believe they updated a policy they never named.
	if bodyID := strings.TrimSpace(req.PolicyID); bodyID != "" && bodyID != id {
		writeError(w, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"policy_id %q in the body does not match %q in the path", bodyID, id))
		return
	}
	rules, apiErr := validateRules(req.Rules)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}

	s.serveMutation(w, r, mutation{
		action:   ActionUpdate,
		policyID: id,
		expected: expected,
		okStatus: http.StatusOK,
		// update-on-Active is a validation failure: the content is fine, the
		// target simply is not writable and no revision would make it so
		// (ADR-036 §3.4).
		illegalStatus: http.StatusUnprocessableEntity,
		commit: func() (governancepolicy.PolicyRecord, error) {
			return s.repo.CompareAndSave(governancepolicy.PolicyRecord{PolicyID: id, Rules: rules}, expected)
		},
	})
}

func (s *Server) handleActivatePolicy(w http.ResponseWriter, r *http.Request) {
	s.handleLifecycle(w, r, ActionActivate, governancepolicy.StatusActive)
}

func (s *Server) handleDeactivatePolicy(w http.ResponseWriter, r *http.Request) {
	// Deactivate returns the record to Draft: no separate "inactive" state
	// exists and Phase 17 invents no new lifecycle state (ADR-036 §4).
	s.handleLifecycle(w, r, ActionDeactivate, governancepolicy.StatusDraft)
}

func (s *Server) handleArchivePolicy(w http.ResponseWriter, r *http.Request) {
	s.handleLifecycle(w, r, ActionArchive, governancepolicy.StatusArchived)
}

// handleLifecycle: POST /management/v1/policies/{id}/{verb} →
// CompareAndTransition(id, ifMatch, target).
func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request, action string, target governancepolicy.PolicyStatus) {
	id, apiErr := validatePolicyID(r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	expected, apiErr := parseIfMatch(r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	s.serveMutation(w, r, mutation{
		action:   action,
		policyID: id,
		expected: expected,
		okStatus: http.StatusOK,
		// An inadmissible lifecycle move is a state conflict, not bad input
		// (ADR-036 §4).
		illegalStatus: http.StatusConflict,
		commit: func() (governancepolicy.PolicyRecord, error) {
			return s.repo.CompareAndTransition(id, expected, target)
		},
	})
}

// decodePolicyRequest reads and validates the request body shape.
//
// Unknown fields are REJECTED. On a read API tolerating them is friendly; on a
// write API it means a client that misspells "rules" gets 201 and an empty
// policy. Silence is the wrong answer when the request changes stored state.
func decodePolicyRequest(w http.ResponseWriter, r *http.Request) (PolicyRequest, *apiError) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req PolicyRequest
	if err := dec.Decode(&req); err != nil {
		return PolicyRequest{}, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"malformed request body: %v", err)
	}
	// Exactly one JSON document per request; trailing content is a sign the
	// client is sending something other than what it thinks.
	if dec.More() {
		return PolicyRequest{}, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest,
			"request body must contain exactly one JSON object")
	}
	return req, nil
}

// ---------------------------------------------------------------------------
// Correlation ids
// ---------------------------------------------------------------------------

// correlationFallback backs the generator if the system CSPRNG fails.
var correlationFallback uint64

// defaultCorrelationID produces a unique per-request id.
//
// The requirement is UNIQUENESS, not secrecy: the id ties two audit rows
// together and is returned only to the already-authenticated caller who caused
// them. crypto/rand is used because it is the cheapest correct source of
// uniqueness, and its failure falls back to time+counter rather than panicking
// inside a handler or, worse, returning a constant that would silently merge
// every request's audit chain into one.
func defaultCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	n := atomic.AddUint64(&correlationFallback, 1)
	return fmt.Sprintf("fallback-%d-%s", time.Now().UnixNano(), strconv.FormatUint(n, 16))
}

// ---------------------------------------------------------------------------
// Phase 17.3 read-only audit surface (ADR-038 §3.3)
// ---------------------------------------------------------------------------

// handleListAudit serves GET /management/v1/audit — a read-only view of the
// management-owned audit trail. It inherits the package guard (AuthN+AuthZ wrap
// the whole mux), so an unauthenticated GET is 401 (R17-7). It performs no
// write (R17-6). The read-model shaping (policy/result filtering) lives here,
// not in a frozen package (R17-5).
// Phase 18 (ADR-040 §3.1/§3.2) changes two things here:
//
//   - the predicate is pushed into the STORE. Filtering a page that was already
//     truncated to `limit` made the oldest matching row invisible and returned
//     an empty list that read as "no such events" (ADR-039 §2 F-3).
//   - the response is the AuditPage envelope {events, limit, truncated}, not a
//     bare array. A bare array is structurally incapable of carrying the window
//     metadata R18-2 requires, and without it an empty `events` is ambiguous.
//
// An unreadable store answers 503 evidence_unavailable — never 200 with an
// empty body, and no longer 500.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// A malformed or non-positive limit is left at zero so the store applies
	// its documented default; the effective value comes back in the envelope,
	// so the caller is never guessing which limit was used.
	limit := 0
	if l := strings.TrimSpace(q.Get("limit")); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	// Phase 19 additive cursor (ADR-042 §3.2). After is an EXCLUSIVE id bound;
	// After==0 is the wildcard, so a caller that omits ?after sees identical
	// behaviour and no offset paging is introduced. Applied to the store, never
	// re-filtered after the page is built.
	after := int64(0)
	if a := strings.TrimSpace(q.Get("after")); a != "" {
		if n, err := strconv.ParseInt(a, 10, 64); err == nil && n > 0 {
			after = n
		}
	}

	page, err := s.audit.Query(storage.AuditQuery{
		Target: strings.TrimSpace(q.Get("policy")),
		Result: strings.TrimSpace(q.Get("result")),
		Limit:  limit,
		After:  after,
	})
	if err != nil {
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeEvidenceUnavailable,
			"the audit trail could not be read; retry — this is NOT an absence of events"))
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleReconcile serves GET /management/v1/reconciliation — runs the
// read-only Scan and returns its report (ADR-038 §3.3, MUST-17.3-B). It never
// mutates audit or policy state; reconciliation is operator VISIBILITY, not
// auto-heal.
// Phase 18 (ADR-040 §3.2): the response is the ScanReport envelope
// {status, window, entries}. `entries: []` alone is not an answer — it is an
// answer only once `status` says whether the search could see anything. A
// failed scan is 503 evidence_unavailable, so the "all clear" reading is not
// available to a client that only checks the status code.
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	report, err := s.reconciler.Scan(r.Context())
	if err != nil {
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeEvidenceUnavailable,
			"the audit trail could not be read; the reconciliation result is unknown — retry, do not conclude"))
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// ---------------------------------------------------------------------------
// Phase 19 — Evidence consumers (ADR-042 §3)
// ---------------------------------------------------------------------------

// handleMetrics serves GET /management/v1/metrics (S-1, R19-7). It renders the
// collector's EXACT lifetime counters in Prometheus exposition format — never
// the bounded window. A nil collector is NOT a "0 metrics" answer; it is 503
// metrics_unavailable so a scraper retries rather than recording an all-zero
// sample that would mask a real collector outage (Phase 18 false-clean,
// migrated into the consumer).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.collector == nil {
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeMetricsUnavailable,
			"the metrics collector is unavailable; retry — do not record an all-zero sample that would mask an outage"))
		return
	}
	body := renderPrometheus(s.collector.Counters())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// handlePolicyActivity serves GET /management/v1/projections/policy-activity
// (S-3, R19-6). The projection is DERIVED from a single bounded AuditQuery at
// read time; it persists nothing and never weakens absence: the page's
// Truncated flag (and the projection's redundant copy) are carried through, so
// an empty projection is always distinguishable from "all clear". A failed read
// is 503 evidence_unavailable, not 200 with empty.
func (s *Server) handlePolicyActivity(w http.ResponseWriter, r *http.Request) {
	page, err := s.audit.Query(storage.AuditQuery{Limit: storage.MaxAuditQueryLimit})
	if err != nil {
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeEvidenceUnavailable,
			"the audit trail could not be read; the policy-activity projection is unknown — retry, do not conclude"))
		return
	}
	proj := PolicyActivityProjection{
		Truncated: page.Truncated,
		Window: ScanWindow{
			Scanned:   len(page.Events),
			Cap:       storage.MaxAuditQueryLimit,
			Truncated: page.Truncated,
		},
		Policies: aggregatePolicyActivity(page.Events),
	}
	writeJSON(w, http.StatusOK, proj)
}

// handleScanHistory serves GET /management/v1/reconciliation/history (S-4,
// R19-8). It returns the bounded, NON-authoritative ring of recent Scan
// reports. Absence from the ring is not absence from history; the Truncated
// flag says whether older scans were evicted. The ring is read-only here.
func (s *Server) handleScanHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.reconciler.ScanHistory())
}

// ---------------------------------------------------------------------------
// Phase 20 — Causal tracing read surface (ADR-045 §5)
// ---------------------------------------------------------------------------

// traceResponse is the frozen traces read contract:
// {"trace_id": "...", "spans": [...], "truncated": bool}.
type traceResponse struct {
	TraceID   string         `json:"trace_id"`
	Spans     []tracing.Span `json:"spans"`
	Truncated bool           `json:"truncated"`
}

// handleTraces serves GET /management/v1/traces (ADR-045 §5). It is the ONLY
// new read surface for Phase 20 and enforces R20-10 end to end:
//
//   - A nil ring is 503 trace_evidence_unavailable — availability is never a
//     clean absence (the Phase 18 false-clean discipline, carried into traces).
//   - Exactly one of execution_id / trace_id is required; otherwise 400.
//   - execution_id -> trace_id is ADVISORY: resolved via Refs, never derived. A
//     missing Refs match returns 404 — no TraceID is ever synthesized from the
//     ExecutionID.
//   - A successful 200 always carries truncated: eviction is never silently
//     dropped from the response.
func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	if s.traceRing == nil {
		writeError(w, newAPIError(http.StatusServiceUnavailable, codeTraceEvidenceUnavailable,
			"the trace ring is unavailable; retry — do not conclude the execution has no trace"))
		return
	}
	q := r.URL.Query()
	execID := strings.TrimSpace(q.Get("execution_id"))
	traceID := strings.TrimSpace(q.Get("trace_id"))
	if execID != "" && traceID != "" {
		writeError(w, newAPIError(http.StatusBadRequest, codeInvalidRequest,
			"supply exactly one of execution_id or trace_id, not both"))
		return
	}
	if execID == "" && traceID == "" {
		writeError(w, newAPIError(http.StatusBadRequest, codeInvalidRequest,
			"exactly one of execution_id or trace_id is required"))
		return
	}
	// Advisory resolution: execution_id -> trace_id is a Refs lookup, never a
	// derivation (R20-10). Missing Refs => 404, no synthesized TraceID.
	if execID != "" {
		traceID = s.resolveTraceByExecution(execID)
		if traceID == "" {
			writeError(w, newAPIError(http.StatusNotFound, codeNotFound,
				"no trace references the given execution_id (advisory resolution only; no trace is synthesized from an execution id)"))
			return
		}
	}
	spans, truncated := s.traceRing.QueryByTrace(traceID)
	if len(spans) == 0 {
		writeError(w, newAPIError(http.StatusNotFound, codeNotFound,
			"no spans found for the given trace_id"))
		return
	}
	writeJSON(w, http.StatusOK, traceResponse{
		TraceID:   traceID,
		Spans:     spans,
		Truncated: truncated,
	})
}

// resolveTraceByExecution is the advisory half of R20-10: it returns the trace
// id of the first span that carries a matching "execution" ref. It never derives
// or synthesizes an id — an unknown execution yields "".
func (s *Server) resolveTraceByExecution(execID string) string {
	spans, _ := s.traceRing.QueryByRef("execution", execID)
	for _, sp := range spans {
		if sp.TraceID != "" {
			return sp.TraceID
		}
	}
	return ""
}

// PolicyActivityProjection is the S-3 read-time view (R19-6). It is derived
// from a single bounded AuditQuery and carries the window's truncation, so a
// consumer can never read an empty projection as "nothing happened". It
// persists nothing — the audit store remains the source of truth.
type PolicyActivityProjection struct {
	// Truncated is true when the derived window over MaxAuditQueryLimit rows was
	// itself truncated; older activity exists beyond this projection.
	Truncated bool `json:"truncated"`
	// Window states the scope of the derivation (mirrors ScanWindow for parity).
	Window ScanWindow `json:"window"`
	// Policies is the per-policy activity within the window.
	Policies []PolicyActivity `json:"policies"`
}

// PolicyActivity is one policy's event count within the projection window.
type PolicyActivity struct {
	PolicyID string `json:"policy_id"`
	Events   int    `json:"events"`
}

// aggregatePolicyActivity groups events by policy id, preserving first-seen
// order. An empty Target is skipped (it is not a policy and would collapse
// unrelated rows).
func aggregatePolicyActivity(events []storage.AuditEvent) []PolicyActivity {
	counts := make(map[string]int)
	order := make([]string, 0, len(events))
	for _, e := range events {
		if e.Target == "" {
			continue
		}
		if _, seen := counts[e.Target]; !seen {
			order = append(order, e.Target)
		}
		counts[e.Target]++
	}
	out := make([]PolicyActivity, 0, len(order))
	for _, id := range order {
		out = append(out, PolicyActivity{PolicyID: id, Events: counts[id]})
	}
	return out
}

// renderPrometheus converts the label-keyed counter map into Prometheus
// exposition text. Keys are "name|k=v|k=v" with labels already sorted
// alphabetically (see collector.counterKey), so the output renders labels in a
// stable, deterministic order. Every family gets one HELP/TYPE line. A nil or
// zero value is rendered as "name 0" (never omitted) so genuine zeros stay
// distinguishable from "not scraped" (R19-7).
func renderPrometheus(counters map[string]int64) string {
	keys := make([]string, 0, len(counters))
	for k := range counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	emittedType := make(map[string]bool)
	for _, key := range keys {
		name, labels := splitCounterKey(key)
		if !emittedType[name] {
			emittedType[name] = true
			sb.WriteString("# HELP ")
			sb.WriteString(name)
			sb.WriteString(" opscore metric\n")
			sb.WriteString("# TYPE ")
			sb.WriteString(name)
			sb.WriteString(" counter\n")
		}
		sb.WriteString(name)
		if len(labels) > 0 {
			sb.WriteString("{")
			for i, lv := range labels {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(lv.name)
				sb.WriteString("=\"")
				sb.WriteString(lv.value)
				sb.WriteString("\"")
			}
			sb.WriteString("}")
		}
		sb.WriteString(" ")
		sb.WriteString(strconv.FormatInt(counters[key], 10))
		sb.WriteString("\n")
	}
	return sb.String()
}

// labelKV is one rendered label pair.
type labelKV struct {
	name  string
	value string
}

// splitCounterKey reverses collector.counterKey: "name|k=v|k=v" -> (name,
// [{k,v}...]). A key with no labels returns (name, nil); the labels are already
// alphabetically sorted by counterKey, so render order is stable.
func splitCounterKey(key string) (string, []labelKV) {
	parts := strings.Split(key, "|")
	if len(parts) == 1 {
		return parts[0], nil
	}
	name := parts[0]
	labels := make([]labelKV, 0, len(parts)-1)
	for _, p := range parts[1:] {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			labels = append(labels, labelKV{name: kv[0], value: kv[1]})
		}
	}
	return name, labels
}

// hasTerminalOutcome reports whether any event in the slice is a terminal audit
// outcome (success or failure). An intent row alone is not terminal — the
// replay guard (MUST-17.3-A) refuses a supplied correlation id only when a
// terminal outcome already exists for it, so a dangling intent does not block a
// legitimate retry under the same key.
func hasTerminalOutcome(events []storage.AuditEvent) bool {
	for _, e := range events {
		if e.Result == resultSuccess || e.Result == resultFailure {
			return true
		}
	}
	return false
}
