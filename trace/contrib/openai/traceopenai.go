// Package openai provides OpenTelemetry middleware for tracing OpenAI API calls.
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
// Then add the middleware to your OpenAI client:
//
//	client := openai.NewClient(
//		option.WithMiddleware(openai.NewMiddleware()),
//	)
//
// For tests or custom configurations, you can provide a TracerProvider:
//
//	middleware := openai.NewMiddleware(openai.WithTracerProvider(tp))
//	client := openai.NewClient(option.WithMiddleware(middleware))
//
//	// Your OpenAI calls will now be automatically traced
//	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
//		Model: openai.F(openai.ChatModelGPT4),
//		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
//			openai.UserMessage("Hello!"),
//		}),
//	})
package openai

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// NextMiddleware represents the next middleware to run in the OpenAI client middleware chain.
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

// NewMiddleware creates a new OpenTelemetry tracing middleware for OpenAI client requests.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	middleware := openai.NewMiddleware()
//	client := openai.NewClient(option.WithMiddleware(middleware))
func NewMiddleware(opts ...MiddlewareOption) func(*http.Request, NextMiddleware) (*http.Response, error) {
	cfg := &middlewareConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	router := func(path string) internal.MiddlewareTracer {
		return openaiRouter(cfg, path)
	}

	return internal.Middleware(router, cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by SDK
}

func openaiRouter(cfg *middlewareConfig, path string) internal.MiddlewareTracer {

	// we map suffix => tracer because some OpenAI compatible endpoints have a different BaseURL and
	// therefore a different path here. For example:
	// 	- OpenAI has /v1/chat/completions
	//  - OpenRouter has /api/v1/chat/completions.
	// See https://github.com/braintrustdata/braintrust-sdk-go/issues/36
	if strings.HasSuffix(path, "/v1/chat/completions") {
		return newChatCompletionsTracer(cfg)
	}

	if strings.HasSuffix(path, "/v1/responses") {
		return newResponsesTracer(cfg)
	}

	return nil
}

// parseUsageTokens parses the usage tokens from OpenAI API responses
// It handles different API formats using a unified approach
func parseUsageTokens(usage map[string]interface{}) map[string]int64 {
	metrics := make(map[string]int64)

	if usage == nil {
		return metrics
	}

	// Parse token metrics and translate names to be consistent
	for k, v := range usage {
		if strings.HasSuffix(k, "_tokens_details") {
			prefix := translateMetricPrefix(strings.TrimSuffix(k, "_tokens_details"))
			if details, ok := v.(map[string]interface{}); ok {
				for kd, vd := range details {
					if ok, i := internal.ToInt64(vd); ok {
						metrics[prefix+"_"+kd] = i
					}
				}
			}
		} else {
			if ok, i := internal.ToInt64(v); ok {
				switch k {
				case "input_tokens":
					metrics["prompt_tokens"] = i
				case "output_tokens":
					metrics["completion_tokens"] = i
				case "total_tokens":
					metrics["tokens"] = i
				default:
					// Keep other fields as-is (future-proofing for new OpenAI fields)
					metrics[k] = i
				}
			}
		}
	}

	return metrics
}

// translateMetricPrefix translates metric prefixes to be consistent between APIs
func translateMetricPrefix(prefix string) string {
	switch prefix {
	case "input":
		return "prompt"
	case "output":
		return "completion"
	default:
		return prefix
	}
}

// Ensure our tracers implement the shared interface
var _ internal.MiddlewareTracer = &responsesTracer{}
var _ internal.MiddlewareTracer = &chatCompletionsTracer{}
