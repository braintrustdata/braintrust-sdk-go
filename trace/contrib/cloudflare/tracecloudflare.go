// Package cloudflare provides OpenTelemetry tracing for Cloudflare Workers AI
// calls using the official Go SDK (github.com/cloudflare/cloudflare-go/v7).
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
// Then add the middleware to your Cloudflare client:
//
//	client := cloudflare.NewClient(
//		option.WithMiddleware(tracecloudflare.NewMiddleware()),
//	)
package cloudflare

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// NextMiddleware represents the next middleware to run in the Cloudflare client middleware chain.
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

// NewMiddleware creates a new OpenTelemetry tracing middleware for Cloudflare Workers AI requests.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	middleware := cloudflare.NewMiddleware()
//	client := cloudflare.NewClient(option.WithMiddleware(middleware))
func NewMiddleware(opts ...MiddlewareOption) func(*http.Request, NextMiddleware) (*http.Response, error) {
	cfg := &middlewareConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	router := func(path string) internal.MiddlewareTracer {
		return cloudflareRouter(cfg, path)
	}

	return internal.Middleware(router, cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by SDK
}

// runPathMarker is the fixed segment of Workers AI's URL:
// accounts/{account_id}/ai/run/{model_name}. Everything after it is the
// model name, which itself often contains slashes (e.g. "@cf/meta/llama-3.1").
const runPathMarker = "/ai/run/"

// cloudflareRouter dispatches AI.Run requests by task, since Workers AI
// exposes every task (text generation, embeddings, etc.) through the same
// endpoint and only the request body shape tells them apart.
func cloudflareRouter(cfg *middlewareConfig, path string) internal.MiddlewareTracer {
	_, model, ok := strings.Cut(path, runPathMarker)
	if !ok {
		return nil
	}
	return newAIRunTracer(cfg, model)
}
