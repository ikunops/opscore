package server

// P1-3 (quality-baseline 2026-07-22): /api/auth/login had no limit on failed
// attempts. This test drives the REAL login handler over the REAL server (the
// newCapTestServer fixture, bootstrapped admin/adminpw) and pins the limiter's
// contract:
//
//   - five consecutive wrong-password attempts: 401 each (the bucket holds 5);
//   - the sixth: 429 with Retry-After (the bucket is empty);
//   - a DIFFERENT username is unaffected (per-username buckets);
//   - the CORRECT password still succeeds even with an empty bucket (successes
//     never consume a token — a legitimate user is never locked out).

import (
	"net/http"
	"testing"
)

func TestP1_3LoginRateLimitedAfterConsecutiveFailures(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()

	attempt := func(user, pass string) int {
		t.Helper()
		rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{user, pass})
		return rec.Code
	}

	// Five consecutive wrong-password attempts for admin: 401 each.
	for i := 0; i < 5; i++ {
		if got := attempt("admin", "wrong-password"); got != http.StatusUnauthorized {
			t.Fatalf("failure %d: want 401, got %d", i+1, got)
		}
	}

	// The sixth: the bucket is empty ⇒ 429 with Retry-After.
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "wrong-password"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("P1-3: the sixth failure must read 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("P1-3/A5: the 429 must carry Retry-After")
	}

	// A different username is unaffected (per-username buckets).
	if got := attempt("ghost", "wrong-password"); got != http.StatusUnauthorized {
		t.Fatalf("P1-3: a different username must not inherit the limit, got %d", got)
	}

	// The CORRECT password still succeeds with an empty bucket: successes never
	// consume a token, so a legitimate user is never locked out.
	if got := attempt("admin", "adminpw"); got != http.StatusOK {
		t.Fatalf("P1-3: a successful login must never be rate-limited, got %d", got)
	}
}
