package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

type contextKey string

const authContextKey contextKey = "braintrust.auth"

// authResult holds the validated auth context for a request.
type authResult struct {
	session *auth.Session
	api     *api.API
}

// newAuthResult creates a session, logs in, and builds an API client.
// This is the shared auth flow used by both per-request auth and no-auth mode.
func newAuthResult(ctx context.Context, apiKey, appURL, apiURL, orgName string, log logger.Logger) (*authResult, error) {
	httpClient := https.NewClient(apiKey, appURL, log)
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey:       apiKey,
		AppURL:       appURL,
		AppPublicURL: appURL,
		APIURL:       apiURL,
		OrgName:      orgName,
		Logger:       log,
		Client:       httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	if err := session.Login(ctx); err != nil {
		session.Close()
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	apiInfo := session.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL), api.WithLogger(log))

	return &authResult{session: session, api: apiClient}, nil
}

// authCache is an LRU cache of authenticated sessions.
type authCache struct {
	mu      sync.Mutex
	entries map[string]*authResult
	order   []string
	maxSize int
	appURL  string
	log     logger.Logger
}

func newAuthCache(appURL string, maxSize int, log logger.Logger) *authCache {
	return &authCache{
		entries: make(map[string]*authResult),
		maxSize: maxSize,
		appURL:  appURL,
		log:     log,
	}
}

// cacheKey builds a collision-free cache key from request auth headers.
// Uses length-prefixing to prevent "a:b"+"c" == "a"+"b:c" collisions.
func cacheKey(token, orgName string) string {
	return fmt.Sprintf("%d:%s:%s", len(token), token, orgName)
}

// getOrCreate returns a cached auth result or creates a new one.
func (c *authCache) getOrCreate(ctx context.Context, token, orgName string) (*authResult, error) {
	key := cacheKey(token, orgName)

	c.mu.Lock()
	if result, ok := c.entries[key]; ok {
		c.moveToEnd(key)
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()

	// Create new session outside the lock to avoid holding it during network calls
	result, err := c.createSession(ctx, token, orgName)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check: another goroutine may have inserted this key while we were unlocked
	if existing, ok := c.entries[key]; ok {
		// Discard the session we just created; use the existing one
		result.session.Close()
		return existing, nil
	}

	// Evict oldest if at capacity
	if len(c.entries) >= c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		if evicted, ok := c.entries[oldest]; ok {
			evicted.session.Close()
		}
		delete(c.entries, oldest)
		c.order = c.order[1:]
	}

	c.entries[key] = result
	c.order = append(c.order, key)
	return result, nil
}

// createSession creates and validates a new auth session.
func (c *authCache) createSession(ctx context.Context, token, orgName string) (*authResult, error) {
	return newAuthResult(ctx, token, c.appURL, "", orgName, c.log)
}

// evict removes a cache entry by token and org name, closing its session.
// Called when a cached session produces auth errors during eval execution.
func (c *authCache) evict(token, orgName string) {
	key := cacheKey(token, orgName)
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[key]; ok {
		entry.session.Close()
		delete(c.entries, key)
		for i, k := range c.order {
			if k == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
}

// moveToEnd moves a key to the end of the LRU order.
// O(n) scan over the order slice; fine for defaultAuthCacheMax (64).
// Must be called with c.mu held.
func (c *authCache) moveToEnd(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			return
		}
	}
}

// isAuthError returns true if the error chain contains an HTTP 401 or 403.
func isAuthError(err error) bool {
	var httpErr *https.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 401 || httpErr.StatusCode == 403
	}
	return false
}

// extractToken extracts the auth token from request headers.
func extractToken(r *http.Request) string {
	// Prefer x-bt-auth-token
	if token := strings.TrimSpace(r.Header.Get("X-Bt-Auth-Token")); token != "" {
		return token
	}
	// Fall back to Authorization: Bearer <token>
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	return strings.TrimSpace(authHeader)
}

// extractOrgName extracts the organization name from request headers.
func extractOrgName(r *http.Request) string {
	return r.Header.Get("X-Bt-Org-Name")
}

// authFromContext retrieves the auth result from request context.
func authFromContext(ctx context.Context) *authResult {
	result, _ := ctx.Value(authContextKey).(*authResult)
	return result
}

// authMiddleware validates request auth and injects the auth result into context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.noAuth {
			next.ServeHTTP(w, r)
			return
		}

		token := extractToken(r)
		if token == "" {
			http.Error(w, `{"error":"missing authentication token"}`, http.StatusUnauthorized)
			return
		}

		orgName := extractOrgName(r)
		result, err := s.authCache.getOrCreate(r.Context(), token, orgName)
		if err != nil {
			s.logger.Warn("authentication failed", "error", err)
			http.Error(w, `{"error":"authentication failed"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), authContextKey, result)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
