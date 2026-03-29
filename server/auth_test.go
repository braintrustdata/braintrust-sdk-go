package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
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

func TestAuthFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), authContextKey, "not-an-authResult")
	result := authFromContext(ctx)
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

func TestAuthCache_Evict(t *testing.T) {
	cache := newAuthCache("https://app.test", 64, nil)
	token := "test-token"
	orgName := "test-org"

	// Manually insert a fake entry
	key := cacheKey(token, orgName)
	session := auth.NewTestSession("key", "org-id", orgName, "https://api.test", "https://app.test", "https://app.test", nil)
	cache.entries[key] = &authResult{session: session, api: nil}
	cache.order = append(cache.order, key)

	// Verify it's there
	assert.Len(t, cache.entries, 1)
	assert.Len(t, cache.order, 1)

	// Evict it
	cache.evict(token, orgName)

	// Verify it's gone
	assert.Len(t, cache.entries, 0)
	assert.Len(t, cache.order, 0)
}

func TestAuthCache_EvictNonExistent(t *testing.T) {
	cache := newAuthCache("https://app.test", 64, nil)

	// Evicting a key that doesn't exist should be a no-op
	cache.evict("no-such-token", "no-such-org")

	assert.Len(t, cache.entries, 0)
	assert.Len(t, cache.order, 0)
}

func TestIsAuthError(t *testing.T) {
	assert.True(t, isAuthError(&https.HTTPError{StatusCode: 401}))
	assert.True(t, isAuthError(&https.HTTPError{StatusCode: 403}))
	assert.True(t, isAuthError(fmt.Errorf("wrapped: %w", &https.HTTPError{StatusCode: 401})))
	assert.False(t, isAuthError(&https.HTTPError{StatusCode: 500}))
	assert.False(t, isAuthError(fmt.Errorf("some other error")))
	assert.False(t, isAuthError(nil))
}
