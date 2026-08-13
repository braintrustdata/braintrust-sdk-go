// Package anthropic provides OpenTelemetry middleware for tracing Anthropic API calls.
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
// Then add the middleware to your Anthropic client:
//
//	client := anthropic.NewClient(
//		option.WithMiddleware(anthropic.NewMiddleware()),
//	)
//
// For tests or custom configurations, you can provide a TracerProvider:
//
//	middleware := anthropic.NewMiddleware(anthropic.WithTracerProvider(tp))
//	client := anthropic.NewClient(option.WithMiddleware(middleware))
//
//	// Your Anthropic calls will now be automatically traced
//	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
//		Model: anthropic.ModelClaudeHaiku4_5,
//		Messages: []anthropic.MessageParam{
//			anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
//		},
//		MaxTokens: 1024,
//	})
package anthropic

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// NextMiddleware represents the next middleware to run in the OpenAI client middleware chain
type NextMiddleware = internal.NextMiddleware

// middlewareConfig holds configuration for the middleware
type middlewareConfig struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// MiddlewareOption configures the middleware
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

// tracer returns the configured tracer
func (c *middlewareConfig) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// NewMiddleware creates a new OpenTelemetry tracing middleware for Anthropic client requests.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	middleware := anthropic.NewMiddleware()
//	client := anthropic.NewClient(option.WithMiddleware(middleware))
func NewMiddleware(opts ...MiddlewareOption) func(*http.Request, NextMiddleware) (*http.Response, error) {
	cfg := &middlewareConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	router := func(path string) internal.MiddlewareTracer {
		return anthropicRouter(cfg, path)
	}

	return internal.Middleware(router, cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by SDK
}

// anthropicRouter maps Anthropic API paths to their corresponding tracers.
func anthropicRouter(cfg *middlewareConfig, path string) internal.MiddlewareTracer {
	if strings.HasSuffix(path, "/v1/messages") {
		return newMessagesTracer(cfg)
	}
	return nil
}

// parseUsageTokens normalizes the Anthropic usage response to the metric names
// permitted by the Braintrust instrumentation specification. Missing or
// invalid values are omitted rather than fabricated.
func parseUsageTokens(usage map[string]interface{}) map[string]int64 {
	metrics := make(map[string]int64)
	if usage == nil {
		return metrics
	}

	inputTokens, hasInput := nonNegativeInt64(usage["input_tokens"])
	outputTokens, hasOutput := nonNegativeInt64(usage["output_tokens"])
	cacheCreationTokens, hasCacheCreation := nonNegativeInt64(usage["cache_creation_input_tokens"])
	cacheReadTokens, hasCacheRead := nonNegativeInt64(usage["cache_read_input_tokens"])

	if hasOutput {
		metrics["completion_tokens"] = outputTokens
	}
	if hasCacheRead {
		metrics["prompt_cached_tokens"] = cacheReadTokens
	}

	// Newer Anthropic responses break cache creation down by TTL. Prefer those
	// explicit buckets over the aggregate metric when they are present.
	var cacheCreation5m, cacheCreation1h int64
	var hasCacheCreation5m, hasCacheCreation1h bool
	if cacheCreation, ok := usage["cache_creation"].(map[string]interface{}); ok {
		cacheCreation5m, hasCacheCreation5m = nonNegativeInt64(cacheCreation["ephemeral_5m_input_tokens"])
		cacheCreation1h, hasCacheCreation1h = nonNegativeInt64(cacheCreation["ephemeral_1h_input_tokens"])
	}
	if hasCacheCreation5m {
		metrics["prompt_cache_creation_5m_tokens"] = cacheCreation5m
	}
	if hasCacheCreation1h {
		metrics["prompt_cache_creation_1h_tokens"] = cacheCreation1h
	}
	if hasCacheCreation && !hasCacheCreation5m && !hasCacheCreation1h {
		metrics["prompt_cache_creation_tokens"] = cacheCreationTokens
	}

	cacheCreationForPrompt := cacheCreationTokens
	if !hasCacheCreation {
		cacheCreationForPrompt = cacheCreation5m + cacheCreation1h
	}
	if hasInput || hasCacheCreation || hasCacheCreation5m || hasCacheCreation1h || hasCacheRead {
		promptTokens := inputTokens + cacheCreationForPrompt + cacheReadTokens
		metrics["prompt_tokens"] = promptTokens
		if hasOutput {
			metrics["tokens"] = promptTokens + outputTokens
		}
	}
	if totalTokens, ok := nonNegativeInt64(usage["total_tokens"]); ok {
		metrics["tokens"] = totalTokens
	}

	return metrics
}

func nonNegativeInt64(value interface{}) (int64, bool) {
	ok, integer := internal.ToInt64(value)
	return integer, ok && integer >= 0
}

// Ensure our tracers implement the shared interface
var _ internal.MiddlewareTracer = &messagesTracer{}
