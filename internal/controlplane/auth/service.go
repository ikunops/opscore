package auth

import (
	"errors"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// Service-level errors.
var (
	ErrUserNotFound       = errors.New("auth: user not found")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUserExists         = errors.New("auth: user already exists")
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

// Refresh validates a refresh token and rotates both tokens.
func (s *AuthService) Refresh(refreshToken string) (access, refresh string, err error) {
	claims, err := ParseToken(refreshToken, s.refreshSecret)
	if err != nil {
		return "", "", err
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
