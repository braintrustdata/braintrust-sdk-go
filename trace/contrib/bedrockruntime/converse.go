package bedrockruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	smithydoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// parseUsageTokens normalizes a Bedrock *types.TokenUsage to the Braintrust
// metric names. Mirrors the semantics of trace/contrib/anthropic:
// prompt_tokens = InputTokens + CacheReadInputTokens + CacheWriteInputTokens
// so cached + cache-creation tokens are still counted toward the prompt total.
func parseUsageTokens(u *types.TokenUsage) map[string]int64 {
	metrics := make(map[string]int64)
	if u == nil {
		return metrics
	}

	var input, cacheRead, cacheWrite int64
	if u.InputTokens != nil {
		input = int64(*u.InputTokens)
	}
	if u.OutputTokens != nil {
		metrics["completion_tokens"] = int64(*u.OutputTokens)
	}
	if u.CacheReadInputTokens != nil {
		cacheRead = int64(*u.CacheReadInputTokens)
		metrics["prompt_cached_tokens"] = cacheRead
	}
	if u.CacheWriteInputTokens != nil {
		cacheWrite = int64(*u.CacheWriteInputTokens)
		metrics["prompt_cache_creation_tokens"] = cacheWrite
	}

	promptTotal := input + cacheRead + cacheWrite
	metrics["prompt_tokens"] = promptTotal

	if u.TotalTokens != nil {
		metrics["tokens"] = int64(*u.TotalTokens)
	} else if completion, ok := metrics["completion_tokens"]; ok {
		metrics["tokens"] = promptTotal + completion
	}
	return metrics
}

// converseTracer handles the non-streaming Converse operation.
type converseTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
}

func (t *converseTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.converse", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "converse",
	}

	params, ok := in.(*bedrockruntime.ConverseInput)
	if !ok || params == nil {
		return ctx, span
	}

	setConverseInputAttrs(t.cfg.logger, span, t.metadata, params.ModelId, params.Messages, params.System,
		params.InferenceConfig, params.ToolConfig, params.AdditionalModelRequestFields, false)
	return ctx, span
}

func (t *converseTracer) TagOutput(span trace.Span, out any, start time.Time) {
	defer span.End()
	timeToLastByte := time.Since(start).Seconds()

	resp, ok := out.(*bedrockruntime.ConverseOutput)
	if !ok || resp == nil {
		return
	}

	if string(resp.StopReason) != "" {
		t.metadata["stop_reason"] = string(resp.StopReason)
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)

	if msg := extractOutputMessage(resp.Output); msg != nil {
		setJSONAttr(t.cfg.logger, span, "braintrust.output_json", []any{msg})
	}

	metrics := make(map[string]any)
	for k, v := range parseUsageTokens(resp.Usage) {
		metrics[k] = v
	}
	metrics["time_to_first_token"] = timeToLastByte
	setJSONAttr(t.cfg.logger, span, "braintrust.metrics", metrics)
}

// setConverseInputAttrs writes the shared Converse/ConverseStream request
// attributes onto the span (input_json, metadata, span_attributes).
func setConverseInputAttrs(
	log logger.Logger,
	span trace.Span,
	metadata map[string]any,
	modelID *string,
	messages []types.Message,
	system []types.SystemContentBlock,
	infer *types.InferenceConfiguration,
	toolCfg *types.ToolConfiguration,
	additional smithydoc.Interface,
	streaming bool,
) {
	if modelID != nil {
		metadata["model"] = *modelID
	}
	if infer != nil {
		if infer.MaxTokens != nil {
			metadata["max_tokens"] = *infer.MaxTokens
		}
		if infer.Temperature != nil {
			metadata["temperature"] = *infer.Temperature
		}
		if infer.TopP != nil {
			metadata["top_p"] = *infer.TopP
		}
		if len(infer.StopSequences) > 0 {
			metadata["stop_sequences"] = infer.StopSequences
		}
	}
	if toolCfg != nil {
		if tools := toolsToJSON(toolCfg.Tools); len(tools) > 0 {
			metadata["tools"] = tools
		}
		if tc := toolChoiceToJSON(toolCfg.ToolChoice); tc != nil {
			metadata["tool_choice"] = tc
		}
	}
	if additional != nil {
		var v any
		if err := additional.UnmarshalSmithyDocument(&v); err == nil && v != nil {
			metadata["additional_model_request_fields"] = v
		}
	}
	if streaming {
		metadata["stream"] = true
	}

	// Prepend system prompt as a role=system message for consistency with the
	// anthropic integration.
	var msgs []any
	if sys := systemToJSON(system); sys != nil {
		msgs = append(msgs, map[string]any{
			"role":    "system",
			"content": sys,
		})
	}
	for _, m := range messagesToJSON(messages) {
		msgs = append(msgs, m)
	}
	if len(msgs) > 0 {
		setJSONAttr(log, span, "braintrust.input_json", msgs)
	}
	setJSONAttr(log, span, "braintrust.metadata", metadata)
	setJSONAttr(log, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
}

// systemToJSON renders a System content list into a JSON-friendly value.
// Returns an []any of content blocks, or nil if no blocks.
func systemToJSON(sys []types.SystemContentBlock) []any {
	if len(sys) == 0 {
		return nil
	}
	out := make([]any, 0, len(sys))
	for _, b := range sys {
		switch v := b.(type) {
		case *types.SystemContentBlockMemberText:
			out = append(out, map[string]any{"type": "text", "text": v.Value})
		default:
			out = append(out, map[string]any{"type": "unknown"})
		}
	}
	return out
}

// messagesToJSON renders typed Bedrock messages into JSON-friendly maps.
func messagesToJSON(ms []types.Message) []map[string]any {
	if len(ms) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, messageToJSON(m))
	}
	return out
}

func messageToJSON(m types.Message) map[string]any {
	return map[string]any{
		"role":    string(m.Role),
		"content": contentBlocksToJSON(m.Content),
	}
}

// contentBlocksToJSON renders typed content blocks into plain JSON maps.
// Covers the common block types (text, toolUse, toolResult) and serializes
// less common ones via JSON marshal fallback.
func contentBlocksToJSON(blocks []types.ContentBlock) []any {
	out := make([]any, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, contentBlockToJSON(b))
	}
	return out
}

func contentBlockToJSON(b types.ContentBlock) any {
	switch v := b.(type) {
	case *types.ContentBlockMemberText:
		return map[string]any{"type": "text", "text": v.Value}
	case *types.ContentBlockMemberToolUse:
		return toolUseBlockToJSON(v.Value)
	case *types.ContentBlockMemberToolResult:
		return toolResultBlockToJSON(v.Value)
	case *types.ContentBlockMemberImage:
		return imageBlockToJSON(v.Value)
	case *types.ContentBlockMemberDocument:
		return documentBlockToJSON(v.Value)
	case *types.ContentBlockMemberReasoningContent:
		return reasoningBlockToJSON(v.Value)
	default:
		return fallbackMarshal(b)
	}
}

func imageBlockToJSON(b types.ImageBlock) map[string]any {
	out := map[string]any{"type": "image", "format": string(b.Format)}
	switch src := b.Source.(type) {
	case *types.ImageSourceMemberBytes:
		// Emit the Anthropic-style base64 data URL shape so the Braintrust UI
		// can render the image inline, matching the trace/contrib/anthropic
		// integration's representation.
		out["source"] = map[string]any{
			"type":       "base64",
			"media_type": "image/" + string(b.Format),
			"data":       base64.StdEncoding.EncodeToString(src.Value),
		}
	case *types.ImageSourceMemberS3Location:
		s3 := map[string]any{"type": "s3"}
		if src.Value.Uri != nil {
			s3["uri"] = *src.Value.Uri
		}
		out["source"] = s3
	}
	return out
}

func documentBlockToJSON(b types.DocumentBlock) map[string]any {
	out := map[string]any{"type": "document", "format": string(b.Format)}
	if b.Name != nil {
		out["name"] = *b.Name
	}
	if b.Citations != nil && b.Citations.Enabled != nil {
		out["citations_enabled"] = *b.Citations.Enabled
	}
	switch src := b.Source.(type) {
	case *types.DocumentSourceMemberText:
		out["source"] = map[string]any{"type": "text", "text": src.Value}
	case *types.DocumentSourceMemberBytes:
		out["source"] = map[string]any{"type": "bytes", "size": len(src.Value)}
	}
	return out
}

func reasoningBlockToJSON(b types.ReasoningContentBlock) map[string]any {
	out := map[string]any{"type": "reasoning"}
	if r, ok := b.(*types.ReasoningContentBlockMemberReasoningText); ok {
		if r.Value.Text != nil {
			out["text"] = *r.Value.Text
		}
		if r.Value.Signature != nil {
			out["signature"] = *r.Value.Signature
		}
	}
	return out
}

func toolUseBlockToJSON(u types.ToolUseBlock) map[string]any {
	out := map[string]any{"type": "tool_use"}
	if u.ToolUseId != nil {
		out["id"] = *u.ToolUseId
	}
	if u.Name != nil {
		out["name"] = *u.Name
	}
	if u.Input != nil {
		var v any
		if err := u.Input.UnmarshalSmithyDocument(&v); err == nil {
			out["input"] = v
		}
	}
	return out
}

func toolResultBlockToJSON(r types.ToolResultBlock) map[string]any {
	out := map[string]any{"type": "tool_result"}
	if r.ToolUseId != nil {
		out["tool_use_id"] = *r.ToolUseId
	}
	if string(r.Status) != "" {
		out["status"] = string(r.Status)
	}
	content := make([]any, 0, len(r.Content))
	for _, c := range r.Content {
		switch v := c.(type) {
		case *types.ToolResultContentBlockMemberText:
			content = append(content, map[string]any{"type": "text", "text": v.Value})
		case *types.ToolResultContentBlockMemberJson:
			var jv any
			if v.Value != nil {
				_ = v.Value.UnmarshalSmithyDocument(&jv)
			}
			content = append(content, map[string]any{"type": "json", "json": jv})
		default:
			content = append(content, fallbackMarshal(c))
		}
	}
	out["content"] = content
	return out
}

// fallbackMarshal renders any typed block by JSON-roundtripping via reflect.
// Unknown block types get their exported fields serialized without failing
// the trace.
func fallbackMarshal(v any) any {
	bytes, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%T", v)
	}
	var out any
	if err := json.Unmarshal(bytes, &out); err != nil {
		return string(bytes)
	}
	return out
}

// toolsToJSON flattens a list of Tools into JSON-ready maps.
func toolsToJSON(tools []types.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		switch v := t.(type) {
		case *types.ToolMemberToolSpec:
			spec := map[string]any{}
			if v.Value.Name != nil {
				spec["name"] = *v.Value.Name
			}
			if v.Value.Description != nil {
				spec["description"] = *v.Value.Description
			}
			if v.Value.InputSchema != nil {
				if js, ok := v.Value.InputSchema.(*types.ToolInputSchemaMemberJson); ok && js.Value != nil {
					var schema any
					if err := js.Value.UnmarshalSmithyDocument(&schema); err == nil {
						spec["input_schema"] = schema
					}
				}
			}
			out = append(out, map[string]any{"tool_spec": spec})
		default:
			out = append(out, fallbackMarshal(t))
		}
	}
	return out
}

func toolChoiceToJSON(tc types.ToolChoice) any {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case *types.ToolChoiceMemberAuto:
		return map[string]any{"type": "auto"}
	case *types.ToolChoiceMemberAny:
		return map[string]any{"type": "any"}
	case *types.ToolChoiceMemberTool:
		out := map[string]any{"type": "tool"}
		if v.Value.Name != nil {
			out["name"] = *v.Value.Name
		}
		return out
	default:
		return fallbackMarshal(tc)
	}
}

// extractOutputMessage returns a JSON-friendly message map from a ConverseOutput union.
func extractOutputMessage(out types.ConverseOutput) map[string]any {
	if out == nil {
		return nil
	}
	if m, ok := out.(*types.ConverseOutputMemberMessage); ok {
		return messageToJSON(m.Value)
	}
	return nil
}
