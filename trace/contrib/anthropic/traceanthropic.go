// Package anthropic provides OpenTelemetry middleware for tracing Anthropic API calls.
//
// First, set up tracing with braintrust.New():
//
//	bt, err := braintrust.New(
//		braintrust.WithAPIKey(os.Getenv("BRAINTRUST_API_KEY")),
//		braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer bt.Close()
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
//		Model: anthropic.ModelClaude3_7SonnetLatest,
//		Messages: []anthropic.MessageParam{
//			anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
//		},
//		MaxTokens: 1024,
//	})
package anthropic

import (
	"net/http"

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
	if path == "/v1/messages" {
		return newMessagesTracer(cfg)
	}
	return nil
}

// parseUsageTokens parses the usage tokens from Anthropic API responses
// It handles Anthropic-specific fields including cache tokens
func parseUsageTokens(usage map[string]interface{}) map[string]int64 {
	metrics := make(map[string]int64)

	if usage == nil {
		return metrics
	}

	var inputTokens, cacheCreationTokens, cacheReadTokens int64

	// Single pass: process all tokens
	for k, v := range usage {
		if ok, i := internal.ToInt64(v); ok {
			switch k {
			case "input_tokens":
				inputTokens = i
			case "cache_creation_input_tokens":
				cacheCreationTokens = i
				metrics["prompt_cache_creation_tokens"] = i
			case "cache_read_input_tokens":
				cacheReadTokens = i
				metrics["prompt_cached_tokens"] = i
			case "output_tokens":
				metrics["completion_tokens"] = i
			case "total_tokens":
				metrics["tokens"] = i
			default:
				// Keep other fields as-is (future-proofing for new Anthropic fields)
				metrics[k] = i
			}
		}
	}

	// Calculate total prompt tokens (input + cache tokens)
	totalPromptTokens := inputTokens + cacheCreationTokens + cacheReadTokens
	metrics["prompt_tokens"] = totalPromptTokens

	// Calculate total tokens if not provided by Anthropic
	if _, hasTokens := metrics["tokens"]; !hasTokens {
		if completionTokens, hasCompletion := metrics["completion_tokens"]; hasCompletion {
			metrics["tokens"] = totalPromptTokens + completionTokens
		}
	}

	return metrics
}

// Ensure our tracers implement the shared interface
var _ internal.MiddlewareTracer = &messagesTracer{}
