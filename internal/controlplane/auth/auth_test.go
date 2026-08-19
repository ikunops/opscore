package auth

import (
	"errors"
	"testing"

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
