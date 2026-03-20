// Package genkit provides OpenTelemetry tracing middleware for Firebase Genkit.
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
// Then use the middleware with genkit.Generate():
//
//	resp, err := genkit.Generate(ctx, g,
//		ai.WithModel(model),
//		ai.WithPrompt("Hello!"),
//		ai.WithMiddleware(tracegenkit.NewMiddleware()),
//	)
package genkit

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

var globalMiddleware = NewMiddleware()

// Option configures the Genkit tracing middleware.
type Option func(*middlewareConfig)

// WithTracerProvider sets a custom TracerProvider for the middleware.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(cfg *middlewareConfig) {
		cfg.tracer = tp.Tracer("braintrust")
	}
}

// WithLogger sets a custom logger for the middleware.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) Option {
	return func(cfg *middlewareConfig) {
		if log != nil {
			cfg.logger = log
		}
	}
}

type middlewareConfig struct {
	logger logger.Logger
	tracer trace.Tracer
}

// NewMiddleware creates a new Genkit ModelMiddleware that traces model calls with
// Braintrust-compatible OpenTelemetry spans.
//
// Example:
//
//	resp, err := genkit.Generate(ctx, g,
//		ai.WithModel(model),
//		ai.WithPrompt("Hello!"),
//		ai.WithMiddleware(tracegenkit.NewMiddleware()),
//	)
func NewMiddleware(opts ...Option) ai.ModelMiddleware {
	cfg := &middlewareConfig{
		logger: logger.Discard(),
		tracer: otel.GetTracerProvider().Tracer("braintrust"),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next ai.ModelFunc) ai.ModelFunc {
		return func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return traceGenerate(ctx, cfg, next, req, cb)
		}
	}
}

func traceGenerate(ctx context.Context, cfg *middlewareConfig, next ai.ModelFunc, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	ctx, span := cfg.tracer.Start(ctx, "genkit.generate",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// Set span type
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		cfg.logger.Debug("Failed to set braintrust.span_attributes", "error", err)
	}

	// Set input
	if err := internal.SetJSONAttr(span, "braintrust.input_json", cleanupForInput(req)); err != nil {
		cfg.logger.Debug("Failed to set braintrust.input_json", "error", err)
	}

	// Extract config metadata
	metadata := extractMetadata(req)

	// Wrap streaming callback for TTFT
	var ttft time.Duration
	wrappedCB := cb
	if cb != nil {
		var once sync.Once
		startTime := time.Now()
		wrappedCB = func(ctx context.Context, chunk *ai.ModelResponseChunk) error {
			once.Do(func() {
				ttft = time.Since(startTime)
			})
			return cb(ctx, chunk)
		}
	}

	// Call the actual model
	resp, err := next(ctx, req, wrappedCB)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	// Set output
	if resp != nil {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", cleanupForOutput(resp)); err != nil {
			cfg.logger.Debug("Failed to set braintrust.output_json", "error", err)
		}

		// Set metrics from usage
		metrics := extractMetrics(resp, ttft)
		if len(metrics) > 0 {
			if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
				cfg.logger.Debug("Failed to set braintrust.metrics", "error", err)
			}
		}
	}

	// Set metadata
	if len(metadata) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.metadata", metadata); err != nil {
			cfg.logger.Debug("Failed to set braintrust.metadata", "error", err)
		}
	}

	return resp, nil
}

// cleanupForInput creates a clean input representation from the ModelRequest.
func cleanupForInput(req *ai.ModelRequest) any {
	if req == nil {
		return nil
	}
	// Marshal and unmarshal to get a clean map without unexported fields
	data, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return cleanupJSON(result)
}

// cleanupForOutput creates a clean output representation from the ModelResponse.
func cleanupForOutput(resp *ai.ModelResponse) any {
	if resp == nil {
		return nil
	}
	// Build a focused output representation
	out := make(map[string]any)
	if resp.Message != nil {
		data, err := json.Marshal(resp.Message)
		if err == nil {
			var msg any
			if json.Unmarshal(data, &msg) == nil {
				out["message"] = msg
			}
		}
	}
	if resp.FinishReason != "" {
		out["finishReason"] = resp.FinishReason
	}
	if resp.FinishMessage != "" {
		out["finishMessage"] = resp.FinishMessage
	}
	cleaned := cleanupJSON(out)
	if cleaned == nil {
		return map[string]any{}
	}
	return cleaned
}

// extractMetrics extracts token usage metrics from the response.
func extractMetrics(resp *ai.ModelResponse, ttft time.Duration) map[string]float64 {
	metrics := make(map[string]float64)

	if resp.Usage != nil {
		if resp.Usage.InputTokens > 0 {
			metrics["prompt_tokens"] = float64(resp.Usage.InputTokens)
		}
		if resp.Usage.OutputTokens > 0 {
			metrics["completion_tokens"] = float64(resp.Usage.OutputTokens)
		}
		if resp.Usage.TotalTokens > 0 {
			metrics["tokens"] = float64(resp.Usage.TotalTokens)
		}
		if resp.Usage.CachedContentTokens > 0 {
			metrics["prompt_cached_tokens"] = float64(resp.Usage.CachedContentTokens)
		}
		if resp.Usage.ThoughtsTokens > 0 {
			metrics["completion_reasoning_tokens"] = float64(resp.Usage.ThoughtsTokens)
		}
	}

	if ttft > 0 {
		metrics["time_to_first_token"] = ttft.Seconds()
	}

	return metrics
}

// extractMetadata extracts model configuration metadata from the request.
func extractMetadata(req *ai.ModelRequest) map[string]any {
	metadata := map[string]any{
		"provider": "genkit",
	}

	if req == nil || req.Config == nil {
		return metadata
	}

	// Try to extract config as GenerationCommonConfig
	var configMap map[string]any
	data, err := json.Marshal(req.Config)
	if err != nil {
		return metadata
	}
	if err := json.Unmarshal(data, &configMap); err != nil {
		return metadata
	}

	if v, ok := configMap["temperature"]; ok && v != nil {
		if f, ok := toFloat(v); ok && f != 0 {
			metadata["temperature"] = f
		}
	}
	if v, ok := configMap["maxOutputTokens"]; ok && v != nil {
		if f, ok := toFloat(v); ok && f != 0 {
			metadata["maxOutputTokens"] = int(f)
		}
	}
	if v, ok := configMap["topP"]; ok && v != nil {
		if f, ok := toFloat(v); ok && f != 0 {
			metadata["topP"] = f
		}
	}
	if v, ok := configMap["topK"]; ok && v != nil {
		if f, ok := toFloat(v); ok && f != 0 {
			metadata["topK"] = int(f)
		}
	}
	if v, ok := configMap["model"]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			metadata["model"] = s
		}
	}
	if v, ok := configMap["version"]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			metadata["version"] = s
		}
	}

	return metadata
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// cleanupJSON recursively removes keys with empty values from JSON structures.
// Empty values include: nil, empty strings, empty slices, and empty maps.
func cleanupJSON(value any) any {
	switch val := value.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			cleaned := cleanupJSON(v)
			if !isEmpty(cleaned) {
				result[k] = cleaned
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []any:
		result := make([]any, 0, len(val))
		for _, item := range val {
			result = append(result, cleanupJSON(item))
		}
		return result
	default:
		return val
	}
}

// isEmpty checks if a value is considered empty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return val == ""
	case []any:
		return len(val) == 0
	case map[string]any:
		return len(val) == 0
	default:
		return false
	}
}
