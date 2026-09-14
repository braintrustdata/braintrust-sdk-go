// Package together provides OpenTelemetry tracing for Together AI API calls
// using the official Go SDK (github.com/togethercomputer/together-go).
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
// Then add the middleware to your Together client:
//
//	client := together.NewClient(
//		option.WithMiddleware(tracetogether.NewMiddleware()),
//	)
package together

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// NextMiddleware represents the next middleware to run in the Together client middleware chain.
type NextMiddleware = internal.NextMiddleware

// middlewareConfig holds configuration for the middleware.
type middlewareConfig struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// MiddlewareOption configures the middleware.
type MiddlewareOption func(*middlewareConfig)

// WithTracerProvider sets a custom TracerProvider for the middleware.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.tracerProvider = tp
	}
}

// WithLogger sets a custom logger for the middleware.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.logger = log
	}
}

func (c *middlewareConfig) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// NewMiddleware creates a new OpenTelemetry tracing middleware for Together AI client requests.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	middleware := together.NewMiddleware()
//	client := together.NewClient(option.WithMiddleware(middleware))
func NewMiddleware(opts ...MiddlewareOption) func(*http.Request, NextMiddleware) (*http.Response, error) {
	cfg := &middlewareConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	router := func(path string) internal.MiddlewareTracer {
		return togetherRouter(cfg, path)
	}

	return internal.Middleware(router, cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by SDK
}

func togetherRouter(cfg *middlewareConfig, path string) internal.MiddlewareTracer {
	if strings.HasSuffix(path, "/chat/completions") {
		return newChatCompletionsTracer(cfg)
	}
	if strings.HasSuffix(path, "/completions") {
		return newCompletionsTracer(cfg)
	}
	if strings.HasSuffix(path, "/embeddings") {
		return newEmbeddingsTracer(cfg)
	}
	return nil
}

// parseUsageTokens converts Together AI's usage object (same shape as
// OpenAI's: prompt_tokens/completion_tokens/total_tokens) into Braintrust's
// metric names.
func parseUsageTokens(usage map[string]any) map[string]int64 {
	metrics := map[string]int64{}
	fields := map[string]string{
		"prompt_tokens":     "prompt_tokens",
		"completion_tokens": "completion_tokens",
		"total_tokens":      "tokens",
	}
	for field, metric := range fields {
		if ok, v := internal.ToInt64(usage[field]); ok {
			metrics[metric] = v
		}
	}
	return metrics
}

// Ensure our tracers implement the shared interface.
var _ internal.MiddlewareTracer = &chatCompletionsTracer{}
var _ internal.MiddlewareTracer = &completionsTracer{}
var _ internal.MiddlewareTracer = &embeddingsTracer{}
