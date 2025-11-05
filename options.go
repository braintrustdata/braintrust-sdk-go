package braintrust

import (
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Option is a functional option for configuring a Braintrust client
type Option func(*config.Config)

// WithAPIKey sets the API key (overrides BRAINTRUST_API_KEY)
func WithAPIKey(apiKey string) Option {
	return func(c *config.Config) {
		c.APIKey = apiKey
	}
}

// WithAPIURL sets the API URL (overrides BRAINTRUST_API_URL)
func WithAPIURL(apiURL string) Option {
	return func(c *config.Config) {
		c.APIURL = apiURL
	}
}

// WithAppURL sets the app URL (overrides BRAINTRUST_APP_URL)
func WithAppURL(appURL string) Option {
	return func(c *config.Config) {
		c.AppURL = appURL
	}
}

// WithOrgName sets the organization name (overrides BRAINTRUST_ORG_NAME)
func WithOrgName(orgName string) Option {
	return func(c *config.Config) {
		c.OrgName = orgName
	}
}

// WithProject sets the default project name (overrides BRAINTRUST_DEFAULT_PROJECT)
func WithProject(projectName string) Option {
	return func(c *config.Config) {
		c.DefaultProjectName = projectName
	}
}

// WithProjectID sets the default project ID (overrides BRAINTRUST_DEFAULT_PROJECT_ID)
func WithProjectID(projectID string) Option {
	return func(c *config.Config) {
		c.DefaultProjectID = projectID
	}
}

// WithLogger sets a custom logger for the SDK
// If not provided, a default logger will be used
func WithLogger(l logger.Logger) Option {
	return func(c *config.Config) {
		c.Logger = l
	}
}

// WithBlockingLogin enables synchronous login
// By default, login happens asynchronously in the background
// Set to true for tests or scripts where you need login to complete before proceeding
func WithBlockingLogin(enabled bool) Option {
	return func(c *config.Config) {
		c.BlockingLogin = enabled
	}
}

// WithExporter injects a custom OpenTelemetry SpanExporter
// If not provided, an OTLP HTTP exporter will be created automatically
// This is primarily useful for testing with a memory exporter
func WithExporter(exporter trace.SpanExporter) Option {
	return func(c *config.Config) {
		c.Exporter = exporter
	}
}

// WithFilterAISpans enables filtering to keep only AI-related spans
// When enabled, only spans with AI-related names or attributes will be sent
func WithFilterAISpans(enabled bool) Option {
	return func(c *config.Config) {
		c.FilterAISpans = enabled
	}
}

// WithSpanFilterFuncs adds custom span filter functions
// Filters are evaluated in order. Return >0 to keep, <0 to drop, 0 to continue
func WithSpanFilterFuncs(filterFuncs ...config.SpanFilterFunc) Option {
	return func(c *config.Config) {
		c.SpanFilterFuncs = append(c.SpanFilterFuncs, filterFuncs...)
	}
}
