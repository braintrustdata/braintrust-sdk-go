package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	intlogger "github.com/braintrustdata/braintrust-sdk-go/internal/logger"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// TestSession_WithTestAPIKey tests login with the special test API key
func TestSession_WithTestAPIKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	session, err := NewSession(ctx, Options{
		AppURL: "https://www.braintrust.dev",
		APIKey: TestAPIKey,
		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(ctx)

	require.NoError(t, err)

	// Verify using new getter methods
	apiInfo := session.APIInfo()
	assert.Equal(t, TestAPIKey, apiInfo.APIKey)
	assert.Equal(t, "https://api.braintrust.ai", apiInfo.APIURL)

	org := session.OrgInfo()
	assert.Equal(t, "test-org-id", org.ID)
	assert.Equal(t, "test-org-name", org.Name)
}

// TestSession_WithValidAPIKey tests login with a valid API key
func TestSession_WithValidAPIKey(t *testing.T) {
	t.Parallel()
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request
		assert.Equal(t, "/api/apikey/login", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

		// Return mock response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"org_info": [
				{
					"id": "org-123",
					"name": "test-org",
					"api_url": "https://api.example.com",
					"proxy_url": "https://proxy.example.com"
				}
			]
		}`))
	}))
	defer server.Close()

	session, err := NewSession(context.Background(), Options{
		AppURL: server.URL,
		APIKey: "test-api-key",
		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(context.Background())

	require.NoError(t, err)

	// Verify using new getter methods
	apiInfo := session.APIInfo()
	assert.Equal(t, "test-api-key", apiInfo.APIKey)
	assert.Equal(t, "https://api.example.com", apiInfo.APIURL)

	org := session.OrgInfo()
	assert.Equal(t, "org-123", org.ID)
	assert.Equal(t, "test-org", org.Name)
}

// TestSession_WithInvalidAPIKey tests login with an invalid API key
func TestSession_WithInvalidAPIKey(t *testing.T) {
	t.Parallel()
	// Create a mock server that returns 401
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid API key"))
	}))
	defer server.Close()

	// Use noop logger since we expect login to fail
	session, err := NewSession(context.Background(), Options{
		AppURL: server.URL,
		APIKey: "invalid-key",
		Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API key")
}

// TestSession_OrgSelection tests selecting a specific org by name
func TestSession_OrgSelection(t *testing.T) {
	t.Parallel()
	// Create a mock server with multiple orgs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"org_info": [
				{
					"id": "org-1",
					"name": "org-one",
					"api_url": "https://api1.example.com",
					"proxy_url": "https://proxy1.example.com"
				},
				{
					"id": "org-2",
					"name": "org-two",
					"api_url": "https://api2.example.com",
					"proxy_url": "https://proxy2.example.com"
				}
			]
		}`))
	}))
	defer server.Close()

	session, err := NewSession(context.Background(), Options{
		AppURL:  server.URL,
		APIKey:  "test-api-key",
		OrgName: "org-two",

		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(context.Background())

	require.NoError(t, err)

	org := session.OrgInfo()
	assert.Equal(t, "org-2", org.ID)
	assert.Equal(t, "org-two", org.Name)

	apiInfo := session.APIInfo()
	assert.Equal(t, "https://api2.example.com", apiInfo.APIURL)
}

// TestSession_OrgNotFound tests error when specified org doesn't exist
func TestSession_OrgNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"org_info": [
				{
					"id": "org-1",
					"name": "org-one",
					"api_url": "https://api1.example.com",
					"proxy_url": "https://proxy1.example.com"
				}
			]
		}`))
	}))
	defer server.Close()

	// Use noop logger since we expect login to fail
	session, err := NewSession(context.Background(), Options{
		AppURL:  server.URL,
		APIKey:  "test-api-key",
		OrgName: "non-existent-org",

		Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "organization")
	assert.Contains(t, err.Error(), "non-existent-org")
}

// TestSession_NoAPIKey tests error when no API key is provided
func TestSession_NoAPIKey(t *testing.T) {
	t.Parallel()
	_, err := NewSession(context.Background(), Options{
		AppURL: "https://www.braintrust.dev",

		Logger: intlogger.NewFailTestLogger(t),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key")
}

// TestSession_NoAppURL tests error when no app URL is provided
func TestSession_NoAppURL(t *testing.T) {
	t.Parallel()
	_, err := NewSession(context.Background(), Options{
		APIKey: "test-key",

		Logger: intlogger.NewFailTestLogger(t),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "app URL")
}

// TestSession_WithRealAPIKey tests login with a real API key from environment
func TestSession_WithRealAPIKey(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("BRAINTRUST_API_KEY")

	session, err := NewSession(context.Background(), Options{
		AppURL: "https://www.braintrust.dev",
		APIKey: apiKey,

		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	err = session.Login(context.Background())

	require.NoError(t, err)

	org := session.OrgInfo()
	assert.NotEmpty(t, org.ID)
	assert.NotEmpty(t, org.Name)

	apiInfo := session.APIInfo()
	assert.NotEmpty(t, apiInfo.APIURL)
}

// TestSession_NonBlockingInfo tests that OrgInfo() returns immediately
func TestSession_NonBlockingInfo(t *testing.T) {
	t.Parallel()
	session, err := NewSession(context.Background(), Options{
		AppURL: "https://www.braintrust.dev",
		APIKey: TestAPIKey,
		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	// OrgInfo() should return immediately even if login not complete
	// (In this case with TestAPIKey it will be fast, but still async)
	org := session.OrgInfo()

	// Either it's already done (populated) or still in progress (empty)
	// Both are valid - just verify it returns immediately
	// With TestAPIKey it should complete quickly
	if org.Name != "" {
		assert.Equal(t, "test-org-id", org.ID)
		assert.Equal(t, "test-org-name", org.Name)
	}
}

// TestSession_BlockingLogin tests that Login() blocks until complete
func TestSession_BlockingLogin(t *testing.T) {
	t.Parallel()
	session, err := NewSession(context.Background(), Options{
		AppURL: "https://www.braintrust.dev",
		APIKey: TestAPIKey,
		Logger: intlogger.NewFailTestLogger(t),
	})
	require.NoError(t, err)
	defer session.Close()

	// Login() should block until complete
	err = session.Login(context.Background())

	require.NoError(t, err)

	org := session.OrgInfo()
	assert.Equal(t, "test-org-id", org.ID)
}

// TestSession_APIInfo tests that APIInfo() returns credentials immediately
func TestSession_APIInfo(t *testing.T) {
	t.Parallel()

	t.Run("with all fields", func(t *testing.T) {
		session, err := NewSession(context.Background(), Options{
			AppURL: "https://www.braintrust.dev",
			APIURL: "https://api.braintrust.dev",
			APIKey: "test-key-123",
			Logger: logger.Discard(),
		})
		require.NoError(t, err)
		defer session.Close()

		// APIInfo() should return immediately, no login required
		apiInfo := session.APIInfo()

		assert.Equal(t, "test-key-123", apiInfo.APIKey)
		assert.Equal(t, "https://api.braintrust.dev", apiInfo.APIURL)
	})

	t.Run("with default APIURL", func(t *testing.T) {
		session, err := NewSession(context.Background(), Options{
			AppURL: "https://www.braintrust.dev",
			// APIURL not specified - should use default
			APIKey: "test-key-456",
			Logger: logger.Discard(),
		})
		require.NoError(t, err)
		defer session.Close()

		apiInfo := session.APIInfo()

		assert.Equal(t, "test-key-456", apiInfo.APIKey)
		assert.Equal(t, "https://api.braintrust.dev", apiInfo.APIURL) // Default
	})

	t.Run("available before login completes", func(t *testing.T) {
		// Create session with invalid URL so login hangs
		session, err := NewSession(context.Background(), Options{
			AppURL: "http://localhost:99999", // Invalid - will retry forever
			APIKey: "test-key-789",
			Logger: logger.Discard(),
		})
		require.NoError(t, err)
		defer session.Close()

		// APIInfo() should work immediately even though login hasn't completed
		apiInfo := session.APIInfo()

		assert.Equal(t, "test-key-789", apiInfo.APIKey)
		assert.Equal(t, "https://api.braintrust.dev", apiInfo.APIURL)
	})
}

// TestSession_OrgName tests that OrgName() returns org name after login
func TestSession_OrgName(t *testing.T) {
	t.Parallel()

	t.Run("returns empty before login completes", func(t *testing.T) {
		session, err := NewSession(context.Background(), Options{
			AppURL: "http://localhost:99999", // Invalid - will hang
			APIKey: "test-key",
			Logger: logger.Discard(),
		})
		require.NoError(t, err)
		defer session.Close()

		// Should return empty string before login completes
		assert.Equal(t, "", session.OrgName())
	})

	t.Run("returns org name after successful login", func(t *testing.T) {
		session, err := NewSession(context.Background(), Options{
			AppURL: "https://www.braintrust.dev",
			APIKey: TestAPIKey,
			Logger: intlogger.NewFailTestLogger(t),
		})
		require.NoError(t, err)
		defer session.Close()

		// Wait for login to complete
		err = session.Login(context.Background())
		require.NoError(t, err)

		// Should return org name
		assert.Equal(t, "test-org-name", session.OrgName())
	})
}
