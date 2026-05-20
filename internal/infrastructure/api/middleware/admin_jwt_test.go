package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("test-secret-must-be-at-least-32-bytes-long")

func newAdminJWTRouter(t *testing.T, cfg AdminJWTConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/protected", AdminJWTMiddleware(cfg),
		func(c *gin.Context) {
			user, _ := c.Get("admin_user")
			c.JSON(http.StatusOK, gin.H{"user": user})
		})
	return router
}

func TestAdminJWTMiddleware_HappyPath(t *testing.T) {
	token, err := MintAdminJWT(testSecret, "ops-leon", 30*time.Minute)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+token, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"user":"ops-leon"`)
}

func TestAdminJWTMiddleware_MissingToken(t *testing.T) {
	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing token")
}

func TestAdminJWTMiddleware_InvalidSignature(t *testing.T) {
	// Mint with one secret, validate with another
	otherSecret := []byte("different-secret-must-be-at-least-32-bytes")
	token, err := MintAdminJWT(otherSecret, "ops-leon", 30*time.Minute)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+token, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid token")
}

func TestAdminJWTMiddleware_ExpiredToken(t *testing.T) {
	// Manually craft a token that's already expired
	claims := AdminClaims{
		User:  "ops-leon",
		Scope: AdminJWTScope,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+signed, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminJWTMiddleware_WrongScope(t *testing.T) {
	// Mint with a non-admin scope
	claims := AdminClaims{
		User:  "ops-leon",
		Scope: "user.read",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+signed, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "wrong scope")
}

func TestAdminJWTMiddleware_TTLExceedsServerPolicy(t *testing.T) {
	// Mint a 24h token; server policy caps at 1h
	token, err := MintAdminJWT(testSecret, "ops-leon", 24*time.Hour)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{
		Secret: testSecret,
		MaxTTL: time.Hour,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+token, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "ttl")
}

func TestAdminJWTMiddleware_AlgNoneAttackRejected(t *testing.T) {
	// Craft a token with alg=none — this should be rejected by the
	// keyfunc's explicit signing-method check
	claims := AdminClaims{
		User:  "attacker",
		Scope: AdminJWTScope,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token="+signed, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"alg=none attack must be rejected")
}

func TestAdminJWTMiddleware_MalformedToken(t *testing.T) {
	router := newAdminJWTRouter(t, AdminJWTConfig{Secret: testSecret})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected?token=not-a-real-jwt", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMintAdminJWT_Validations(t *testing.T) {
	t.Run("rejects short secret", func(t *testing.T) {
		_, err := MintAdminJWT([]byte("too-short"), "ops-leon", time.Hour)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least 32 bytes")
	})
	t.Run("rejects empty user", func(t *testing.T) {
		_, err := MintAdminJWT(testSecret, "", time.Hour)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "user must not be empty")
	})
	t.Run("mints valid token", func(t *testing.T) {
		signed, err := MintAdminJWT(testSecret, "ops-leon", time.Hour)
		require.NoError(t, err)
		// JWT has 3 segments separated by dots
		assert.Equal(t, 3, strings.Count(signed, ".")+1)
	})
}
