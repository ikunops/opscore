package server

// P1-2 (quality-baseline 2026-07-22): refresh tokens could not be revoked.
// The floor design (ADR-082): POST /api/auth/revoke records a per-user
// revocation floor in the config store; /api/auth/refresh rejects any refresh
// token whose `iat` is at or before the floor. This test drives the REAL HTTP
// surface and pins:
//
//   - the revoke endpoint requires a valid access token (a leaked REFRESH
//     token must not be able to revoke — that would let an attacker lock the
//     real user out);
//   - after the revoke, the leaked/pre-floor refresh token is rejected;
//   - a fresh login after crossing the second boundary works, and its new
//     refresh token rotates (the floor only kills pre-floor tokens);
//   - the revoke does not disturb the access token (it expires on its own TTL).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestP1_2RevokeRefreshInvalidatesPreFloorTokens(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()

	// Login: access + refresh.
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", rec.Code, rec.Body.String())
	}
	var tp tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}

	// Sanity: the fresh refresh token rotates before the revoke.
	rec = doJSON(t, h, "POST", "/api/auth/refresh", "", map[string]string{"refresh_token": tp.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-revoke refresh status = %d body=%s", rec.Code, rec.Body.String())
	}

	// Revoke WITHOUT a valid access token must be refused (a leaked refresh
	// token must not be able to revoke — that would let an attacker lock the
	// real user out).
	rec = doJSON(t, h, "POST", "/api/auth/revoke", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("P1-2: an unauthenticated revoke must be refused, got %d", rec.Code)
	}

	// The real user revokes with their valid access token.
	rec = doJSON(t, h, "POST", "/api/auth/revoke", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("P1-2: revoke status = %d body=%s", rec.Code, rec.Body.String())
	}

	// The pre-floor refresh token is dead.
	rec = doJSON(t, h, "POST", "/api/auth/refresh", "", map[string]string{"refresh_token": tp.RefreshToken})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("P1-2: a pre-floor refresh token must be rejected, got %d", rec.Code)
	}

	// The access token is UNAFFECTED (it expires on its own 15-minute TTL —
	// the revocation targets the long-lived credential, ADR-082 §2).
	rec = doJSON(t, h, "GET", "/api/inventory", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("P1-2: the access token must be unaffected by the refresh floor, got %d", rec.Code)
	}

	// A fresh login after crossing the second boundary works, and its NEW
	// refresh token rotates (the floor only kills tokens issued before it).
	time.Sleep(1100 * time.Millisecond)
	rec = doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	if rec.Code != http.StatusOK {
		t.Fatalf("post-revoke login status = %d", rec.Code)
	}
	var tp2 tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp2); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}
	rec = doJSON(t, h, "POST", "/api/auth/refresh", "", map[string]string{"refresh_token": tp2.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("P1-2: a post-revoke refresh token must rotate, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// P54 (ADR-087/088): revocation granularity — the admin face and per-token
// (jti) revocation. T396~T398 drive the real HTTP surface.
// ---------------------------------------------------------------------------

// loginAs logs a user in and returns the token pair.
func loginAs(t *testing.T, h http.Handler, user, pass string) tokenPair {
	t.Helper()
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{user, pass})
	if rec.Code != http.StatusOK {
		t.Fatalf("login(%s) status = %d body=%s", user, rec.Code, rec.Body.String())
	}
	var tp tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}
	return tp
}

// refreshStatus returns the HTTP status of rotating the given refresh token.
func refreshStatus(t *testing.T, h http.Handler, refresh string) int {
	t.Helper()
	return doJSON(t, h, "POST", "/api/auth/refresh", "", map[string]string{"refresh_token": refresh}).Code
}

// T396 (I1): the body-less revoke keeps its exact meaning — the caller's own
// floor. (The legacy shape is exercised by TestP1_2...; this pins the empty
// JSON object variant so the new body parsing cannot silently change it.)
func TestT396RevokeNoBodyStillSelfFloor(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	tp := loginAs(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/auth/revoke", tp.AccessToken, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("T396: empty-body revoke status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := refreshStatus(t, h, tp.RefreshToken); got != http.StatusUnauthorized {
		t.Fatalf("T396: the caller's own floor must still die, refresh got %d", got)
	}
}

// T397 (I4/I5): presenting a refresh token revokes exactly THAT token — the
// user's other session keeps rotating (the "log out the other sessions"
// semantics the floor cannot express). It must NOT set a floor.
func TestT397PerTokenRevokeIsGranular(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	sess1 := loginAs(t, h, "admin", "adminpw")
	sess2 := loginAs(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "POST", "/api/auth/revoke", sess1.AccessToken, map[string]string{"refresh_token": sess1.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("T397: per-token revoke status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := refreshStatus(t, h, sess1.RefreshToken); got != http.StatusUnauthorized {
		t.Fatalf("T397: the revoked token must die, refresh got %d", got)
	}
	// The other session is untouched — and this also proves NO floor was set
	// (a floor would have killed this token too).
	if got := refreshStatus(t, h, sess2.RefreshToken); got != http.StatusOK {
		t.Fatalf("T397: the other session must keep rotating, refresh got %d", got)
	}
}

// T398 (I2/I3): the admin boundary. Authorization strictly precedes any state
// change: a refused revoke must leave the target's tokens USABLE.
func TestT398RevokeAdminBoundary(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	if rec := doJSON(t, h, "POST", "/api/auth/register", "", credentials{"bob", "bobpw"}); rec.Code != http.StatusCreated {
		t.Fatalf("register bob: %d %s", rec.Code, rec.Body.String())
	}
	admin := loginAs(t, h, "admin", "adminpw")
	bob := loginAs(t, h, "bob", "bobpw")

	// Non-admin bob may not revoke the admin's tokens by presenting them.
	rec := doJSON(t, h, "POST", "/api/auth/revoke", bob.AccessToken, map[string]string{"refresh_token": admin.RefreshToken})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("T398: non-admin per-token revoke of another user must be 403, got %d", rec.Code)
	}
	// Refused means REFUSED: the admin's token must still rotate.
	if got := refreshStatus(t, h, admin.RefreshToken); got != http.StatusOK {
		t.Fatalf("T398: a refused revoke must not touch the target, refresh got %d", got)
	}

	// Non-admin bob may not revoke the admin's floor either.
	rec = doJSON(t, h, "POST", "/api/auth/revoke", bob.AccessToken, map[string]string{"username": "admin"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("T398: non-admin floor revoke of another user must be 403, got %d", rec.Code)
	}
	if got := refreshStatus(t, h, admin.RefreshToken); got != http.StatusOK {
		t.Fatalf("T398: the admin's tokens must survive the refused floor revoke, refresh got %d", got)
	}

	// Ambiguous intent is refused, never guessed.
	rec = doJSON(t, h, "POST", "/api/auth/revoke", admin.AccessToken,
		map[string]string{"username": "bob", "refresh_token": bob.RefreshToken})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("T398: both fields must be 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := refreshStatus(t, h, bob.RefreshToken); got != http.StatusOK {
		t.Fatalf("T398: the ambiguous request must not have revoked anything, refresh got %d", got)
	}

	// A garbage refresh token is a bad request, not a 500 and not a revocation.
	rec = doJSON(t, h, "POST", "/api/auth/revoke", admin.AccessToken, map[string]string{"refresh_token": "not-a-token"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("T398: malformed refresh token must be 400, got %d", rec.Code)
	}

	// The admin DOES have both powers.
	rec = doJSON(t, h, "POST", "/api/auth/revoke", admin.AccessToken, map[string]string{"refresh_token": bob.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("T398: admin per-token revoke must be 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := refreshStatus(t, h, bob.RefreshToken); got != http.StatusUnauthorized {
		t.Fatalf("T398: the admin-revoked token must die, refresh got %d", got)
	}
	bob2 := loginAs(t, h, "bob", "bobpw")
	rec = doJSON(t, h, "POST", "/api/auth/revoke", admin.AccessToken, map[string]string{"username": "bob"})
	if rec.Code != http.StatusOK {
		t.Fatalf("T398: admin floor revoke must be 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := refreshStatus(t, h, bob2.RefreshToken); got != http.StatusUnauthorized {
		t.Fatalf("T398: the admin floor revoke must kill bob's tokens, refresh got %d", got)
	}
}
