// Package tests provides test utilities for creating test sessions and other test helpers.
package tests

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
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
