package together

// this file parses the chat completions API.

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

// chatCompletionsTracer is a tracer for the Together AI chat/completions POST endpoint.
// See docs here: https://docs.together.ai/reference/chat-completions-1
type chatCompletionsTracer struct {
	cfg       *middlewareConfig
	streaming bool
	metadata  map[string]any
	startTime time.Time
}

func newChatCompletionsTracer(cfg *middlewareConfig) *chatCompletionsTracer {
	return &chatCompletionsTracer{
		cfg: cfg,
		metadata: map[string]any{
			"provider": "together",
		},
	}
}

func (ct *chatCompletionsTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ct.startTime = t
	ctx, span := ct.cfg.tracer().Start(ctx, "Chat Completion", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	metadataFields := []string{
		"model", "frequency_penalty", "logprobs", "max_tokens", "n",
		"presence_penalty", "response_format", "seed", "stop", "stream",
		"temperature", "top_p", "top_k", "tools", "tool_choice", "user",
		"repetition_penalty", "min_p",
	}
	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			ct.metadata[field] = value
			if field == "stream" {
				if streaming, ok := value.(bool); ok {
					ct.streaming = streaming
				}
			}
		}
	}

	if messages, ok := raw["messages"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", messages); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", ct.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (ct *chatCompletionsTracer) TagSpan(span trace.Span, body io.Reader) error {
	if ct.streaming {
		return ct.parseStreamingResponse(span, body)
	}
	return ct.parseResponse(span, body)
}

func (ct *chatCompletionsTracer) parseResponse(span trace.Span, body io.Reader) error {
	timeToFirstToken := time.Since(ct.startTime)

	var raw map[string]any
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	for _, field := range []string{"id", "object", "created"} {
		if v, ok := raw[field]; ok {
			ct.metadata[field] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", ct.metadata); err != nil {
		return err
	}

	metrics := map[string]any{"time_to_first_token": timeToFirstToken.Seconds()}
	if usage, ok := raw["usage"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	if choices, ok := raw["choices"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", choices); err != nil {
			return err
		}
	}

	return nil
}

func (ct *chatCompletionsTracer) parseStreamingResponse(span trace.Span, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	var allChunks []map[string]any
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
			timeToFirstToken = time.Since(ct.startTime)
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return err
		}
		allChunks = append(allChunks, chunk)

		if usage, ok := chunk["usage"]; ok {
			ct.metadata["usage"] = usage
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.output_json", mergeChatCompletionChunks(allChunks)); err != nil {
		return err
	}

	metrics := map[string]any{"time_to_first_token": timeToFirstToken.Seconds()}
	if usage, ok := ct.metadata["usage"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	return scanner.Err()
}

// mergeChatCompletionChunks accumulates a streamed chat completion's delta
// content, tool calls, and finish reason into a single choice, matching the
// shape of a non-streaming chat completion's choices array.
func mergeChatCompletionChunks(chunks []map[string]any) []map[string]any {
	var role any
	var content string
	var toolCalls []any
	var finishReason any

	for _, chunk := range chunks {
		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}

		if role == nil {
			role = delta["role"]
		}
		if fr, ok := choice["finish_reason"]; ok && fr != nil {
			finishReason = fr
		}
		if deltaContent, ok := delta["content"].(string); ok {
			content += deltaContent
		}
		if deltaToolCalls, ok := delta["tool_calls"].([]any); ok && len(deltaToolCalls) > 0 {
			toolCalls = mergeToolCallDelta(toolCalls, deltaToolCalls[0])
		}
	}

	var mergedToolCalls any
	if len(toolCalls) > 0 {
		mergedToolCalls = toolCalls
	}

	return []map[string]any{
		{
			"index": 0,
			"message": map[string]any{
				"role":       role,
				"content":    content,
				"tool_calls": mergedToolCalls,
			},
			"finish_reason": finishReason,
		},
	}
}

func mergeToolCallDelta(toolCalls []any, rawDelta any) []any {
	delta, ok := rawDelta.(map[string]any)
	if !ok {
		return toolCalls
	}

	if toolID, ok := delta["id"].(string); ok && toolID != "" {
		newCall := map[string]any{"id": toolID, "type": delta["type"]}
		if function, ok := delta["function"].(map[string]any); ok {
			newCall["function"] = function
		}
		return append(toolCalls, newCall)
	}

	if len(toolCalls) == 0 {
		return toolCalls
	}
	lastCall, ok := toolCalls[len(toolCalls)-1].(map[string]any)
	if !ok {
		return toolCalls
	}
	function, ok := lastCall["function"].(map[string]any)
	if !ok {
		return toolCalls
	}
	deltaFunction, ok := delta["function"].(map[string]any)
	if !ok {
		return toolCalls
	}
	if args, ok := deltaFunction["arguments"].(string); ok {
		if currentArgs, ok := function["arguments"].(string); ok {
			function["arguments"] = currentArgs + args
		} else {
			function["arguments"] = args
		}
	}
	return toolCalls
}
