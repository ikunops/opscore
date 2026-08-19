package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims is the JWT payload. Sub is the username; Jti is a unique random id so
// every token is distinct even when minted within the same second, supporting
// future revocation lists without a server-side session store.
type Claims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
}

// newJTI returns a random 16-byte hex string.
func newJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; fall back to time-based uniqueness
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func sign(segments string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(segments))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignToken produces a minimal HS256 JWT (header.payload.signature).
func SignToken(claims Claims, secret string) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	seg := b64(header) + "." + b64(payload)
	return seg + "." + sign(seg, []byte(secret)), nil
}

// ParseToken validates the signature and expiry, returning the Claims.
func ParseToken(token, secret string) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("auth: malformed token")
	}
	expected := sign(parts[0]+"."+parts[1], []byte(secret))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return c, errors.New("auth: invalid signature")
	}
	payload, err := unb64(parts[1])
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, err
	}
	if c.Exp > 0 && time.Now().Unix() > c.Exp {
		return c, errors.New("auth: token expired")
	}
	return c, nil
}

// NewClaims builds Claims with iat=now, exp=now+ttl, and a fresh random Jti.
func NewClaims(sub string, ttlSeconds int64) Claims {
	now := time.Now().Unix()
	return Claims{Sub: sub, Iat: now, Exp: now + ttlSeconds, Jti: newJTI()}
}
