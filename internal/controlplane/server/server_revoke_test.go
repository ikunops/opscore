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
