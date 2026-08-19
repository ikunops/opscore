package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

const testToken = "s3cr3t-management-token"

// fakeAudit is an in-memory AuditStore with injectable failure. Failure
// injection is the point: MUST-P17-13 is a statement about what happens when
// the audit write FAILS, so a store that always succeeds cannot test it.
type fakeAudit struct {
	mu     sync.Mutex
	rows   []storage.AuditEvent
	failOn func(e storage.AuditEvent) error
	// failRead makes every READ path fail. Phase 18 is a set of statements
	// about what a failed read must look like, so a store whose reads always
	// succeed cannot test any of them (ADR-040 §3.4).
	failRead error
	// failCorr fails ListByCorrelation for specific correlation ids, modelling
	// a per-row evidence failure ("unexaminable") without failing the scan.
	failCorr map[string]error
}

func (f *fakeAudit) Append(e storage.AuditEvent) (storage.AuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != nil {
		if err := f.failOn(e); err != nil {
			return e, err
		}
	}
	e.ID = int64(len(f.rows) + 1)
	f.rows = append(f.rows, e)
	return e, nil
}

func (f *fakeAudit) List(limit int) ([]storage.AuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead != nil {
		return nil, f.failRead
	}
	out := append([]storage.AuditEvent(nil), f.rows...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeAudit) ListByOperation(op string, limit int) ([]storage.AuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead != nil {
		return nil, f.failRead
	}
	var out []storage.AuditEvent
	for _, e := range f.rows {
		if e.Operation == op {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeAudit) ListByCorrelation(correlationID string, limit int) ([]storage.AuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead != nil {
		return nil, f.failRead
	}
	if err, bad := f.failCorr[correlationID]; bad {
		return nil, err
	}
	var out []storage.AuditEvent
	for _, e := range f.rows {
		if e.CorrelationID == correlationID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Query mirrors the Phase 18 store contract (ADR-040 §3.1) so the fixture
// cannot accidentally be more forgiving than production: predicate first,
// newest-first, LIMIT n+1 truncation probe.
func (f *fakeAudit) Query(q storage.AuditQuery) (storage.AuditPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead != nil {
		return storage.AuditPage{}, f.failRead
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = storage.DefaultAuditQueryLimit
	case limit > storage.MaxAuditQueryLimit:
		limit = storage.MaxAuditQueryLimit
	}
	out := make([]storage.AuditEvent, 0, limit)
	var truncated bool
	for i := len(f.rows) - 1; i >= 0; i-- {
		e := f.rows[i]
		// Phase 19 cursor (ADR-042 §3.2): id < After is the contract; After==0
		// is the wildcard. Mirrors memAuditStore.Query so the fixture cannot be
		// more forgiving than production.
		if q.After != 0 && e.ID >= q.After {
			continue
		}
		if q.Target != "" && e.Target != q.Target {
			continue
		}
		if q.Result != "" && e.Result != q.Result {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.CorrelationID != "" && e.CorrelationID != q.CorrelationID {
			continue
		}
		if len(out) == limit {
			truncated = true
			break
		}
		out = append(out, e)
	}
	return storage.AuditPage{Events: out, Limit: limit, Truncated: truncated}, nil
}

func (f *fakeAudit) snapshot() []storage.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.AuditEvent(nil), f.rows...)
}

// stubAuthn returns a fixed principal, so AuthZ can be tested independently of
// AuthN.
type stubAuthn struct {
	p   Principal
	err error
}

func (s stubAuthn) Authenticate(*http.Request) (Principal, error) { return s.p, s.err }

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type kit struct {
	t     *testing.T
	repo  governancepolicy.Repository
	audit *fakeAudit
	ts    *httptest.Server
	// srv is the constructed server, exposed so tests can reach its read-only
	// reconciler (Phase 19 S-4 scan-history surface).
	srv       *Server
	collector *observability.Collector
}

func newKit(t *testing.T) *kit { return newKitWith(t, nil) }

func newKitWith(t *testing.T, authn Authenticator) *kit {
	t.Helper()
	repo, err := governancepolicy.NewFileRepository(t.TempDir())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	audit := &fakeAudit{}
	if authn == nil {
		a, err := NewTokenAuthenticator(testToken, "op-1")
		if err != nil {
			t.Fatalf("authn: %v", err)
		}
		authn = a
	}
	// Phase 19 (ADR-042 §3.1): the observability collector is a required
	// dependency — New fails closed on a nil one, so the harness must supply it.
	collector := observability.NewCollector()
	var seq int
	srv, err := New(Config{
		Repo:          repo,
		Audit:         audit,
		Authenticator: authn,
		Collector:     collector,
		// Deterministic ids make the causal-chain assertions exact instead of
		// "two rows that happen to share whatever was generated".
		NewCorrelationID: func() string { seq++; return fmt.Sprintf("corr-%d", seq) },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &kit{t: t, repo: repo, audit: audit, ts: ts, srv: srv, collector: collector}
}

type call struct {
	method  string
	path    string
	token   string
	ifMatch string
	body    string
	noToken bool
	// headers carries extra request headers (e.g. Idempotency-Key for the
	// replay-guard tests).
	headers map[string]string
}

func (k *kit) do(c call) (*http.Response, string) {
	k.t.Helper()
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req, err := http.NewRequest(c.method, k.ts.URL+c.path, body)
	if err != nil {
		k.t.Fatalf("request: %v", err)
	}
	if !c.noToken {
		tok := c.token
		if tok == "" {
			tok = testToken
		}
		req.Header.Set(HeaderToken, tok)
	}
	if c.ifMatch != "" {
		req.Header.Set(HeaderIfMatch, c.ifMatch)
	}
	for h, v := range c.headers {
		req.Header.Set(h, v)
	}
	resp, err := k.ts.Client().Do(req)
	if err != nil {
		k.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func (k *kit) create(id string, rules string) (*http.Response, string) {
	return k.do(call{method: http.MethodPost, path: RoutePrefix + "policies",
		body: fmt.Sprintf(`{"policy_id":%q,"rules":[%s]}`, id, rules)})
}

const ruleA = `{"rule_id":"r1","priority":10,"kind":"change-freeze"}`
const ruleB = `{"rule_id":"r2","priority":5,"kind":"group-allow","param":"g1"}`

func decodePolicy(t *testing.T, raw string) PolicyResponse {
	t.Helper()
	var p PolicyResponse
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("decode policy %q: %v", raw, err)
	}
	return p
}

func decodeErr(t *testing.T, raw string) errorBody {
	t.Helper()
	var e errorEnvelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("decode error %q: %v", raw, err)
	}
	return e.Error
}

func wantStatus(t *testing.T, resp *http.Response, raw string, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, want, raw)
	}
}

func wantCode(t *testing.T, raw string, want string) {
	t.Helper()
	if got := decodeErr(t, raw).Code; got != want {
		t.Fatalf("error code = %q, want %q (body %s)", got, want, raw)
	}
}

// ---------------------------------------------------------------------------
// AuthN / AuthZ — fail-closed (ADR-036 §3.1, P17-2 HARD MUST)
// ---------------------------------------------------------------------------

func TestAuthNFailsClosed(t *testing.T) {
	k := newKit(t)

	cases := []struct {
		name string
		c    call
	}{
		{"no token", call{method: http.MethodPost, path: RoutePrefix + "policies", noToken: true, body: `{"policy_id":"p"}`}},
		{"wrong token", call{method: http.MethodPost, path: RoutePrefix + "policies", token: "nope", body: `{"policy_id":"p"}`}},
		{"empty token", call{method: http.MethodPost, path: RoutePrefix + "policies", token: "   ", body: `{"policy_id":"p"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := k.do(tc.c)
			wantStatus(t, resp, raw, http.StatusUnauthorized)
			wantCode(t, raw, codeUnauthenticated)
		})
	}

	// Nothing reached the store and nothing reached the audit log: a rejected
	// caller must not even produce an intent row.
	if rows := k.audit.snapshot(); len(rows) != 0 {
		t.Fatalf("unauthenticated requests produced %d audit rows, want 0", len(rows))
	}
	if all, _ := k.repo.List(); len(all) != 0 {
		t.Fatalf("unauthenticated requests created %d policies, want 0", len(all))
	}
}

// The gate wraps the WHOLE mux, so an unauthenticated caller cannot map the
// surface by probing for 404 vs 401.
func TestAuthNPrecedesRouting(t *testing.T) {
	k := newKit(t)
	resp, raw := k.do(call{method: http.MethodGet, path: RoutePrefix + "does-not-exist", noToken: true})
	wantStatus(t, resp, raw, http.StatusUnauthorized)

	// ... and an authenticated caller does get the honest 404 from the mux.
	resp2, raw2 := k.do(call{method: http.MethodGet, path: RoutePrefix + "does-not-exist"})
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("authenticated probe still 401 (body %s)", raw2)
	}
}

func TestAuthZFailsClosed(t *testing.T) {
	// Authenticated, but carrying no capability at all.
	k := newKitWith(t, stubAuthn{p: Principal{ID: "weak"}})
	resp, raw := k.create("p1", ruleA)
	wantStatus(t, resp, raw, http.StatusForbidden)
	wantCode(t, raw, codeForbidden)
	if rows := k.audit.snapshot(); len(rows) != 0 {
		t.Fatalf("forbidden request produced %d audit rows, want 0", len(rows))
	}
}

// MUST-P17-14: no authentication prerequisite ⇒ the surface must not be
// constructible, so a composition root cannot bind it by accident.
func TestStartupFailsClosedWithoutToken(t *testing.T) {
	if _, err := NewTokenAuthenticator("", ""); err != ErrAuthPrerequisiteMissing {
		t.Fatalf("empty token err = %v, want ErrAuthPrerequisiteMissing", err)
	}
	if _, err := NewTokenAuthenticator("   \t ", ""); err != ErrAuthPrerequisiteMissing {
		t.Fatalf("blank token err = %v, want ErrAuthPrerequisiteMissing", err)
	}
	repo, _ := governancepolicy.NewFileRepository(t.TempDir())
	if _, err := New(Config{Repo: repo, Audit: &fakeAudit{}}); err != ErrAuthPrerequisiteMissing {
		t.Fatalf("nil authenticator err = %v, want ErrAuthPrerequisiteMissing", err)
	}
	// There must be no way to get an authenticated-by-default server.
	if _, err := New(Config{Repo: repo, Audit: &fakeAudit{}, Authorizer: CapabilityAuthorizer{}}); err == nil {
		t.Fatal("a server was constructed with no Authenticator")
	}
}

// ---------------------------------------------------------------------------
// Create — CAS-1 and store-derived idempotency (ADR-036 §3.4)
// ---------------------------------------------------------------------------

func TestCreateLandsDraftRevisionOne(t *testing.T) {
	k := newKit(t)
	resp, raw := k.create("p1", ruleA+","+ruleB)
	wantStatus(t, resp, raw, http.StatusCreated)

	got := decodePolicy(t, raw)
	if got.Revision != 1 || got.Status != string(governancepolicy.StatusDraft) {
		t.Fatalf("created = %+v, want revision 1 draft", got)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(got.Rules))
	}
	if got.ActivatedAt != nil {
		t.Fatalf("activated_at = %v on a fresh draft, want omitted", got.ActivatedAt)
	}
	if e := resp.Header.Get("ETag"); e != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", e)
	}
	if resp.Header.Get(HeaderCorrelationID) == "" {
		t.Fatal("no correlation id header")
	}
}

func TestCreateReplayIsIdempotent(t *testing.T) {
	k := newKit(t)
	if resp, raw := k.create("p1", ruleA); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d (%s)", resp.StatusCode, raw)
	}

	resp, raw := k.create("p1", ruleA)
	wantStatus(t, resp, raw, http.StatusOK)
	if got := decodePolicy(t, raw); got.Revision != 1 {
		t.Fatalf("replay revision = %d, want the ORIGINAL 1 (a replay must not mint a revision)", got.Revision)
	}
	all, _ := k.repo.List()
	if len(all) != 1 {
		t.Fatalf("store holds %d policies after a replay, want 1", len(all))
	}
}

func TestCreateSameIDDifferentPayloadIsConflict(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)

	resp, raw := k.create("p1", ruleB)
	wantStatus(t, resp, raw, http.StatusConflict)
	wantCode(t, raw, codeConflict)

	// The stored policy is untouched — "idempotency" must never degrade into
	// overwrite-by-ID.
	rec, _, _ := k.repo.Get("p1")
	if len(rec.Rules) != 1 || rec.Rules[0].RuleID != "r1" || rec.Revision != 1 {
		t.Fatalf("stored record mutated by a refused create: %+v", rec)
	}
}

func TestCreateRejectsIfMatch(t *testing.T) {
	k := newKit(t)
	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies",
		ifMatch: "3", body: `{"policy_id":"p1","rules":[]}`})
	wantStatus(t, resp, raw, http.StatusUnprocessableEntity)
	wantCode(t, raw, codeInvalidRequest)

	// "0" states the same precondition CAS-1 already fixes, so it is accepted.
	resp2, raw2 := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies",
		ifMatch: "0", body: `{"policy_id":"p1","rules":[]}`})
	wantStatus(t, resp2, raw2, http.StatusCreated)
}

// ---------------------------------------------------------------------------
// Validation (ADR-036 §3.4) — structural only, Engine never consulted
// ---------------------------------------------------------------------------

func TestValidationRejects(t *testing.T) {
	k := newKit(t)
	cases := []struct{ name, body string }{
		{"empty policy id", `{"policy_id":"","rules":[]}`},
		{"blank policy id", `{"policy_id":"   ","rules":[]}`},
		{"unknown rule kind", `{"policy_id":"p","rules":[{"rule_id":"r","kind":"teleport"}]}`},
		{"empty rule id", `{"policy_id":"p","rules":[{"rule_id":"","kind":"change-freeze"}]}`},
		{"duplicate rule id", `{"policy_id":"p","rules":[{"rule_id":"r","kind":"change-freeze"},{"rule_id":"r","kind":"require-approval"}]}`},
		{"unknown field", `{"policy_id":"p","rules":[],"status":"active"}`},
		{"malformed json", `{"policy_id":`},
		{"two documents", `{"policy_id":"p","rules":[]}{"policy_id":"q","rules":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies", body: tc.body})
			wantStatus(t, resp, raw, http.StatusUnprocessableEntity)
			wantCode(t, raw, codeInvalidRequest)
		})
	}
	// Validation failures are rejected BEFORE the pipeline starts, so they
	// leave no intent row behind.
	if rows := k.audit.snapshot(); len(rows) != 0 {
		t.Fatalf("validation failures produced %d audit rows, want 0", len(rows))
	}
}

// A "status" field is not merely unknown — accepting it would let the caller
// take a lifecycle decision at the write boundary (P17-4). Belt and braces: the
// repository also owns Status on create.
func TestCreateCannotConjureActive(t *testing.T) {
	k := newKit(t)
	resp, raw := k.create("p1", ruleA)
	wantStatus(t, resp, raw, http.StatusCreated)
	if got := decodePolicy(t, raw); got.Status != string(governancepolicy.StatusDraft) {
		t.Fatalf("status = %q, want draft", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Update — If-Match, Draft-only (ADR-036 §3.4)
// ---------------------------------------------------------------------------

func TestUpdateRequiresIfMatch(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)

	for _, ifMatch := range []string{"", "*", "abc", "0", "-1"} {
		t.Run("if-match="+ifMatch, func(t *testing.T) {
			resp, raw := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1",
				ifMatch: ifMatch, body: `{"rules":[` + ruleB + `]}`})
			wantStatus(t, resp, raw, http.StatusUnprocessableEntity)
			wantCode(t, raw, codeInvalidRequest)
		})
	}
}

func TestUpdateHappyPathAndStaleConflict(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)

	resp, raw := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1",
		ifMatch: `"1"`, body: `{"policy_id":"p1","rules":[` + ruleB + `]}`})
	wantStatus(t, resp, raw, http.StatusOK)
	if got := decodePolicy(t, raw); got.Revision != 2 || got.Rules[0].RuleID != "r2" {
		t.Fatalf("updated = %+v, want revision 2 carrying r2", got)
	}

	// The caller now holds a stale token.
	resp2, raw2 := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1",
		ifMatch: "1", body: `{"rules":[` + ruleA + `]}`})
	wantStatus(t, resp2, raw2, http.StatusConflict)
	wantCode(t, raw2, codeConflict)
}

func TestUpdateRejectsBodyPathIDMismatch(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	resp, raw := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1",
		ifMatch: "1", body: `{"policy_id":"other","rules":[]}`})
	wantStatus(t, resp, raw, http.StatusUnprocessableEntity)
}

func TestUpdateUnknownPolicyIsNotFound(t *testing.T) {
	k := newKit(t)
	resp, raw := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/ghost",
		ifMatch: "1", body: `{"rules":[]}`})
	wantStatus(t, resp, raw, http.StatusNotFound)
	wantCode(t, raw, codeNotFound)
}

// Update is Draft-only: an Active target is a validation failure, because no
// revision the caller could supply would make the write legal (ADR-036 §3.4).
func TestUpdateOnActiveIsRejected(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})

	resp, raw := k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1",
		ifMatch: "2", body: `{"rules":[` + ruleB + `]}`})
	wantStatus(t, resp, raw, http.StatusUnprocessableEntity)
	wantCode(t, raw, codeIllegal)
}

// ---------------------------------------------------------------------------
// Lifecycle — CompareAndTransition, CT-8, CT-9 (ADR-036 §3.2.2, §4)
// ---------------------------------------------------------------------------

func TestLifecycleChain(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)

	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})
	wantStatus(t, resp, raw, http.StatusOK)
	if got := decodePolicy(t, raw); got.Revision != 2 || got.Status != "active" || got.ActivatedAt == nil {
		t.Fatalf("activated = %+v, want revision 2 active with a timestamp", got)
	}

	resp, raw = k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/deactivate", ifMatch: "2"})
	wantStatus(t, resp, raw, http.StatusOK)
	if got := decodePolicy(t, raw); got.Revision != 3 || got.Status != "draft" {
		t.Fatalf("deactivated = %+v, want revision 3 draft", got)
	}

	resp, raw = k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/archive", ifMatch: "3"})
	wantStatus(t, resp, raw, http.StatusOK)
	if got := decodePolicy(t, raw); got.Revision != 4 || got.Status != "archived" {
		t.Fatalf("archived = %+v, want revision 4 archived", got)
	}
}

// CT-8: a self-transition is a revision-checked no-op. If it bumped, every
// retry would mint a revision and invalidate the caller's own If-Match token —
// the opposite of idempotent.
func TestLifecycleSelfTransitionIsNoOp(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})

	for i := 0; i < 3; i++ {
		resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "2"})
		wantStatus(t, resp, raw, http.StatusOK)
		if got := decodePolicy(t, raw); got.Revision != 2 || got.Status != "active" {
			t.Fatalf("replay %d = %+v, want an unchanged revision 2", i, got)
		}
	}
}

func TestLifecycleIllegalTransitionIsConflict(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/archive", ifMatch: "1"})

	// Archived is terminal.
	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "2"})
	wantStatus(t, resp, raw, http.StatusConflict)
	wantCode(t, raw, codeIllegal)
}

// CT-9: the revision is compared FIRST. A request that is both stale AND
// illegal must be reported as a conflict, because refetching is the action that
// would actually help the caller.
func TestLifecycleRevisionOutranksLegality(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/archive", ifMatch: "1"})

	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})
	wantStatus(t, resp, raw, http.StatusConflict)
	wantCode(t, raw, codeConflict) // NOT illegal_transition
}

func TestLifecycleUnknownPolicyIsNotFound(t *testing.T) {
	k := newKit(t)
	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/ghost/activate", ifMatch: "1"})
	wantStatus(t, resp, raw, http.StatusNotFound)
	wantCode(t, raw, codeNotFound)
}

// ---------------------------------------------------------------------------
// Audit — MUST-P17-13 and the frozen vocabulary (ADR-036 §3.3)
// ---------------------------------------------------------------------------

func TestAuditIntentThenOutcomeIsCausallyLinked(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})

	rows := k.audit.snapshot()
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (two intent/outcome pairs)", len(rows))
	}

	// Pair 1 — create: expected 0 → committed 1.
	if rows[0].Result != resultIntent || rows[0].Revision != 0 || rows[0].Action != ActionCreate {
		t.Fatalf("create intent = %+v", rows[0])
	}
	if rows[1].Result != resultSuccess || rows[1].Revision != 1 || rows[1].Action != ActionCreate {
		t.Fatalf("create outcome = %+v", rows[1])
	}
	if rows[0].CorrelationID == "" || rows[0].CorrelationID != rows[1].CorrelationID {
		t.Fatalf("create pair not correlated: %q vs %q", rows[0].CorrelationID, rows[1].CorrelationID)
	}

	// Pair 2 — activate: expected 1 → committed 2, and a DIFFERENT correlation.
	if rows[2].Revision != 1 || rows[3].Revision != 2 || rows[3].Action != ActionActivate {
		t.Fatalf("activate pair = %+v / %+v", rows[2], rows[3])
	}
	if rows[2].CorrelationID == rows[0].CorrelationID {
		t.Fatal("two separate requests share a correlation id")
	}

	for i, e := range rows {
		if e.Target != "p1" {
			t.Fatalf("row %d target = %q, want the PolicyID", i, e.Target)
		}
		if e.Actor != "op-1" {
			t.Fatalf("row %d actor = %q, want the authenticated principal", i, e.Actor)
		}
		if e.Operation != auditOperation {
			t.Fatalf("row %d operation = %q", i, e.Operation)
		}
		if e.ExecutionID != "" {
			t.Fatalf("row %d carries an ExecutionID (%q); it must never be cross-populated", i, e.ExecutionID)
		}
	}
}

// The frozen policy.* vocabulary: a policy mutation must never be recorded as
// an execution (P17-10).
func TestAuditActionVocabulary(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPut, path: RoutePrefix + "policies/p1", ifMatch: "1", body: `{"rules":[` + ruleB + `]}`})
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "2"})
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/deactivate", ifMatch: "3"})
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/archive", ifMatch: "4"})

	admitted := map[string]bool{
		ActionCreate: true, ActionUpdate: true, ActionActivate: true,
		ActionDeactivate: true, ActionArchive: true,
	}
	seen := map[string]bool{}
	for _, e := range k.audit.snapshot() {
		if !admitted[e.Action] {
			t.Fatalf("audit action %q is outside the frozen policy.* vocabulary", e.Action)
		}
		if !strings.HasPrefix(e.Action, "policy.") {
			t.Fatalf("audit action %q is not namespaced", e.Action)
		}
		seen[e.Action] = true
		switch e.Result {
		case resultIntent, resultSuccess, resultFailure:
		default:
			t.Fatalf("audit result %q is outside the frozen set", e.Result)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("exercised %d of 5 mutations: %v", len(seen), seen)
	}
}

// MUST-P17-13, first clause: no durable intent ⇒ the mutation is FORBIDDEN.
func TestAuditIntentFailureBlocksMutation(t *testing.T) {
	k := newKit(t)
	k.audit.failOn = func(e storage.AuditEvent) error {
		if e.Result == resultIntent {
			return fmt.Errorf("audit store down")
		}
		return nil
	}

	resp, raw := k.create("p1", ruleA)
	wantStatus(t, resp, raw, http.StatusServiceUnavailable)
	wantCode(t, raw, codeAuditUnavailable)

	if all, _ := k.repo.List(); len(all) != 0 {
		t.Fatalf("mutation was attempted despite a failed intent: %d policies stored", len(all))
	}
	if rows := k.audit.snapshot(); len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 (the intent write itself failed)", len(rows))
	}
}

// The permitted degraded state: intent durable, mutation applied, outcome
// write failed. It must be reported, not swallowed — and NOT described as a
// rollback, because nothing is rolled back.
func TestAuditOutcomeFailureIsReportedAsDegraded(t *testing.T) {
	k := newKit(t)
	k.audit.failOn = func(e storage.AuditEvent) error {
		if e.Result == resultSuccess {
			return fmt.Errorf("audit store down")
		}
		return nil
	}

	resp, raw := k.create("p1", ruleA)
	wantStatus(t, resp, raw, http.StatusInternalServerError)
	wantCode(t, raw, codeAuditUnrecorded)
	if !strings.Contains(decodeErr(t, raw).Message, "WAS applied") {
		t.Fatalf("degraded message hides that the mutation landed: %q", decodeErr(t, raw).Message)
	}
	if resp.Header.Get(HeaderCorrelationID) == "" {
		t.Fatal("degraded response carries no correlation id to reconcile with")
	}

	// The mutation stands.
	if _, ok, _ := k.repo.Get("p1"); !ok {
		t.Fatal("the policy is absent; the response claimed the mutation was applied")
	}
	// And the intent row is the dangling evidence.
	rows := k.audit.snapshot()
	if len(rows) != 1 || rows[0].Result != resultIntent {
		t.Fatalf("rows = %+v, want exactly one dangling intent", rows)
	}
}

// A refused CAS records a FAILURE carrying the actually stored revision — not a
// separate "conflict" result class (frozen table, ADR-036 §3.3.2.1).
func TestAuditConflictRecordsActualRevision(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/activate", ifMatch: "1"})

	// Stale token: the store is at revision 2.
	resp, raw := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies/p1/archive", ifMatch: "1"})
	wantStatus(t, resp, raw, http.StatusConflict)

	rows := k.audit.snapshot()
	last := rows[len(rows)-1]
	if last.Result != resultFailure {
		t.Fatalf("conflict recorded as %q, want %q", last.Result, resultFailure)
	}
	if last.Revision != 2 {
		t.Fatalf("conflict revision = %d, want the ACTUAL stored 2", last.Revision)
	}
	if !strings.Contains(last.Detail, "expected_revision=1") || !strings.Contains(last.Detail, "actual_revision=2") {
		t.Fatalf("conflict detail = %q, want both the expected and the actual revision", last.Detail)
	}
	intent := rows[len(rows)-2]
	if intent.Result != resultIntent || intent.Revision != 1 {
		t.Fatalf("intent = %+v, want the EXPECTED revision 1", intent)
	}
	if intent.CorrelationID != last.CorrelationID {
		t.Fatal("the failed pair is not correlated")
	}
}

// An idempotent replay is a successful REQUEST with no state change. The chain
// says so explicitly rather than claiming a mutation occurred.
func TestAuditReplayIsHonest(t *testing.T) {
	k := newKit(t)
	k.create("p1", ruleA)
	k.create("p1", ruleA)

	rows := k.audit.snapshot()
	last := rows[len(rows)-1]
	if last.Result != resultSuccess || last.Revision != 1 {
		t.Fatalf("replay outcome = %+v, want success at the unchanged revision 1", last)
	}
	if !strings.Contains(last.Detail, "no-mutation") {
		t.Fatalf("replay detail = %q, want it to state that nothing was mutated", last.Detail)
	}
}
