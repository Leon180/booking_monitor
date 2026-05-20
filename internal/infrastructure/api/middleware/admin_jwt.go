package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// AdminJWTScope is the JWT `scope` claim required for SSE admin
// endpoint access. Hardcoded constant — there's only one scope.
const AdminJWTScope = "admin.events"

// DefaultAdminJWTMaxTTL caps the lifetime of any single admin token.
// Even if a token mint utility produces a 24h token, the server
// rejects it. Forces ops to regenerate hourly — limits exposure
// window if a token leaks via access logs or screen-share.
const DefaultAdminJWTMaxTTL = time.Hour

// AdminClaims is the JWT payload for admin SSE tokens.
//
// Field choices (see docs/design/admin_event_streaming.md § Q12):
//   - User: free-form ops identifier ("ops-leon", "ops-amy"); set
//     in c.Set("admin_user", ...) for audit logging downstream.
//   - Scope: must equal AdminJWTScope. Reserves space for future
//     per-endpoint scoping (e.g., "admin.events.read-only").
//   - jwt.RegisteredClaims: provides iat, exp, nbf, iss, sub, aud,
//     jti — library-validated automatically via ParseWithClaims.
type AdminClaims struct {
	User  string `json:"user"`
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

// AdminJWTConfig configures the middleware.
type AdminJWTConfig struct {
	Secret []byte        // HS256 signing key
	MaxTTL time.Duration // server-enforced lifetime cap
}

// AdminJWTMiddleware validates the `?token=<jwt>` query parameter
// on admin SSE endpoints.
//
// The token is read from the query string (not headers) because
// the browser's EventSource API cannot set custom headers — see
// design doc § Q12 and MDN:
// https://developer.mozilla.org/en-US/docs/Web/API/EventSource
//
// On success, c.Set("admin_user", claims.User) so downstream
// handlers can log the active operator.
func AdminJWTMiddleware(cfg AdminJWTConfig) gin.HandlerFunc {
	if cfg.MaxTTL == 0 {
		cfg.MaxTTL = DefaultAdminJWTMaxTTL
	}
	return func(c *gin.Context) {
		tokenStr := c.Query("token")
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing token",
			})
			return
		}

		var claims AdminClaims
		token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
			// CRITICAL: explicitly assert the signing method is HMAC.
			// Without this check, an attacker could send a token with
			// `alg: none` (no signature) and the library would accept
			// it. See https://github.com/golang-jwt/jwt/blob/main/MIGRATION_GUIDE.md
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return cfg.Secret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid token",
			})
			return
		}

		// Scope check — explicit, not relying on library claim parsing
		if claims.Scope != AdminJWTScope {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "wrong scope",
			})
			return
		}

		// TTL cap — server-enforced; reject overly-long tokens even
		// if signature is valid
		if claims.IssuedAt == nil || claims.ExpiresAt == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing required claims",
			})
			return
		}
		ttl := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
		if ttl > cfg.MaxTTL {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "ttl exceeds server policy",
			})
			return
		}
		if ttl < 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "negative ttl",
			})
			return
		}

		c.Set("admin_user", claims.User)
		c.Next()
	}
}

// MintAdminJWT is a helper for the token-mint CLI tool. Produces
// a signed token suitable for the AdminJWTMiddleware to accept.
//
// Caller is responsible for ensuring ttl <= server's MaxTTL —
// otherwise the server will reject the token at validation time.
func MintAdminJWT(secret []byte, user string, ttl time.Duration) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("admin jwt secret must be at least 32 bytes")
	}
	if user == "" {
		return "", errors.New("admin jwt user must not be empty")
	}
	now := time.Now()
	claims := AdminClaims{
		User:  user,
		Scope: AdminJWTScope,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign admin jwt: %w", err)
	}
	return signed, nil
}
