package anthropic

// this file parses the messages API.

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

// messagesTracer is a tracer for the anthropic v1/messages POST endpoint.
// See docs here: https://docs.anthropic.com/en/api/messages
type messagesTracer struct {
	cfg       *middlewareConfig
	streaming bool
	metadata  map[string]any
	startTime time.Time
}

func newMessagesTracer(cfg *middlewareConfig) *messagesTracer {
	return &messagesTracer{
		cfg:       cfg,
		streaming: false,
		metadata: map[string]any{
			"provider": "anthropic",
		},
	}
}

func (mt *messagesTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	mt.startTime = t

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		// Fall back to default span name on parse error.
		ctx, span := mt.cfg.tracer().Start(ctx, "anthropic.messages.create", trace.WithTimestamp(t))
		return ctx, span, err
	}

	metadataFields := []string{
		"model",
		"max_tokens",
		"temperature",
		"top_p",
		"top_k",
		"stop_sequences",
		"stream",
		"tools",
		"tool_choice",
		"metadata",
		"container",
		"mcp_servers",
		"service_tier",
		"thinking",
	}

	// handle simple fields here.
	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			mt.metadata[field] = value
			// keep track of streaming requests so we can parse the streaming response later.
			if field == "stream" {
				if value, ok := value.(bool); ok {
					mt.streaming = value
				}
			}
		}
	}

	// Use a distinct span name for streaming calls.
	spanName := "anthropic.messages.create"
	if mt.streaming {
		spanName = "anthropic.messages.stream"
	}

	ctx, span := mt.cfg.tracer().Start(ctx, spanName, trace.WithTimestamp(t))

	// Build input messages array, appending system prompt last
	var msgs []any

	// Add user/assistant messages first, normalizing content blocks
	if messages, ok := raw["messages"].([]any); ok {
		for _, m := range messages {
			if msg, ok := m.(map[string]any); ok {
				msgs = append(msgs, normalizeMessageContent(msg))
			} else {
				msgs = append(msgs, m)
			}
		}
	}

	// Append system prompt as a message at the end.
	// The system field can be a string or a list of content blocks.
	if system, ok := raw["system"]; ok {
		msgs = append(msgs, map[string]any{
			"role":    "system",
			"content": simplifyContentBlocks(system),
		})
	}

	if len(msgs) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", msgs); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", mt.metadata); err != nil {
		return ctx, span, err
	}

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (mt *messagesTracer) TagSpan(span trace.Span, body io.Reader) error {
	if mt.streaming {
		return mt.parseStreamingResponse(span, body)
	}
	return mt.parseResponse(span, body)
}

func (mt *messagesTracer) parseStreamingResponse(span trace.Span, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	accumulator := internal.NewClaudeStreamAccumulator()
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

		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return err
		}
		if accumulator.Add(chunk) && timeToFirstToken == 0 {
			timeToFirstToken = time.Since(mt.startTime)
		}
	}

	if messages := accumulator.Output(); len(messages) > 0 {
		output := messages[0]
		if stopReason := accumulator.StopReason(); stopReason != nil {
			output["stop_reason"] = stopReason
		}
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}
	if model := accumulator.Model(); model != "" {
		mt.metadata["model"] = model
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", mt.metadata); err != nil {
		return err
	}

	metrics := make(map[string]any)
	for key, value := range parseUsageTokens(accumulator.Usage()) {
		metrics[key] = value
	}
	if timeToFirstToken > 0 {
		metrics["time_to_first_token"] = timeToFirstToken.Seconds()
	}
	if len(metrics) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (mt *messagesTracer) parseResponse(span trace.Span, body io.Reader) error {
	var raw map[string]any
	err := json.NewDecoder(body).Decode(&raw)
	if err != nil {
		return err
	}

	return mt.handleMessageResponse(span, raw)
}

func (mt *messagesTracer) handleMessageResponse(span trace.Span, rawMsg map[string]any) error {
	// Update model if present in response (in case it was resolved from "latest").
	if model, ok := rawMsg["model"].(string); ok {
		mt.metadata["model"] = model
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", mt.metadata); err != nil {
		return err
	}

	if usage, ok := rawMsg["usage"].(map[string]any); ok {
		metrics := parseUsageTokens(usage)
		if len(metrics) > 0 {
			if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
				return err
			}
		}
	}

	if content, ok := rawMsg["content"]; ok {
		role, _ := rawMsg["role"].(string)
		output := map[string]any{
			"role":    role,
			"content": content,
		}
		if stopReason, exists := rawMsg["stop_reason"]; exists && stopReason != nil {
			output["stop_reason"] = stopReason
		}
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	return nil
}

// normalizeMessageContent simplifies a message's content field when it is a
// single text block with no extra fields (e.g. cache_control), converting
// [{type: "text", text: "hello"}] to just "hello".
func normalizeMessageContent(msg map[string]any) map[string]any {
	content := msg["content"]
	// Only attempt simplification for list content.
	if _, isList := content.([]any); !isList {
		return msg
	}
	simplified := simplifyContentBlocks(content)
	// If simplifyContentBlocks returned a string, it was simplified.
	if _, isStr := simplified.(string); !isStr {
		return msg
	}
	// Shallow-copy to avoid mutating the original map.
	out := make(map[string]any, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	out["content"] = simplified
	return out
}

// simplifyContentBlocks converts a list of content blocks to a plain string
// when the list contains exactly one text block with only type+text fields.
func simplifyContentBlocks(content any) any {
	blocks, ok := content.([]any)
	if !ok || len(blocks) != 1 {
		return content
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		return content
	}
	if block["type"] != "text" {
		return content
	}
	text, ok := block["text"].(string)
	if !ok {
		return content
	}
	// Only simplify if there are no extra fields (e.g. cache_control).
	if len(block) > 2 {
		return content
	}
	return text
}
