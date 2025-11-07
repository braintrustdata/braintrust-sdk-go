// Package config provides configuration management for the Braintrust SDK.
package config

import (
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Config holds immutable configuration for the Braintrust SDK.
type Config struct {
	APIKey             string
	APIURL             string
	AppURL             string
	OrgName            string
	DefaultProjectID   string
	DefaultProjectName string
	BlockingLogin      bool

	// Tracing configuration
	FilterAISpans   bool
	SpanFilterFuncs []SpanFilterFunc
	Exporter        trace.SpanExporter

	// Logger
	Logger logger.Logger
}

// SpanFilterFunc is a function that decides which spans to send to Braintrust.
// Return >0 to keep the span, <0 to drop the span, or 0 to not influence the decision.
type SpanFilterFunc func(span trace.ReadOnlySpan) int

// FromEnv loads configuration from environment variables with defaults.
//
// Supported environment variables:
//   - BRAINTRUST_API_KEY: API key for authentication
//   - BRAINTRUST_API_URL: API endpoint URL (default: "https://api.braintrust.dev")
//   - BRAINTRUST_APP_URL: Application URL (default: "https://www.braintrust.dev")
//   - BRAINTRUST_ORG_NAME: Organization name
//   - BRAINTRUST_DEFAULT_PROJECT_ID: Default project ID
//   - BRAINTRUST_DEFAULT_PROJECT: Default project name (default: "default-go-project")
//   - BRAINTRUST_BLOCKING_LOGIN: Enable blocking login (default: false)
//   - BRAINTRUST_OTEL_FILTER_AI_SPANS: Filter to keep only AI-related spans (default: false)
func FromEnv() *Config {
	return &Config{
		APIKey:             getEnvString("BRAINTRUST_API_KEY", ""),
		APIURL:             getEnvString("BRAINTRUST_API_URL", "https://api.braintrust.dev"),
		AppURL:             getEnvString("BRAINTRUST_APP_URL", "https://www.braintrust.dev"),
		OrgName:            getEnvString("BRAINTRUST_ORG_NAME", ""),
		DefaultProjectID:   getEnvString("BRAINTRUST_DEFAULT_PROJECT_ID", ""),
		DefaultProjectName: getEnvString("BRAINTRUST_DEFAULT_PROJECT", "default-go-project"),
		BlockingLogin:      getEnvBool("BRAINTRUST_BLOCKING_LOGIN", false),
		FilterAISpans:      getEnvBool("BRAINTRUST_OTEL_FILTER_AI_SPANS", false),
	}
}

// getEnvString returns the trimmed environment variable value or the default
func getEnvString(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return strings.TrimSpace(value)
	}
	return defaultValue
}

// getEnvBool returns the environment variable as a bool or the default
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(strings.TrimSpace(value)) == "true"
	}
	return defaultValue
}

// IsValid checks if the configuration has all required fields.
// Returns an error if any required field is missing.
func (c *Config) IsValid() error {
	if c.APIKey == "" {
		return fmt.Errorf("API key is required")
	}
	if c.APIURL == "" {
		return fmt.Errorf("API URL is required")
	}
	if c.AppURL == "" {
		return fmt.Errorf("app URL is required")
	}
	return nil
}
