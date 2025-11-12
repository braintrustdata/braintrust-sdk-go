// Package tests provides test utilities for creating test sessions and other test helpers.
package tests

import (
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	intlogger "github.com/braintrustdata/braintrust-sdk-go/internal/logger"
)

// NewSession creates a static test session with hardcoded data.
// This session does not make any network calls or start goroutines.
// Uses the fail logger if t is provided.
func NewSession(t *testing.T) *auth.Session {
	t.Helper()
	log := intlogger.NewFailTestLogger(t)

	done := make(chan struct{})
	close(done) // Already done, no login needed

	info := &auth.Info{
		OrgName:      "test-org",
		OrgID:        "org-test-12345",
		AppPublicURL: "https://test.braintrust.dev",
		AppURL:       "https://test.braintrust.dev",
		APIURL:       "https://api-test.braintrust.dev",
		APIKey:       auth.TestAPIKey,
		LoggedIn:     true,
	}

	return auth.NewTestSession(info, done, log)
}

// RandomString generates a random string of the specified length
func RandomString(length int) string {
	charset := "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]rune, length)
	for i := range b {
		b[i] = rune(charset[rand.Intn(len(charset))])
	}
	return string(b)
}

// Name generates a test-specific name by combining t.Name() with optional suffixes.
//
// Example usage:
//
//	tests.Name(t)                    // "TestFoo"
//	tests.Name(t, "slug")            // "TestFoo-slug"
//	tests.Name(t, "task", "v2")      // "TestFoo-task-v2"
func Name(t *testing.T, suffixes ...string) string {
	t.Helper()

	name := t.Name()

	if len(suffixes) == 0 {
		return name
	}

	for _, suffix := range suffixes {
		if suffix != "" {
			name = name + "-" + suffix
		}
	}

	return name
}

// RandomName generates a unique name for tests using the test name and a random suffix.
// This ensures test resources don't collide when running tests in parallel.
func RandomName(t *testing.T, suffixes ...string) string {
	t.Helper()
	parts := []string{
		"go-sdk-test",
		t.Name(),
		RandomString(8),
	}
	parts = append(parts, suffixes...)
	return strings.Join(parts, "-")
}

// GetTestHTTPSClient creates an HTTPS client for integration tests.
// It reads BRAINTRUST_API_KEY and BRAINTRUST_API_URL from environment variables.
// Uses the fail logger to report errors immediately.
// Skips the test if running in short mode (-short flag).
// Fails the test if BRAINTRUST_API_KEY is not set.
func GetTestHTTPSClient(t *testing.T) *https.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if apiKey == "" {
		t.Fatal("BRAINTRUST_API_KEY not set")
	}

	apiURL := os.Getenv("BRAINTRUST_API_URL")
	if apiURL == "" {
		apiURL = "https://api.braintrust.dev"
	}

	log := intlogger.NewFailTestLogger(t)
	client := https.NewClient(apiKey, apiURL, log)

	return client
}
