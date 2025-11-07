// Package traceopenai provides OpenTelemetry tracing for github.com/sashabaranov/go-openai client.
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
// Then create your OpenAI client with a traced HTTP client:
//
//	config := openai.DefaultConfig(apiKey)
//	config.HTTPClient = traceopenai.Client()
//	client := openai.NewClientWithConfig(config)
//
// For tests or custom configurations, you can provide a TracerProvider:
//
//	httpClient := traceopenai.Client(traceopenai.WithTracerProvider(tp))
//	config := openai.DefaultConfig(apiKey)
//	config.HTTPClient = httpClient
//	client := openai.NewClientWithConfig(config)
//
//	// Your OpenAI calls will now be automatically traced
//	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
//		Model: openai.GPT4,
//		Messages: []openai.ChatCompletionMessage{
//			{
//				Role:    openai.ChatMessageRoleUser,
//				Content: "Hello!",
//			},
//		},
//	})
package traceopenai

import (
	"net/http"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

// config holds configuration for the HTTP client wrapper
type config struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// Option configures the HTTP client wrapper
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

// Client returns a new http.Client configured with tracing middleware.
// This is equivalent to WrapClient(nil), which wraps the default HTTP transport.
//
// Example:
//
//	config := openai.DefaultConfig(apiKey)
//	config.HTTPClient = traceopenai.Client()
//	client := openai.NewClientWithConfig(config)
func Client(opts ...Option) *http.Client {
	return WrapClient(nil, opts...)
}

// WrapClient wraps an existing http.Client with tracing middleware.
// If client is nil, a new client with the default transport is created.
//
// Example:
//
//	existingClient := &http.Client{Timeout: 30 * time.Second}
//	config := openai.DefaultConfig(apiKey)
//	config.HTTPClient = traceopenai.WrapClient(existingClient)
//	client := openai.NewClientWithConfig(config)
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
	// Convert our config options to openai middleware options
	var middlewareOpts []openai.MiddlewareOption
	if rt.cfg.tracerProvider != nil {
		middlewareOpts = append(middlewareOpts, openai.WithTracerProvider(rt.cfg.tracerProvider))
	}
	if rt.cfg.logger != nil {
		middlewareOpts = append(middlewareOpts, openai.WithLogger(rt.cfg.logger))
	}

	// Use the existing openai middleware
	middleware := openai.NewMiddleware(middlewareOpts...)

	// Create a NextMiddleware function that calls the base transport
	next := func(r *http.Request) (*http.Response, error) {
		return rt.base.RoundTrip(r)
	}

	return middleware(req, next)
}
