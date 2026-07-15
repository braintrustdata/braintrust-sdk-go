// Package config provides configuration management for the Braintrust SDK.
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/apikey"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// APIKeyResolver resolves a Braintrust API key when one is not provided
// explicitly or through the process environment.
type APIKeyResolver interface {
	APIKey(context.Context) (string, bool)
}

// Config holds immutable configuration for the Braintrust SDK.
type Config struct {
	APIKey             string
	APIKeyResolver     APIKeyResolver
	APIURL             string
	AppURL             string
	OrgName            string
	DefaultProjectID   string
	DefaultProjectName string
	BlockingLogin      bool

	// Tracing configuration
	FilterAISpans            bool
	EnableBuiltinAdkTraces   bool // if false (default), drop Google ADK native spans to avoid duplicates
	EnableTraceConsoleLog    bool // log traces to stdout for debugging
	AutoConvertAIAttachments bool // scan spans for base64 attachments and upload them (default: true)
	SpanFilterFuncs          []SpanFilterFunc
	Exporter                 trace.SpanExporter
	Environment              *Environment

	// Logger
	Logger logger.Logger
}

// Environment describes where spans are produced for span-origin provenance.
type Environment struct {
	Type string
	Name string
}

// SpanFilterFunc is a function that decides which spans to send to Braintrust.
// Return >0 to keep the span, <0 to drop the span, or 0 to not influence the decision.
type SpanFilterFunc func(span trace.ReadOnlySpan) int

// FromEnv loads configuration from environment variables with defaults. If
// BRAINTRUST_API_KEY is unset or blank, API key discovery can fall back to the
// nearest .braintrust.json file when authentication or export first needs it.
//
// Supported environment variables:
//   - BRAINTRUST_API_KEY: API key for authentication
//   - BRAINTRUST_API_URL: API endpoint URL (default: "https://api.braintrust.dev")
//   - BRAINTRUST_APP_URL: Application URL (default: "https://www.braintrust.dev")
//   - BRAINTRUST_ORG_NAME: Organization name
//   - BRAINTRUST_DEFAULT_PROJECT_ID: Default project ID
//   - BRAINTRUST_DEFAULT_PROJECT: Default project name (default: "default-go-project")
//   - BRAINTRUST_BLOCKING_LOGIN: Enable blocking login (default: false)
//   - BRAINTRUST_ENABLE_TRACE_CONSOLE_LOG: Log traces to stdout for debugging (default: false)
//   - BRAINTRUST_OTEL_FILTER_AI_SPANS: Filter to keep only AI-related spans (default: false)
//   - BRAINTRUST_OTEL_ENABLE_BUILTIN_ADK_TRACES: Enable exporting spans from Google ADK's built-in telemetry (default: false)
//   - BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS: Scan spans for base64 attachments and upload them (default: true)
func FromEnv() *Config {
	apiKey := getEnvString("BRAINTRUST_API_KEY", "")
	cfg := &Config{
		APIKey:                   apiKey,
		APIURL:                   getEnvString("BRAINTRUST_API_URL", "https://api.braintrust.dev"),
		AppURL:                   getEnvString("BRAINTRUST_APP_URL", "https://www.braintrust.dev"),
		OrgName:                  getEnvString("BRAINTRUST_ORG_NAME", ""),
		DefaultProjectID:         getEnvString("BRAINTRUST_DEFAULT_PROJECT_ID", ""),
		DefaultProjectName:       getEnvString("BRAINTRUST_DEFAULT_PROJECT", "default-go-project"),
		BlockingLogin:            getEnvBool("BRAINTRUST_BLOCKING_LOGIN", false),
		FilterAISpans:            getEnvBool("BRAINTRUST_OTEL_FILTER_AI_SPANS", false),
		EnableTraceConsoleLog:    getEnvBool("BRAINTRUST_ENABLE_TRACE_CONSOLE_LOG", false),
		EnableBuiltinAdkTraces:   getEnvBool("BRAINTRUST_OTEL_ENABLE_BUILTIN_ADK_TRACES", false),
		AutoConvertAIAttachments: getEnvBool("BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS", true),
		Environment:              DetectEnvironment(nil),
	}
	if cfg.APIKey == "" {
		cfg.APIKeyResolver = apikey.NewResolver()
	}
	return cfg
}

// DetectEnvironment resolves span-origin environment provenance from an
// explicit value, Braintrust environment variables, CI, and server runtimes.
func DetectEnvironment(explicit *Environment) *Environment {
	if explicit != nil {
		return explicit
	}
	envType := getEnvString("BRAINTRUST_ENVIRONMENT_TYPE", "")
	envName := getEnvString("BRAINTRUST_ENVIRONMENT_NAME", "")
	if envType != "" || envName != "" {
		return &Environment{Type: envType, Name: envName}
	}
	for key, name := range map[string]string{
		"GITHUB_ACTIONS": "github_actions", "GITLAB_CI": "gitlab_ci", "CIRCLECI": "circleci",
		"BUILDKITE": "buildkite", "JENKINS_URL": "jenkins", "JENKINS_HOME": "jenkins",
		"TF_BUILD": "azure_pipelines", "TEAMCITY_VERSION": "teamcity", "TRAVIS": "travis",
		"BITBUCKET_BUILD_NUMBER": "bitbucket",
	} {
		if getProcessEnvString(key) != "" {
			return &Environment{Type: "ci", Name: name}
		}
	}
	if getProcessEnvString("CI") != "" {
		return &Environment{Type: "ci", Name: "ci"}
	}
	if name := detectServerEnvironmentName(); name != "" {
		return &Environment{Type: "server", Name: name}
	}
	return nil
}

func detectServerEnvironmentName() string {
	for key, name := range map[string]string{"VERCEL": "vercel", "NETLIFY": "netlify"} {
		if getProcessEnvString(key) != "" {
			return name
		}
	}
	if getProcessEnvString("ECS_CONTAINER_METADATA_URI") != "" || getProcessEnvString("ECS_CONTAINER_METADATA_URI_V4") != "" {
		return "ecs"
	}
	if value := getProcessEnvString("AWS_EXECUTION_ENV"); strings.HasPrefix(value, "AWS_ECS_") {
		return "ecs"
	} else if strings.HasPrefix(value, "AWS_Lambda_") {
		return "aws_lambda"
	}
	if getProcessEnvString("AWS_LAMBDA_FUNCTION_NAME") != "" {
		return "aws_lambda"
	}
	for key, name := range map[string]string{
		"K_SERVICE": "cloud_run", "FUNCTION_TARGET": "gcp_functions", "KUBERNETES_SERVICE_HOST": "kubernetes",
		"DYNO": "heroku", "FLY_APP_NAME": "fly", "RAILWAY_ENVIRONMENT": "railway", "RENDER_SERVICE_NAME": "render",
	} {
		if getProcessEnvString(key) != "" {
			return name
		}
	}
	return ""
}

func getProcessEnvString(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// getEnvString returns the trimmed environment variable value or the default
func getEnvString(key, defaultValue string) string {
	if value := getProcessEnvString(key); value != "" {
		return value
	}
	if value := getBraintrustEnvFileValue(key); value != "" {
		return value
	}
	return defaultValue
}

func getBraintrustEnvFileValue(key string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for depth := 0; depth <= 64; depth++ {
		envPath := filepath.Join(dir, ".env.braintrust")
		if data, err := os.ReadFile(envPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				name, value, ok := strings.Cut(line, "=")
				if !ok || strings.TrimSpace(name) != key {
					continue
				}
				return strings.Trim(strings.TrimSpace(value), `"'`)
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
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
	if c.APIKey == "" && c.APIKeyResolver == nil {
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
