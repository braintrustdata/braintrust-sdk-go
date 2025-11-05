// Package tests provides test utilities for creating test sessions and other test helpers.
package tests

import (
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
