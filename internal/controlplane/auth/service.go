package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// Service-level errors.
var (
	ErrUserNotFound       = errors.New("auth: user not found")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUserExists         = errors.New("auth: user already exists")
	ErrTokenRevoked       = errors.New("auth: refresh token revoked")
)

const (
	defaultAccessTTL  = 15 * 60       // 15 minutes
	defaultRefreshTTL = 7 * 24 * 3600 // 7 days
)

// AuthService handles registration, login, and the access/refresh token lifecycle.
//
// Access and refresh tokens are signed with distinct secrets so a leaked access
// token cannot be replayed against the refresh endpoint. Both are minimal HS256
// JWTs (see token.go) — no external JWT dependency, preserving the single-binary
// 8-bit constraint (ADR-003).
type AuthService struct {
	stor          storage.Storage
	accessSecret  string
	refreshSecret string
	accessTTL     int64
	refreshTTL    int64
}

// NewAuthService builds an AuthService. accessSecret signs access tokens,
// refreshSecret signs refresh tokens.
func NewAuthService(stor storage.Storage, accessSecret, refreshSecret string) *AuthService {
	return &AuthService{
		stor:          stor,
		accessSecret:  accessSecret,
		refreshSecret: refreshSecret,
		accessTTL:     defaultAccessTTL,
		refreshTTL:    defaultRefreshTTL,
	}
}

// Register creates a new user with NO roles (least privilege). The bootstrap
// admin is created separately by the synchronizer (Phase 1.3), not here.
func (s *AuthService) Register(username, password string) (storage.User, error) {
	if username == "" || password == "" {
		return storage.User{}, errors.New("auth: username and password required")
	}
	if existing, err := s.stor.Users().GetByUsername(username); err == nil {
		_ = existing
		return storage.User{}, ErrUserExists
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return storage.User{}, err
	}
	return s.stor.Users().Save(storage.User{
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    time.Now(),
	})
}

// Login validates credentials and returns a freshly signed access+refresh pair.
func (s *AuthService) Login(username, password string) (access, refresh string, user storage.User, err error) {
	u, err := s.stor.Users().GetByUsername(username)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", "", storage.User{}, ErrInvalidCredentials
		}
		return "", "", storage.User{}, err
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return "", "", storage.User{}, ErrInvalidCredentials
	}
	access, err = SignToken(NewClaims(u.Username, s.accessTTL), s.accessSecret)
	if err != nil {
		return "", "", storage.User{}, err
	}
	refresh, err = SignToken(NewClaims(u.Username, s.refreshTTL), s.refreshSecret)
	if err != nil {
		return "", "", storage.User{}, err
	}
	return access, refresh, u, nil
}

// tokenFloorKey is the config-store key holding username's revocation floor
// (the earliest token issue time still honored). The floor approach
// (quality-baseline 2026-07-22 P1-2) needs no per-token state: revoking a user
// invalidates every refresh token they hold at once, and the floor survives
// restarts whenever the storage is persisted (sqlite; the in-memory demo
// storage keeps it for the process lifetime — registered as the demo's own
// limitation).
func tokenFloorKey(username string) string { return "auth:token-floor:" + username }

// RevokeRefresh invalidates every refresh token issued to username before now:
// it records a revocation FLOOR in the config store, and Refresh rejects any
// refresh token whose `iat` precedes the floor. The granularity is per user
// (every device dies together). Per-TOKEN revocation ("log out the other
// sessions") is the jti denylist's job — see DenyRefreshToken (P54,
// ADR-087/088); the two mechanisms answer different questions and Refresh
// consults both.
//
// The floor does not touch ACCESS tokens by design: they expire on their own
// 15-minute TTL, so revoking the long-lived credential is the part that needs
// server-side state (checking the floor on every authenticated request would
// add a storage read to the hot path).
func (s *AuthService) RevokeRefresh(username string) error {
	if username == "" {
		return errors.New("auth: username required")
	}
	return s.stor.Config().Set(tokenFloorKey(username), fmt.Sprintf("%d", time.Now().Unix()))
}

// refreshFloor loads username's revocation floor; (0, false) when none is
// recorded. A storage error propagates: a floor that cannot be READ is a floor
// that cannot be honored (fail-closed).
func (s *AuthService) refreshFloor(username string) (int64, bool, error) {
	v, ok, err := s.stor.Config().Get(tokenFloorKey(username))
	if err != nil || !ok {
		return 0, false, err
	}
	floor, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, false, perr
	}
	return floor, true, nil
}

// tokenDenyKey is the config-store key denying ONE refresh token by its jti
// (P54, ADR-087/088). Per-token: revoking a single session leaves the user's
// other sessions rotating — the shape the floor (a time bound) cannot express.
func tokenDenyKey(jti string) string { return "auth:token-denylist:" + jti }

// DenyRefreshToken records jti as revoked; exp is informational (diagnostics /
// future compaction) — a hit is a hit, the value is never consulted to decide.
// One key per jti: no read-modify-write, so concurrent revokes cannot lose an
// entry. Entries are inert once the token expires (its jti can never appear in
// a valid token again) but are never deleted — the Config store has no Delete,
// registered as the mechanism's growth bound (ADR-087 §6).
func (s *AuthService) DenyRefreshToken(jti string, exp int64) error {
	if jti == "" {
		return errors.New("auth: jti required")
	}
	return s.stor.Config().Set(tokenDenyKey(jti), strconv.FormatInt(exp, 10))
}

// refreshDenied reports whether jti is on the denylist. A storage error
// propagates: a denylist that cannot be READ is a denylist that cannot be
// honored (fail-closed, same discipline as the floor).
func (s *AuthService) refreshDenied(jti string) (bool, error) {
	v, ok, err := s.stor.Config().Get(tokenDenyKey(jti))
	if err != nil {
		return false, err
	}
	return ok && v != "", nil
}

// ParseRefreshClaims validates a refresh token with the refresh secret and
// returns its claims — "only the holder of the token can name it" is the
// authentication the per-token revoke endpoint relies on. (The endpoint still
// checks the subject against the caller / admin role before denying.)
func (s *AuthService) ParseRefreshClaims(token string) (Claims, error) {
	return ParseToken(token, s.refreshSecret)
}

// Refresh validates a refresh token and rotates both tokens.
//
// P1-2: the refresh is rejected when the token's `iat` precedes username's
// revocation floor — a revoked user's leaked refresh token dies at the floor,
// while tokens minted AFTER the revoke (a fresh login) are unaffected.
func (s *AuthService) Refresh(refreshToken string) (access, refresh string, err error) {
	claims, err := ParseToken(refreshToken, s.refreshSecret)
	if err != nil {
		return "", "", err
	}
	if floor, ok, ferr := s.refreshFloor(claims.Sub); ferr != nil {
		return "", "", ferr
	} else if ok && claims.Iat <= floor {
		// `<=` on purpose: the floor is recorded at SECOND granularity (the
		// claims' iat is second-granular too), so "the revoke's own second"
		// must also die — otherwise a token minted in that same second would
		// survive. Cost: a re-login in the same second as the revoke is
		// rejected for up to one second (the user retries and succeeds).
		return "", "", ErrTokenRevoked
	}
	// P54 (ADR-087/088): per-token revocation, checked AFTER the floor (the
	// coarse mechanism) and BEFORE rotation. The rotated successor carries a
	// fresh jti, so denying the old one is exactly "log out that session".
	if claims.Jti != "" {
		if denied, derr := s.refreshDenied(claims.Jti); derr != nil {
			return "", "", derr // fail-closed: an unreadable denylist is not an empty one
		} else if denied {
			return "", "", ErrTokenRevoked
		}
	}
	access, err = SignToken(NewClaims(claims.Sub, s.accessTTL), s.accessSecret)
	if err != nil {
		return "", "", err
	}
	refresh, err = SignToken(NewClaims(claims.Sub, s.refreshTTL), s.refreshSecret)
	if err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

// Authenticate validates an access token and returns the subject username.
// Used by the HTTP middleware (Phase 1.5) to resolve the caller.
func (s *AuthService) Authenticate(accessToken string) (string, error) {
	claims, err := ParseToken(accessToken, s.accessSecret)
	if err != nil {
		return "", err
	}
	return claims.Sub, nil
}
