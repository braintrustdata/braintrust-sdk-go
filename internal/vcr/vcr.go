// Package vcr provides utilities for recording and replaying HTTP interactions in tests using go-vcr.
package vcr

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/dnaeon/go-vcr.v3/cassette"
	"gopkg.in/dnaeon/go-vcr.v3/recorder"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	intlogger "github.com/braintrustdata/braintrust-sdk-go/internal/logger"
)

// Mode represents the mode for VCR operations
type Mode string

const (
	// ModeOff disables VCR and uses real HTTP requests
	ModeOff Mode = "off"
	// ModeRecord records or updates cassettes
	ModeRecord Mode = "record"
	// ModeReplay replays from existing cassettes (default)
	ModeReplay Mode = "replay"
)

// GetVCRMode reads the VCR_MODE environment variable and returns the mode.
// Defaults to replay mode if not set or invalid.
func GetVCRMode() Mode {
	mode := os.Getenv("VCR_MODE")
	switch mode {
	case string(ModeOff):
		return ModeOff
	case string(ModeRecord):
		return ModeRecord
	case string(ModeReplay), "":
		return ModeReplay
	default:
		// Invalid mode, default to replay
		return ModeReplay
	}
}

// NewVCRRecorder creates a new VCR recorder for the given cassette path.
// The recorder automatically scrubs sensitive headers before saving.
func NewVCRRecorder(t *testing.T, cassettePath string) (*recorder.Recorder, error) {
	t.Helper()

	mode := GetVCRMode()

	var recorderMode recorder.Mode
	switch mode {
	case ModeRecord:
		recorderMode = recorder.ModeRecordOnly
	case ModeReplay:
		recorderMode = recorder.ModeReplayOnly
	default:
		// ModeOff - shouldn't reach here, caller should check
		t.Fatalf("NewVCRRecorder called with ModeOff - this is a programming error")
		return nil, nil
	}

	r, err := recorder.NewWithOptions(&recorder.Options{
		CassetteName:       cassettePath,
		Mode:               recorderMode,
		SkipRequestLatency: true, // Don't simulate recorded delays in replay mode
	})
	if err != nil {
		return nil, err
	}

	// Add hook to scrub sensitive data before saving cassettes
	r.AddHook(scrubCredentials, recorder.BeforeSaveHook)

	return r, nil
}

// scrubCredentials removes sensitive headers from cassette interactions
// before they are saved to disk.
func scrubCredentials(i *cassette.Interaction) error {
	// Sensitive header patterns to scrub
	sensitivePatterns := []string{
		"authorization",
		"api-key",
		"organization-id",
	}

	targets := []map[string][]string{
		i.Request.Headers,
		i.Response.Headers,
	}

	for _, headers := range targets {
		// Build list of all header keys first
		keys := make([]string, 0, len(headers))
		for key := range headers {
			keys = append(keys, key)
		}

		// Iterate the list and delete sensitive headers
		for _, key := range keys {
			lowerKey := strings.ToLower(key)
			for _, pattern := range sensitivePatterns {
				if strings.Contains(lowerKey, pattern) {
					delete(headers, key)
					break
				}
			}
		}
	}

	return nil
}

// WrapHTTPClient wraps an existing http.Client with VCR recording/replay functionality.
// If VCR_MODE=off, returns the original client unchanged.
// The cassette name is automatically derived from t.Name().
// Cassettes are stored in testdata/cassettes/<test-name>.yaml
func WrapHTTPClient(t *testing.T, httpClient *http.Client) *http.Client {
	t.Helper()

	mode := GetVCRMode()
	if mode == ModeOff {
		// VCR disabled, return original client
		return httpClient
	}

	// Build cassette path (don't add .yaml extension, recorder adds it automatically)
	cassettePath := filepath.Join("testdata", "cassettes", t.Name())

	// Create recorder
	r, err := NewVCRRecorder(t, cassettePath)
	if err != nil {
		t.Fatalf("Failed to create VCR recorder: %v", err)
	}

	// Register cleanup to stop recorder
	t.Cleanup(func() {
		if err := r.Stop(); err != nil {
			t.Errorf("Failed to stop VCR recorder: %v", err)
		}
	})

	// Create new client with VCR transport
	vcrClient := &http.Client{
		Transport: r,
		// Copy other settings from original client
		CheckRedirect: httpClient.CheckRedirect,
		Jar:           httpClient.Jar,
		Timeout:       httpClient.Timeout,
	}

	return vcrClient
}

// NewHTTPClient creates a new HTTP client with VCR support.
// If VCR_MODE=off, returns a standard HTTP client.
// The cassette name is automatically derived from t.Name().
func NewHTTPClient(t *testing.T) *http.Client {
	t.Helper()

	baseClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	return WrapHTTPClient(t, baseClient)
}

// GetAPIKeyForVCR returns an API key for VCR-enabled tests.
// In replay mode, returns a dummy key. In record/off modes, reads from BRAINTRUST_API_KEY env var.
func GetAPIKeyForVCR(t *testing.T) string {
	t.Helper()

	mode := GetVCRMode()

	// In replay mode, we don't need API keys
	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if mode != ModeReplay && apiKey == "" {
		t.Fatal("BRAINTRUST_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		// Use dummy key for replay mode
		apiKey = "dummy-api-key-for-replay"
	}

	return apiKey
}

// GetHTTPSClient creates an HTTPS client for integration tests with VCR support.
// It wraps the HTTP client with VCR recording/replay based on VCR_MODE environment variable.
// The cassette name is automatically derived from t.Name().
// In replay mode, a dummy API key is used. In record/off modes, BRAINTRUST_API_KEY is required.
func GetHTTPSClient(t *testing.T) *https.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	apiKey := GetAPIKeyForVCR(t)

	apiURL := os.Getenv("BRAINTRUST_API_URL")
	if apiURL == "" {
		apiURL = "https://api.braintrust.dev"
	}

	log := intlogger.NewFailTestLogger(t)

	// Create VCR-wrapped HTTP client
	vcrClient := NewHTTPClient(t)

	// Create HTTPS client with the VCR-wrapped HTTP client
	client := https.NewWrappedClient(apiKey, apiURL, vcrClient, log)

	return client
}
