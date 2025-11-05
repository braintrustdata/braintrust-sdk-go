package genai

// this file parses the generateContent API.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// generateContentTracer is a tracer for the Gemini generateContent endpoint.
type generateContentTracer struct {
	cfg       *config
	streaming bool
	metadata  map[string]any
	model     string
}

func newGenerateContentTracer(cfg *config, model string) *generateContentTracer {
	return &generateContentTracer{
		cfg:       cfg,
		streaming: false,
		model:     model,
		metadata: map[string]any{
			"provider": "gemini",
		},
	}
}

func (gt *generateContentTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := gt.cfg.tracer().Start(
		ctx,
		"genai.models.generateContent",
		trace.WithTimestamp(t),
	)

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	// Extract metadata fields from request
	metadataFields := []string{
		"model",
		"systemInstruction",
		"tools",
		"toolConfig",
		"safetySettings",
		"cachedContent",
	}

	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			gt.metadata[field] = value
		}
	}

	// Handle generationConfig
	if genConfig, ok := raw["generationConfig"].(map[string]any); ok {
		configFields := []string{
			"temperature",
			"topP",
			"topK",
			"candidateCount",
			"maxOutputTokens",
			"stopSequences",
			"responseMimeType",
			"responseSchema",
		}
		for _, field := range configFields {
			if value, exists := genConfig[field]; exists {
				gt.metadata[field] = value
			}
		}
	}

	// Log the raw request format
	inputLog := make(map[string]any)

	// Add model from URL path (or from body if present)
	if model, ok := raw["model"].(string); ok {
		inputLog["model"] = model
	} else if gt.model != "" {
		inputLog["model"] = gt.model
	}

	// Add contents as-is
	if contents, ok := raw["contents"]; ok {
		inputLog["contents"] = contents
	}

	// Add generationConfig as config
	if genConfig, ok := raw["generationConfig"]; ok {
		inputLog["config"] = genConfig
	}

	if len(inputLog) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", inputLog); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", gt.metadata); err != nil {
		return ctx, span, err
	}

	// Set span attributes to mark this as an LLM span
	spanAttrs := map[string]string{
		"type": "llm",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (gt *generateContentTracer) TagSpan(span trace.Span, body io.Reader) error {
	// For now, handle non-streaming responses
	// Streaming will be handled separately
	return gt.parseResponse(span, body)
}

func (gt *generateContentTracer) parseResponse(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	err := json.NewDecoder(body).Decode(&raw)
	if err != nil {
		return err
	}

	return gt.handleResponse(span, raw)
}

func (gt *generateContentTracer) handleResponse(span trace.Span, raw map[string]any) error {
	// Extract model version if present
	if modelVersion, ok := raw["modelVersion"].(string); ok {
		gt.metadata["model"] = modelVersion
	}

	// Update metadata
	if err := internal.SetJSONAttr(span, "braintrust.metadata", gt.metadata); err != nil {
		return err
	}

	// Log the raw response format
	if err := internal.SetJSONAttr(span, "braintrust.output_json", raw); err != nil {
		return err
	}

	// Parse usage metadata (token counts)
	if usageMetadata, ok := raw["usageMetadata"].(map[string]any); ok {
		metrics := parseUsageTokens(usageMetadata)
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			return err
		}
	}

	return nil
}

// parseUsageTokens parses the usage tokens from Gemini API responses
func parseUsageTokens(usage map[string]interface{}) map[string]int64 {
	metrics := make(map[string]int64)

	if usage == nil {
		return metrics
	}

	for k, v := range usage {
		if ok, i := internal.ToInt64(v); ok {
			switch k {
			case "promptTokenCount":
				metrics["prompt_tokens"] = i
			case "candidatesTokenCount":
				metrics["completion_tokens"] = i
			case "totalTokenCount":
				metrics["tokens"] = i
			case "cachedContentTokenCount":
				metrics["prompt_cached_tokens"] = i
			default:
				// Keep other fields as-is for future-proofing
				// Convert camelCase to snake_case for consistency
				snakeKey := camelToSnake(k)
				metrics[snakeKey] = i
			}
		}
	}

	return metrics
}

// camelToSnake converts camelCase to snake_case
func camelToSnake(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// Ensure our tracer implements the shared interface
var _ internal.MiddlewareTracer = &generateContentTracer{}
