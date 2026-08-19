package management

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// Request headers of the Management surface (ADR-036 §3.1 / §3.2).
const (
	// HeaderToken carries the shared-secret bearer token (ADR-036 §3.1).
	HeaderToken = "X-Management-Token"
	// HeaderIfMatch carries the expected policy revision. It is the caller's
	// entire concurrency contract: the handler never compares revisions itself
	// (that would be the Get -> check -> Save pattern MUST-P17-12 forbids), it
	// only forwards the expectation into the repository CAS.
	HeaderIfMatch = "If-Match"
	// HeaderCorrelationID is a RESPONSE header carrying the server-generated
	// correlation id of the request, so an operator can locate the intent and
	// outcome audit rows for exactly this attempt.
	//
	// It is deliberately not honoured as a REQUEST header: accepting a
	// caller-supplied correlation id would let one caller collide with, or
	// forge, another caller's audit chain — and that chain is the evidence
	// MUST-P17-13 exists to produce.
	HeaderCorrelationID = "X-Correlation-Id"
	// HeaderIdempotencyKey MAY be supplied by the caller so the replay guard
	// (MUST-17.3-A, ADR-038 §3.4) can refuse a second mutation under an
	// already-terminal correlation id. If absent, the server generates a fresh
	// id and no replay protection applies. It is honoured as a REQUEST header
	// here only; the response still carries the (possibly generated) id in
	// HeaderCorrelationID.
	HeaderIdempotencyKey = "Idempotency-Key"
)

// CapabilityPolicyManage is the single capability the Management surface
// requires (ADR-036 §3.1). Phase 17 has exactly one writing principal; a
// capability MATRIX would be architecture paid for an imagined future (§4.5.1).
const CapabilityPolicyManage = "policy:manage"

// Errors of the auth seam. They are transport-independent: the HTTP layer maps
// them to 401/403, and a future non-HTTP caller would map them differently.
var (
	// ErrUnauthenticated: no token, or a token that does not match.
	ErrUnauthenticated = errors.New("management: unauthenticated")
	// ErrForbidden: authenticated, but the principal lacks the capability.
	ErrForbidden = errors.New("management: forbidden")
	// ErrAuthPrerequisiteMissing is the STARTUP fail-closed signal
	// (MUST-P17-14). It is returned by the constructors when the
	// authentication prerequisite is absent, so the composition root can
	// decline to bind the surface at all. A registered-but-always-401 write
	// port still advertises a writable surface; not binding is strictly
	// stronger, and this error is how that strength is delivered upward.
	ErrAuthPrerequisiteMissing = errors.New("management: authentication prerequisite missing")
)

// Principal is the authenticated management caller. It is an AUTHN ARTIFACT,
// not a domain entity: it carries identity and capabilities and nothing the
// policy domain could read.
type Principal struct {
	// ID is the audit Actor for every row this principal produces.
	ID string
	// Capabilities is the closed set granted to this principal.
	Capabilities []string
}

// Has reports whether the principal holds capability. The zero Principal holds
// nothing, which makes "forgot to authenticate" fail closed by construction
// rather than by a check somebody might omit.
func (p Principal) Has(capability string) bool {
	for _, c := range p.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Authenticator turns a request into a Principal. It is an interface so that
// mTLS / OIDC can REPLACE the shared secret in a later phase without reshaping
// the pipeline (§4.5.1: the decision is replaceable, not extensible).
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// Authorizer decides whether a Principal may perform a capability.
type Authorizer interface {
	Authorize(p Principal, capability string) error
}

// TokenAuthenticator is the Phase 17 shared-secret authenticator. The secret is
// supplied by the deployment (OPCORE_MANAGEMENT_TOKEN) and never compiled in.
type TokenAuthenticator struct {
	token       []byte
	principalID string
}

// NewTokenAuthenticator fails closed on an empty/blank token
// (ErrAuthPrerequisiteMissing). There is deliberately no "no-auth" constructor
// in this package: an open management authenticator is not a configuration this
// codebase can express.
func NewTokenAuthenticator(token, principalID string) (*TokenAuthenticator, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return nil, ErrAuthPrerequisiteMissing
	}
	if principalID == "" {
		principalID = "management"
	}
	return &TokenAuthenticator{token: []byte(t), principalID: principalID}, nil
}

// Authenticate compares the presented token in constant time.
//
// subtle.ConstantTimeCompare short-circuits on differing lengths, so token
// LENGTH is not hidden — only its content is. That is the accepted property: a
// length oracle on a deployment-generated secret is not a practical attack,
// whereas a byte-by-byte early return would be.
func (a *TokenAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	presented := strings.TrimSpace(r.Header.Get(HeaderToken))
	if presented == "" {
		return Principal{}, ErrUnauthenticated
	}
	if subtle.ConstantTimeCompare([]byte(presented), a.token) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{ID: a.principalID, Capabilities: []string{CapabilityPolicyManage}}, nil
}

// CapabilityAuthorizer is default-deny: it grants only what the Principal
// explicitly carries.
type CapabilityAuthorizer struct{}

// Authorize implements Authorizer.
func (CapabilityAuthorizer) Authorize(p Principal, capability string) error {
	if !p.Has(capability) {
		return ErrForbidden
	}
	return nil
}

// principalKey is the unexported context key for the authenticated principal.
// Unexported so no other package can inject a Principal into a context and
// bypass the authenticator.
type principalKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the authenticated principal. A missing principal yields
// the zero value, which holds no capabilities — so a routing mistake that
// reached a handler without the auth middleware degrades to "no rights", not to
// "unchecked".
func principalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}
