package genai

// this file parses the generateContent API.

import (
	"bufio"
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
	startTime time.Time
}

func newGenerateContentTracer(cfg *config, model string, streaming bool) *generateContentTracer {
	return &generateContentTracer{
		cfg:       cfg,
		streaming: streaming,
		model:     model,
		metadata: map[string]any{
			"provider": "gemini",
		},
	}
}

func (gt *generateContentTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	gt.startTime = t
	ctx, span := gt.cfg.tracer().Start(
		ctx,
		"generate_content",
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
	if gt.streaming {
		return gt.parseStreamingResponse(span, body)
	}
	return gt.parseResponse(span, body)
}

func (gt *generateContentTracer) parseStreamingResponse(span trace.Span, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	var allResults []map[string]any
	var timeToFirstToken time.Duration

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		line = strings.TrimPrefix(line, "data: ")
		if line == "[DONE]" {
			break
		}

		if timeToFirstToken == 0 {
			timeToFirstToken = time.Since(gt.startTime)
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return err
		}

		allResults = append(allResults, chunk)
	}

	// Aggregate chunks into a single response
	output := gt.postprocessStreamingResults(allResults)
	if output != nil {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	// Collect usage from the last chunk (Gemini includes usage in the final chunk)
	metrics := make(map[string]any)
	for i := len(allResults) - 1; i >= 0; i-- {
		if usageMetadata, ok := allResults[i]["usageMetadata"].(map[string]any); ok {
			for k, v := range parseUsageTokens(usageMetadata) {
				metrics[k] = v
			}
			break
		}
	}

	// Extract model version from any chunk
	for _, chunk := range allResults {
		if modelVersion, ok := chunk["modelVersion"].(string); ok {
			gt.metadata["model"] = modelVersion
			break
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", gt.metadata); err != nil {
		return err
	}

	metrics["time_to_first_token"] = timeToFirstToken.Seconds()
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	return scanner.Err()
}

// postprocessStreamingResults aggregates streaming chunks into a single response
// matching the non-streaming generateContent response format.
func (gt *generateContentTracer) postprocessStreamingResults(allResults []map[string]any) map[string]any {
	if len(allResults) == 0 {
		return nil
	}

	// Aggregate text parts from all candidates across chunks
	var textParts []string
	var finishReason any
	var role string

	for _, chunk := range allResults {
		candidates, ok := chunk["candidates"].([]any)
		if !ok || len(candidates) == 0 {
			continue
		}
		candidate, ok := candidates[0].(map[string]any)
		if !ok {
			continue
		}

		if fr, ok := candidate["finishReason"]; ok && fr != nil {
			finishReason = fr
		}

		content, ok := candidate["content"].(map[string]any)
		if !ok {
			continue
		}

		if r, ok := content["role"].(string); ok && role == "" {
			role = r
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				textParts = append(textParts, text)
			}
		}
	}

	// Build aggregated response in Gemini format
	result := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{
							"text": strings.Join(textParts, ""),
						},
					},
					"role": role,
				},
			},
		},
	}

	if finishReason != nil {
		if candidates, ok := result["candidates"].([]any); ok && len(candidates) > 0 {
			if c, ok := candidates[0].(map[string]any); ok {
				c["finishReason"] = finishReason
			}
		}
	}

	// Include usage from the last chunk
	for i := len(allResults) - 1; i >= 0; i-- {
		if usage, ok := allResults[i]["usageMetadata"]; ok {
			result["usageMetadata"] = usage
			break
		}
	}

	// Include model version
	for _, chunk := range allResults {
		if mv, ok := chunk["modelVersion"]; ok {
			result["modelVersion"] = mv
			break
		}
	}

	return result
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

	// Parse usage metadata (token counts) and time_to_first_token
	metrics := make(map[string]any)
	if usageMetadata, ok := raw["usageMetadata"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usageMetadata) {
			metrics[k] = v
		}
	}
	metrics["time_to_first_token"] = time.Since(gt.startTime).Seconds()
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
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
