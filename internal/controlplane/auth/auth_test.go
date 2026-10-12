package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

func newTestStore(t *testing.T) storage.Storage {
	t.Helper()
	return storage.NewMemoryStorage()
}

// buildGrant wires a role -> operation grant and assigns it to a user, returning
// the storage handle ready for RBAC assertions.
func buildGrant(t *testing.T, stor storage.Storage, opName, roleName, username string) {
	t.Helper()
	op, err := stor.Operations().Save(storage.Operation{Name: opName, Enabled: true})
	if err != nil {
		t.Fatalf("save op: %v", err)
	}
	role, err := stor.Roles().Save(storage.Role{Name: roleName})
	if err != nil {
		t.Fatalf("save role: %v", err)
	}
	if err := stor.Roles().AddOperation(role.ID, op.ID); err != nil {
		t.Fatalf("add op to role: %v", err)
	}
	u, err := stor.Users().Save(storage.User{Username: username})
	if err != nil {
		t.Fatalf("save user: %v", err)
	}
	if err := stor.Users().AddRole(u.ID, role.ID); err != nil {
		t.Fatalf("add role to user: %v", err)
	}
}

func TestPassword(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(hash, "s3cret") {
		t.Fatal("expected verify true for correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("expected verify false for wrong password")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	secret := "test-access-secret"
	tok, err := SignToken(NewClaims("alice", 900), secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	claims, err := ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Sub != "alice" {
		t.Fatalf("sub = %q, want alice", claims.Sub)
	}
	// wrong secret must fail
	if _, err := ParseToken(tok, "other"); err == nil {
		t.Fatal("expected signature error with wrong secret")
	}
}

func TestRBAC_Can(t *testing.T) {
	stor := newTestStore(t)
	buildGrant(t, stor, "system.service.restart", "ops", "alice")

	ok, err := Can(stor, "alice", "system.service.restart")
	if err != nil {
		t.Fatalf("can: %v", err)
	}
	if !ok {
		t.Fatal("alice should be authorized")
	}

	// user with no roles
	if _, err := stor.Users().Save(storage.User{Username: "bob"}); err != nil {
		t.Fatalf("save bob: %v", err)
	}
	ok, err = Can(stor, "bob", "system.service.restart")
	if err != nil {
		t.Fatalf("can bob: %v", err)
	}
	if ok {
		t.Fatal("bob should NOT be authorized (no roles)")
	}

	// unknown operation
	ok, err = Can(stor, "alice", "system.unknown")
	if err != nil {
		t.Fatalf("can unknown: %v", err)
	}
	if ok {
		t.Fatal("alice should NOT be authorized for unknown op")
	}

	// unknown user fails closed
	ok, err = Can(stor, "ghost", "system.service.restart")
	if err != nil {
		t.Fatalf("can ghost: %v", err)
	}
	if ok {
		t.Fatal("ghost should NOT be authorized")
	}
}

func TestAuthorize(t *testing.T) {
	stor := newTestStore(t)
	buildGrant(t, stor, "system.service.restart", "ops", "alice")

	if err := Authorize(stor, "alice", "system.service.restart"); err != nil {
		t.Fatalf("alice authorize: %v", err)
	}
	if err := Authorize(stor, "alice", "nope.op"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("want ErrOperationNotFound, got %v", err)
	}
	if err := Authorize(stor, "bob", "system.service.restart"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestAuthService_RegisterLoginRefresh(t *testing.T) {
	stor := newTestStore(t)
	svc := NewAuthService(stor, "access-secret", "refresh-secret")

	if _, err := svc.Register("alice", "pw123"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// duplicate
	if _, err := svc.Register("alice", "pw123"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("want ErrUserExists, got %v", err)
	}
	// wrong password
	if _, _, _, err := svc.Login("alice", "bad"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
	// unknown user
	if _, _, _, err := svc.Login("ghost", "pw123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials for unknown, got %v", err)
	}

	access, refresh, user, err := svc.Login("alice", "pw123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user.Username != "alice" {
		t.Fatalf("username = %q", user.Username)
	}
	sub, err := svc.Authenticate(access)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if sub != "alice" {
		t.Fatalf("authenticate sub = %q", sub)
	}

	// refresh rotates tokens
	newAccess, newRefresh, err := svc.Refresh(refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if newAccess == access || newRefresh == refresh {
		t.Fatal("refresh should issue new tokens")
	}
	if _, err := svc.Authenticate(newAccess); err != nil {
		t.Fatalf("new access authenticate: %v", err)
	}
	// old access token still valid (not revoked in Phase 1.4; stateless JWT)
	if _, err := svc.Authenticate(access); err != nil {
		t.Fatalf("old access should still validate: %v", err)
	}
}

// P1-2 (quality-baseline 2026-07-22): refresh tokens could not be revoked.
// The floor design: RevokeRefresh records a revocation floor per user; Refresh
// rejects any refresh token whose `iat` precedes the floor. The assertions pin:
// the leaked (pre-floor) token dies; a fresh login after the revoke works (its
// iat is after the floor); the floor does not touch access tokens (they expire
// on their own TTL); a user with no floor is unaffected.
func TestAuthService_RefreshRevocationFloor(t *testing.T) {
	srv := NewAuthService(newTestStore(t), "access-secret", "refresh-secret")
	if _, err := srv.Register("alice", "pw-alice"); err != nil {
		t.Fatalf("register: %v", err)
	}
	access, refresh, _, err := srv.Login("alice", "pw-alice")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Sanity: the fresh refresh token rotates fine before the revoke.
	if _, _, err := srv.Refresh(refresh); err != nil {
		t.Fatalf("pre-revoke refresh: %v", err)
	}

	if err := srv.RevokeRefresh("alice"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// The pre-floor refresh token must die with ErrTokenRevoked.
	if _, _, err := srv.Refresh(refresh); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("P1-2: a pre-floor refresh token must be rejected with ErrTokenRevoked, got %v", err)
	}

	// A fresh login after the revoke works (its iat is in a LATER second than
	// the floor — the comparison is `<=` at second granularity, so the test
	// crosses the second boundary before re-logging-in). Its refresh token can
	// be rotated, then dies after ANOTHER revoke (the floor moved forward).
	time.Sleep(1100 * time.Millisecond)
	_, refresh2, _, err := srv.Login("alice", "pw-alice")
	if err != nil {
		t.Fatalf("post-revoke login: %v", err)
	}
	if _, _, err := srv.Refresh(refresh2); err != nil {
		t.Fatalf("post-revoke refresh2: %v", err)
	}
	if err := srv.RevokeRefresh("alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.Refresh(refresh2); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("P1-2: the second revoke must kill refresh2 too, got %v", err)
	}

	// A fresh login after the SECOND revoke works again (the floor is per-user
	// and only kills tokens issued before it) — again crossing the second
	// boundary first.
	time.Sleep(1100 * time.Millisecond)
	if _, _, _, err := srv.Login("alice", "pw-alice"); err != nil {
		t.Fatalf("login after the second revoke: %v", err)
	}

	// The floor does not touch ACCESS tokens: the pre-revoke access token still
	// authenticates (it expires on its own TTL) — the revocation targets the
	// long-lived credential, which is what P1-2 describes.
	if _, err := srv.Authenticate(access); err != nil {
		t.Fatalf("P1-2: the access token must be unaffected by the refresh floor: %v", err)
	}

	// An unknown user revokes to a harmless no-op floor (nothing to reject).
	if err := srv.RevokeRefresh("ghost"); err != nil {
		t.Fatalf("revoke for an unknown user must be a no-op, got %v", err)
	}
}

// T394 (ADR-087/088): the jti denylist is PER TOKEN — the denied token dies,
// the same user's other session and other users keep rotating, and the floor
// stays an independent mechanism.
func TestP54T394JtiDenylistIsPerToken(t *testing.T) {
	srv := NewAuthService(newTestStore(t), "access-secret", "refresh-secret")
	if _, err := srv.Register("alice", "pw-alice"); err != nil {
		t.Fatalf("register alice: %v", err)
	}
	if _, err := srv.Register("bob", "pw-bob"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	_, refA1, _, err := srv.Login("alice", "pw-alice")
	if err != nil {
		t.Fatalf("login alice #1: %v", err)
	}
	_, refA2, _, err := srv.Login("alice", "pw-alice")
	if err != nil {
		t.Fatalf("login alice #2: %v", err)
	}
	_, refB, _, err := srv.Login("bob", "pw-bob")
	if err != nil {
		t.Fatalf("login bob: %v", err)
	}

	claims, err := srv.ParseRefreshClaims(refA1)
	if err != nil {
		t.Fatalf("parse refresh claims: %v", err)
	}
	if err := srv.DenyRefreshToken(claims.Jti, claims.Exp); err != nil {
		t.Fatalf("deny: %v", err)
	}

	if _, _, err := srv.Refresh(refA1); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("T394: the denied token must be revoked, got %v", err)
	}
	if _, _, err := srv.Refresh(refA2); err != nil {
		t.Fatalf("T394: the user's OTHER session must keep rotating, got %v", err)
	}
	if _, _, err := srv.Refresh(refB); err != nil {
		t.Fatalf("T394: another user must be unaffected, got %v", err)
	}

	// The floor remains an independent mechanism (and independent per user).
	if err := srv.RevokeRefresh("bob"); err != nil {
		t.Fatalf("floor revoke: %v", err)
	}
	if _, _, err := srv.Refresh(refB); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("T394: the floor must still revoke, got %v", err)
	}
	if _, _, err := srv.Refresh(refA2); err != nil {
		t.Fatalf("T394: bob's floor must not touch alice, got %v", err)
	}

	// An empty jti can never be minted, so denying one is refused outright.
	if err := srv.DenyRefreshToken("", 0); err == nil {
		t.Fatal("T394: empty jti must be refused")
	}
}

// T395 (ADR-087/088): rotation + per-token revoke = "log out that session":
// denying the OLD token's jti leaves the rotated successor alive.
func TestP54T395RevokingOldJtiKeepsRotatedSuccessor(t *testing.T) {
	srv := NewAuthService(newTestStore(t), "access-secret", "refresh-secret")
	if _, err := srv.Register("alice", "pw-alice"); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, oldRef, _, err := srv.Login("alice", "pw-alice")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := srv.ParseRefreshClaims(oldRef)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, newRef, err := srv.Refresh(oldRef)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := srv.DenyRefreshToken(claims.Jti, claims.Exp); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if _, _, err := srv.Refresh(oldRef); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("T395: the old token must die, got %v", err)
	}
	if _, _, err := srv.Refresh(newRef); err != nil {
		t.Fatalf("T395: the rotated successor must keep rotating, got %v", err)
	}
}
