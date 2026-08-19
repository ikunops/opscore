package external

import "context"

// Tenant is the authn artifact returned by Authenticate. It is NOT a domain entity (MUST-2): it only
// carries the caller's tenancy scope. v1 is single-tenant, so the ID is constant.
type Tenant struct {
	ID string `json:"id"`
}

// Authenticator is the Authn/Tenant seam (MUST-0 / MUST-5). The v1 implementation is a single-tenant
// / no-auth stub; the interface exists so v2 can plug real authn without reshaping the contract
// (ADR-024 §3).
type Authenticator interface {
	Authenticate(ctx context.Context) (Tenant, error)
}

// NoAuthAuthenticator is the v1 single-tenant / no-auth stub (MUST-5 Authn seam). It always succeeds
// and returns the constant single-tenant tenant. Real multi-tenant authz is deferred — it is a
// distinct Major Evolution per ADR-021, not part of Phase 11.1.
type NoAuthAuthenticator struct{}

// Authenticate implements Authenticator. It performs no checks in v1 (the seam is declared, not
// exercised).
func (NoAuthAuthenticator) Authenticate(_ context.Context) (Tenant, error) {
	return Tenant{ID: "single-tenant"}, nil
}
