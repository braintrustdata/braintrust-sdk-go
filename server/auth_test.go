package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractToken(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "x-bt-auth-token header",
			headers:  map[string]string{"X-Bt-Auth-Token": "token123"},
			expected: "token123",
		},
		{
			name:     "bearer authorization",
			headers:  map[string]string{"Authorization": "Bearer token456"},
			expected: "token456",
		},
		{
			name:     "plain authorization",
			headers:  map[string]string{"Authorization": "token789"},
			expected: "token789",
		},
		{
			name:     "x-bt-auth-token takes priority",
			headers:  map[string]string{"X-Bt-Auth-Token": "preferred", "Authorization": "Bearer fallback"},
			expected: "preferred",
		},
		{
			name:     "no token",
			headers:  map[string]string{},
			expected: "",
		},
		{
			name:     "bearer with extra whitespace",
			headers:  map[string]string{"Authorization": "Bearer   token-ws  "},
			expected: "token-ws",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			assert.Equal(t, tt.expected, extractToken(r))
		})
	}
}

func TestExtractOrgName(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Bt-Org-Name", "my-org")
	assert.Equal(t, "my-org", extractOrgName(r))
}

func TestAuthMiddleware_NoAuth(t *testing.T) {
	srv := New(WithNoAuth())
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	srv := New() // auth enabled by default
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing authentication token")
}

func TestAuthFromContext_Nil(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	result := authFromContext(r.Context())
	assert.Nil(t, result)
}

func TestAuthCacheKey(t *testing.T) {
	key := cacheKey("token", "org")
	assert.Equal(t, "5:token:org", key)
}

func TestAuthCacheKey_NoCollision(t *testing.T) {
	// "a:b" + "c" should differ from "a" + "b:c"
	key1 := cacheKey("a:b", "c")
	key2 := cacheKey("a", "b:c")
	assert.NotEqual(t, key1, key2)
}
