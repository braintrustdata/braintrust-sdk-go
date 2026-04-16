// Package genai provides OpenTelemetry tracing for Google Gemini API calls.
//
// First, set up tracing with braintrust.New():
//
//	tp := trace.NewTracerProvider()
//	defer tp.Shutdown(context.Background())
//	otel.SetTracerProvider(tp)
//
//	bt, err := braintrust.New(tp,
//		braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//
// Then create your Gemini client with tracing:
//
//	client, err := genai.NewClient(ctx, &genai.ClientConfig{
//		HTTPClient: genai.WrapClient(nil),
//		APIKey:     apiKey,
//		Backend:    genai.BackendGeminiAPI,
//	})
//
// For tests or custom configurations, you can provide a TracerProvider:
//
//	httpClient := genai.WrapClient(nil, genai.WithTracerProvider(tp))
//
//	// Your Gemini calls will now be automatically traced
//	resp, err := client.Models.GenerateContent(ctx, "gemini-2.0-flash-exp",
//		genai.Text("Hello!"), nil)
package genai

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// config holds configuration for the HTTP client wrapper
type config struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// Option configures the genai HTTP client wrapper
type Option func(*config)

// WithTracerProvider sets a custom TracerProvider for the HTTP client wrapper.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) {
		c.tracerProvider = tp
	}
}

// WithLogger sets a custom logger for the HTTP client wrapper.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) Option {
	return func(c *config) {
		c.logger = log
	}
}

// tracer returns the configured tracer
func (c *config) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// Client returns a new http.Client configured with tracing middleware.
// This is equivalent to WrapClient(nil), which wraps the default HTTP transport.
//
// Example:
//
//	httpClient := genai.Client()
func Client(opts ...Option) *http.Client {
	return WrapClient(nil, opts...)
}

// WrapClient wraps an existing http.Client with tracing middleware.
// If client is nil, a new client with the default transport is created.
//
// Example:
//
//	httpClient := genai.WrapClient(existingClient)
func WrapClient(client *http.Client, opts ...Option) *http.Client {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if client == nil {
		client = &http.Client{}
	}

	// Get the existing transport or use default
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	// Wrap with our tracing RoundTripper
	client.Transport = newRoundTripper(transport, cfg)
	return client
}

// roundTripper wraps an http.RoundTripper with OpenTelemetry tracing.
type roundTripper struct {
	base http.RoundTripper
	cfg  *config
}

// newRoundTripper creates a new tracing RoundTripper that wraps the base transport.
func newRoundTripper(base http.RoundTripper, cfg *config) http.RoundTripper {
	return &roundTripper{base: base, cfg: cfg}
}

// RoundTrip implements http.RoundTripper by intercepting requests and responses.
func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Use the internal middleware infrastructure
	router := func(path string) internal.MiddlewareTracer {
		return genaiRouter(rt.cfg, path)
	}
	middleware := internal.Middleware(router, rt.cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by caller

	// Create a NextMiddleware function that calls the base transport
	next := func(r *http.Request) (*http.Response, error) {
		return rt.base.RoundTrip(r)
	}

	return middleware(req, next)
}

// genaiRouter maps Gemini API paths to their corresponding tracers.
func genaiRouter(cfg *config, path string) internal.MiddlewareTracer {
	// Match both Gemini API and Vertex AI paths for generateContent and streamGenerateContent
	// Gemini API: /v1beta/models/{model}:generateContent or :streamGenerateContent
	// Vertex AI: /v1/projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent
	if containsGenerateContent(path) {
		model := extractModelFromPath(path)
		streaming := isStreamingPath(path)
		return newGenerateContentTracer(cfg, model, streaming)
	}
	return nil
}

// containsGenerateContent checks if the path is for a generateContent or streamGenerateContent endpoint.
// We require a delimiter (: or /) before the method name to avoid matching unrelated endpoints.
func containsGenerateContent(path string) bool {
	return strings.Contains(path, ":generateContent") ||
		strings.Contains(path, "/generateContent") ||
		strings.Contains(path, ":streamGenerateContent") ||
		strings.Contains(path, "/streamGenerateContent")
}

// isStreamingPath checks if the path is for the streaming endpoint
func isStreamingPath(path string) bool {
	return strings.Contains(path, "streamGenerateContent") ||
		strings.Contains(path, "StreamGenerateContent")
}

// extractModelFromPath extracts the model name from the URL path
// Gemini API: /v1beta/models/{model}/generateContent or /v1beta/models/{model}:generateContent
// Vertex AI: /v1/projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent
func extractModelFromPath(path string) string {
	// Split by /models/ to get the part after it
	parts := strings.Split(path, "/models/")
	if len(parts) < 2 {
		return ""
	}

	// Get the model part (after /models/)
	modelPart := parts[1]

	// Remove the endpoint suffix (handles both streaming and non-streaming)
	for _, suffix := range []string{
		":streamGenerateContent",
		"/streamGenerateContent",
		":generateContent",
		"/generateContent",
	} {
		modelPart = strings.TrimSuffix(modelPart, suffix)
	}

	return modelPart
}
