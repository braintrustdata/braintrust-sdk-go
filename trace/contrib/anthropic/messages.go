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
}

func newMessagesTracer(cfg *middlewareConfig) *messagesTracer {
	return &messagesTracer{
		cfg:       cfg,
		streaming: false,
		metadata: map[string]any{
			"provider": "anthropic",
			"endpoint": "/v1/messages",
		},
	}
}

func (mt *messagesTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := mt.cfg.tracer().Start(
		ctx,
		"anthropic.messages.create",
		trace.WithTimestamp(t),
	)

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
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

	// Build input messages array, prepending system prompt if present
	var msgs []any

	// Prepend system prompt as a message if present
	if system, ok := raw["system"]; ok {
		msgs = append(msgs, map[string]any{
			"role":    "system",
			"content": system,
		})
	}

	// Add user/assistant messages
	if messages, ok := raw["messages"].([]any); ok {
		msgs = append(msgs, messages...)
	}

	if len(msgs) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", msgs); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", mt.metadata); err != nil {
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
	var allResults []map[string]any
	usage := make(map[string]any)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		line = strings.TrimPrefix(line, "data: ")
		if line == "[DONE]" {
			break // End of stream
		}

		var chunk map[string]any
		err := json.Unmarshal([]byte(line), &chunk)
		if err != nil {
			return err
		}

		allResults = append(allResults, chunk)

		// Handle usage in streaming response from different event types
		eventType, ok := chunk["type"].(string)
		if ok {
			switch eventType {

			// Contains input and cache tokens
			case "message_start":
				// Usage is nested in message object for message_start events
				if message, ok := chunk["message"].(map[string]any); ok {
					if curUsage, ok := message["usage"].(map[string]any); ok {
						// Initialize combined usage with message_start data (contains input_tokens)
						for k, v := range curUsage {
							usage[k] = v
						}
					}
				}

			// Contains output tokens, There can be multiple "message_delta" events in a single response.
			// But the usage data in there is supposed to be cumulative as per the docs.
			// So using the last usage data is fine.
			case "message_delta":
				// Usage is at top level for message_delta events (contains final output_tokens)
				if curUsage, ok := chunk["usage"].(map[string]any); ok {
					// message_delta usage is cumulative, so it overrides any previous values
					for k, v := range curUsage {
						usage[k] = v
					}
				}
			}
		}
	}

	// Post-process streaming results to match expected output format
	output := mt.postprocessStreamingResults(allResults)
	if len(output) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	// Handle usage metrics
	if len(usage) > 0 {
		metrics := parseUsageTokens(usage)
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func (mt *messagesTracer) postprocessStreamingResults(allResults []map[string]any) []map[string]any {
	// Track content blocks by index
	contentBlocks := make(map[int]map[string]any)
	builders := make(map[int]*strings.Builder)
	var stopReason interface{}

	for _, result := range allResults {
		eventType, ok := result["type"].(string)
		if !ok {
			continue
		}

		switch eventType {
		case "content_block_start":
			// Initialize a new content block
			indexf64, ok := result["index"].(float64)
			if !ok {
				continue
			}
			if contentBlock, ok := result["content_block"].(map[string]any); ok {
				contentBlocks[int(indexf64)] = contentBlock
			}

		case "content_block_delta":
			indexf64, ok := result["index"].(float64)
			if !ok {
				continue
			}
			idx := int(indexf64)

			if delta, ok := result["delta"].(map[string]any); ok {
				deltaType, ok := delta["type"].(string)
				if !ok {
					continue
				}

				// Ensure block exists
				if _, exists := contentBlocks[idx]; !exists {
					contentBlocks[idx] = make(map[string]any)
				}

				switch deltaType {
				case "text_delta":
					// Accumulate text for text blocks
					if text, ok := delta["text"].(string); ok {
						if builders[idx] == nil {
							builders[idx] = &strings.Builder{}
						}
						builders[idx].WriteString(text)
						contentBlocks[idx]["type"] = "text"
					}
				case "input_json_delta":
					// Accumulate JSON for tool_use blocks
					if partialJSON, ok := delta["partial_json"].(string); ok {
						if builders[idx] == nil {
							builders[idx] = &strings.Builder{}
						}
						builders[idx].WriteString(partialJSON)
						contentBlocks[idx]["type"] = "tool_use"
					}
				}
			}

		case "message_delta":
			if delta, ok := result["delta"].(map[string]any); ok {
				if sr, ok := delta["stop_reason"]; ok {
					stopReason = sr
				}
			}
		}
	}

	// Convert builders to strings in the appropriate field
	for idx, builder := range builders {
		if block, ok := contentBlocks[idx]; ok {
			msg := builder.String()
			// Check block type to determine which field to set
			if blockType, ok := block["type"].(string); ok {
				switch blockType {
				case "text":
					block["text"] = msg
				case "tool_use":
					block["input"] = msg
				}
			}
		}
	}

	// Store stop reason in metadata if present
	if stopReason != nil {
		mt.metadata["stop_reason"] = stopReason
	}

	// Convert map to sorted content array
	if len(contentBlocks) == 0 {
		return nil
	}

	content := make([]map[string]any, 0, len(contentBlocks))
	for i := 0; i < len(contentBlocks); i++ {
		if block, ok := contentBlocks[i]; ok {
			// Parse accumulated JSON string for tool_use blocks
			if block["type"] == "tool_use" {
				if inputStr, ok := block["input"].(string); ok && inputStr != "" {
					var inputObj any
					if err := json.Unmarshal([]byte(inputStr), &inputObj); err == nil {
						block["input"] = inputObj
					}
				}
			}
			content = append(content, block)
		}
	}

	// Format as array of messages (same format as input)
	return []map[string]any{
		{
			"role":    "assistant",
			"content": content,
		},
	}
}

func (mt *messagesTracer) parseResponse(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	err := json.NewDecoder(body).Decode(&raw)
	if err != nil {
		return err
	}

	return mt.handleMessageResponse(span, raw)
}

func (mt *messagesTracer) handleMessageResponse(span trace.Span, rawMsg map[string]any) error {
	// Only add response-level metadata that's relevant
	// (stop_reason, stop_sequence, model if not already set)
	responseMetadataFields := []string{
		"stop_reason",
		"stop_sequence",
	}

	for _, field := range responseMetadataFields {
		if v, ok := rawMsg[field]; ok {
			mt.metadata[field] = v
		}
	}

	// Update model if present in response (in case it was resolved from "latest")
	if model, ok := rawMsg["model"].(string); ok {
		mt.metadata["model"] = model
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", mt.metadata); err != nil {
		return err
	}

	if usage, ok := rawMsg["usage"].(map[string]any); ok {
		metrics := parseUsageTokens(usage)
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			return err
		}
	}

	// Format output as array of messages (same format as input)
	if content, ok := rawMsg["content"]; ok {
		role, _ := rawMsg["role"].(string)
		output := []map[string]any{
			{
				"role":    role,
				"content": content,
			},
		}
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	return nil
}
