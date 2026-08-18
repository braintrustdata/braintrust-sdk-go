package genai

// this file parses the generateContent API.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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
			"provider": "google",
			"model":    model,
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

	// Extract explicitly allowlisted provider configuration. Conversation
	// content and system instructions belong in input, while tool definitions
	// use Braintrust's normalized metadata shape.
	for _, field := range []string{"safetySettings", "cachedContent"} {
		if value, exists := raw[field]; exists {
			gt.metadata[field] = value
		}
	}
	if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
		if normalized := normalizeTools(tools); len(normalized) > 0 {
			gt.metadata["tools"] = normalized
		}
	}
	if toolConfig, ok := raw["toolConfig"].(map[string]any); ok {
		captureToolConfig(gt.metadata, toolConfig)
	}

	if genConfig, ok := raw["generationConfig"].(map[string]any); ok {
		captureGenerationConfig(gt.metadata, genConfig)
	}

	// Log the raw request format
	inputLog := make(map[string]any)

	// Add model from URL path (or from body if present)
	if model, ok := raw["model"].(string); ok {
		inputLog["model"] = model
		gt.metadata["model"] = model
	} else if gt.model != "" {
		inputLog["model"] = gt.model
	}

	// Add conversation content and the separate system instruction as-is.
	if contents, ok := raw["contents"]; ok {
		inputLog["contents"] = contents
	}
	if systemInstruction, ok := raw["systemInstruction"]; ok {
		inputLog["systemInstruction"] = systemInstruction
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
	reader := bufio.NewReader(body)
	var allResults []map[string]any
	var timeToFirstToken time.Duration

	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
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

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}

	// Aggregate chunks into a single response
	output := gt.postprocessStreamingResults(allResults)
	if output != nil {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	// Collect usage from the final usage-bearing chunk.
	metrics := make(map[string]any)
	for i := len(allResults) - 1; i >= 0; i-- {
		if usageMetadata, ok := allResults[i]["usageMetadata"].(map[string]any); ok {
			for k, v := range parseUsageTokens(usageMetadata) {
				metrics[k] = v
			}
			gt.captureUsageMetadata(usageMetadata)
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

	return nil
}

// postprocessStreamingResults aggregates streaming chunks into one provider-
// native response. It preserves all candidates and non-text parts (thoughts,
// function calls, code execution, and media) instead of flattening to text.
func (gt *generateContentTracer) postprocessStreamingResults(allResults []map[string]any) map[string]any {
	if len(allResults) == 0 {
		return nil
	}

	result := map[string]any{}
	candidatesByIndex := map[int]map[string]any{}
	var candidateOrder []int

	for _, chunk := range allResults {
		for key, value := range chunk {
			if key != "candidates" {
				result[key] = value
			}
		}

		candidates, _ := chunk["candidates"].([]any)
		for position, rawCandidate := range candidates {
			candidate, ok := rawCandidate.(map[string]any)
			if !ok {
				continue
			}
			index := position
			if ok, parsed := internal.ToInt64(candidate["index"]); ok {
				index = int(parsed)
			}
			aggregated, exists := candidatesByIndex[index]
			if !exists {
				aggregated = map[string]any{}
				candidatesByIndex[index] = aggregated
				candidateOrder = append(candidateOrder, index)
			}
			mergeStreamingCandidate(aggregated, candidate)
		}
	}

	if len(candidateOrder) > 0 {
		candidates := make([]any, 0, len(candidateOrder))
		for _, index := range candidateOrder {
			candidates = append(candidates, candidatesByIndex[index])
		}
		result["candidates"] = candidates
	}

	return result
}

func mergeStreamingCandidate(dst, src map[string]any) {
	for key, value := range src {
		if key != "content" {
			dst[key] = value
		}
	}

	srcContent, ok := src["content"].(map[string]any)
	if !ok {
		return
	}
	dstContent, ok := dst["content"].(map[string]any)
	if !ok {
		dstContent = map[string]any{}
		dst["content"] = dstContent
	}
	for key, value := range srcContent {
		if key != "parts" {
			dstContent[key] = value
		}
	}

	srcParts, _ := srcContent["parts"].([]any)
	dstParts, _ := dstContent["parts"].([]any)
	for _, rawPart := range srcParts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			dstParts = append(dstParts, rawPart)
			continue
		}
		if len(dstParts) > 0 {
			if previous, ok := dstParts[len(dstParts)-1].(map[string]any); ok && mergeTextPart(previous, part) {
				continue
			}
		}
		dstParts = append(dstParts, part)
	}
	dstContent["parts"] = dstParts
}

func mergeTextPart(dst, src map[string]any) bool {
	dstText, dstOK := dst["text"].(string)
	srcText, srcOK := src["text"].(string)
	if !dstOK || !srcOK || len(dst) != len(src) {
		return false
	}
	for key, value := range src {
		if key != "text" && !reflect.DeepEqual(dst[key], value) {
			return false
		}
	}
	dst["text"] = dstText + srcText
	return true
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
	if modelVersion, ok := raw["modelVersion"].(string); ok {
		gt.metadata["model"] = modelVersion
	}

	metrics := make(map[string]any)
	if usageMetadata, ok := raw["usageMetadata"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usageMetadata) {
			metrics[k] = v
		}
		gt.captureUsageMetadata(usageMetadata)
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", gt.metadata); err != nil {
		return err
	}
	if err := internal.SetJSONAttr(span, "braintrust.output_json", raw); err != nil {
		return err
	}
	return internal.SetJSONAttr(span, "braintrust.metrics", metrics)
}

// parseUsageTokens normalizes Gemini UsageMetadata to Braintrust metrics.
func parseUsageTokens(usage map[string]interface{}) map[string]int64 {
	metrics := make(map[string]int64)

	hasPromptTokens, promptTokens := internal.ToInt64(usage["promptTokenCount"])
	hasToolUsePromptTokens, toolUsePromptTokens := internal.ToInt64(usage["toolUsePromptTokenCount"])
	if hasPromptTokens || hasToolUsePromptTokens {
		metrics["prompt_tokens"] = promptTokens + toolUsePromptTokens
	}

	hasCandidateTokens, candidateTokens := internal.ToInt64(usage["candidatesTokenCount"])
	hasThoughtTokens, thoughtTokens := internal.ToInt64(usage["thoughtsTokenCount"])
	if hasCandidateTokens || hasThoughtTokens {
		metrics["completion_tokens"] = candidateTokens + thoughtTokens
	}
	if hasThoughtTokens {
		metrics["completion_reasoning_tokens"] = thoughtTokens
	}

	if ok, totalTokens := internal.ToInt64(usage["totalTokenCount"]); ok {
		metrics["tokens"] = totalTokens
	}
	if ok, cachedTokens := internal.ToInt64(usage["cachedContentTokenCount"]); ok {
		metrics["prompt_cached_tokens"] = cachedTokens
	}

	if tokens, ok := sumModalityTokens(usage["promptTokensDetails"], "AUDIO"); ok {
		metrics["prompt_audio_tokens"] = tokens
	}
	if tokens, ok := sumModalityTokens(usage["candidatesTokensDetails"], "AUDIO"); ok {
		metrics["completion_audio_tokens"] = tokens
	}
	if tokens, ok := sumModalityTokens(usage["candidatesTokensDetails"], "IMAGE"); ok {
		metrics["completion_image_tokens"] = tokens
	}

	return metrics
}

func sumModalityTokens(raw any, modality string) (int64, bool) {
	details, ok := raw.([]any)
	if !ok {
		return 0, false
	}
	var total int64
	found := false
	for _, rawDetail := range details {
		detail, ok := rawDetail.(map[string]any)
		if !ok {
			continue
		}
		detailModality, ok := detail["modality"].(string)
		if !ok || !strings.EqualFold(detailModality, modality) {
			continue
		}
		if ok, count := internal.ToInt64(detail["tokenCount"]); ok {
			total += count
			found = true
		}
	}
	return total, found
}

func (gt *generateContentTracer) captureUsageMetadata(usage map[string]any) {
	byModality := map[string]any{}
	if details, ok := usage["cachedContentTokenCountDetails"]; ok {
		byModality["cache_tokens_details"] = details
	} else if details, ok := usage["cacheTokensDetails"]; ok {
		byModality["cache_tokens_details"] = details
	}
	if details, ok := usage["toolUsePromptTokensDetails"]; ok {
		byModality["tool_use_prompt_tokens_details"] = details
	}
	if len(byModality) > 0 {
		gt.metadata["usage_by_modality"] = byModality
	}
}

func captureGenerationConfig(metadata, genConfig map[string]any) {
	// Common fields use Braintrust's canonical names.
	for source, target := range map[string]string{
		"temperature":      "temperature",
		"topP":             "top_p",
		"maxOutputTokens":  "max_tokens",
		"stopSequences":    "stop",
		"presencePenalty":  "presence_penalty",
		"frequencyPenalty": "frequency_penalty",
	} {
		if value, exists := genConfig[source]; exists {
			metadata[target] = value
		}
	}

	// Preserve the remaining reproducibility-relevant Google settings through
	// an explicit allowlist rather than copying generationConfig wholesale.
	for _, field := range []string{
		"topK",
		"candidateCount",
		"responseLogprobs",
		"logprobs",
		"seed",
		"responseMimeType",
		"responseSchema",
		"responseJsonSchema",
		"routingConfig",
		"modelSelectionConfig",
		"responseModalities",
		"mediaResolution",
		"speechConfig",
		"audioTimestamp",
		"thinkingConfig",
		"imageConfig",
		"enableEnhancedCivicAnswers",
	} {
		if value, exists := genConfig[field]; exists {
			metadata[field] = value
		}
	}
}

func normalizeTools(tools []any) []any {
	normalized := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if declarations, ok := tool["functionDeclarations"].([]any); ok {
			for _, rawDeclaration := range declarations {
				declaration, ok := rawDeclaration.(map[string]any)
				if !ok {
					continue
				}
				function := map[string]any{}
				for _, field := range []string{"name", "description"} {
					if value, exists := declaration[field]; exists {
						function[field] = value
					}
				}
				if parameters, exists := declaration["parameters"]; exists {
					function["parameters"] = normalizeJSONSchema(parameters)
				} else if parameters, exists := declaration["parametersJsonSchema"]; exists {
					function["parameters"] = normalizeJSONSchema(parameters)
				}
				if _, ok := function["name"].(string); ok {
					normalized = append(normalized, map[string]any{
						"type":     "function",
						"function": function,
					})
				}
			}
		}

		builtIn := map[string]any{}
		for key, value := range tool {
			if key != "functionDeclarations" {
				builtIn[key] = value
			}
		}
		if len(builtIn) > 0 {
			normalized = append(normalized, builtIn)
		}
	}
	return normalized
}

func normalizeJSONSchema(value any) any {
	switch value := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(value))
		for key, child := range value {
			if key == "type" {
				if schemaType, ok := child.(string); ok {
					normalized[key] = strings.ToLower(schemaType)
					continue
				}
			}
			normalized[key] = normalizeJSONSchema(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(value))
		for i, child := range value {
			normalized[i] = normalizeJSONSchema(child)
		}
		return normalized
	default:
		return value
	}
}

func captureToolConfig(metadata, toolConfig map[string]any) {
	if toolChoice := normalizeToolChoice(toolConfig); toolChoice != nil {
		metadata["tool_choice"] = toolChoice
	}

	functionConfig, ok := toolConfig["functionCallingConfig"].(map[string]any)
	if !ok {
		return
	}
	if mode, _ := functionConfig["mode"].(string); strings.EqualFold(mode, "VALIDATED") {
		metadata["function_calling_mode"] = "VALIDATED"
	}
	rawNames, ok := functionConfig["allowedFunctionNames"].([]any)
	if !ok || len(rawNames) < 2 {
		return
	}
	names := make([]string, 0, len(rawNames))
	for _, rawName := range rawNames {
		if name, ok := rawName.(string); ok {
			names = append(names, name)
		}
	}
	if len(names) > 1 {
		metadata["allowed_function_names"] = names
	}
}

func normalizeToolChoice(toolConfig map[string]any) any {
	functionConfig, ok := toolConfig["functionCallingConfig"].(map[string]any)
	if !ok {
		return nil
	}
	mode, _ := functionConfig["mode"].(string)
	switch strings.ToUpper(mode) {
	case "AUTO", "VALIDATED":
		return "auto"
	case "NONE":
		return "none"
	case "ANY":
		if names, ok := functionConfig["allowedFunctionNames"].([]any); ok && len(names) == 1 {
			if name, ok := names[0].(string); ok {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
		return "required"
	default:
		return nil
	}
}

// Ensure our tracer implements the shared interface
var _ internal.MiddlewareTracer = &generateContentTracer{}
