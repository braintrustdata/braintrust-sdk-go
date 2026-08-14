package bedrockruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
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
	hasPromptUsage := false
	if u.InputTokens != nil && *u.InputTokens >= 0 {
		input = int64(*u.InputTokens)
		hasPromptUsage = true
	}
	if u.OutputTokens != nil && *u.OutputTokens >= 0 {
		metrics["completion_tokens"] = int64(*u.OutputTokens)
	}
	if u.CacheReadInputTokens != nil && *u.CacheReadInputTokens >= 0 {
		cacheRead = int64(*u.CacheReadInputTokens)
		metrics["prompt_cached_tokens"] = cacheRead
		hasPromptUsage = true
	}
	if u.CacheWriteInputTokens != nil && *u.CacheWriteInputTokens >= 0 {
		cacheWrite = int64(*u.CacheWriteInputTokens)
		metrics["prompt_cache_creation_tokens"] = cacheWrite
		hasPromptUsage = true
	}

	var detailedCacheWrite int64
	hasDetailedCacheWrite := false
	for _, detail := range u.CacheDetails {
		if detail.InputTokens == nil || *detail.InputTokens < 0 {
			continue
		}
		value := int64(*detail.InputTokens)
		detailedCacheWrite += value
		hasDetailedCacheWrite = true
		switch detail.Ttl {
		case types.CacheTTLFiveMinutes:
			metrics["prompt_cache_creation_5m_tokens"] += value
		case types.CacheTTLOneHour:
			metrics["prompt_cache_creation_1h_tokens"] += value
		}
	}
	if hasDetailedCacheWrite {
		hasPromptUsage = true
		if u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens < 0 {
			cacheWrite = detailedCacheWrite
			metrics["prompt_cache_creation_tokens"] = cacheWrite
		}
	}

	if hasPromptUsage {
		promptTotal := input + cacheRead + cacheWrite
		metrics["prompt_tokens"] = promptTotal
		if completion, ok := metrics["completion_tokens"]; ok && u.TotalTokens == nil {
			metrics["tokens"] = promptTotal + completion
		}
	}
	if u.TotalTokens != nil && *u.TotalTokens >= 0 {
		metrics["tokens"] = int64(*u.TotalTokens)
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
		params.InferenceConfig, params.ToolConfig, false)
	return ctx, span
}

func (t *converseTracer) TagOutput(span trace.Span, out any, _ time.Time) {
	defer span.End()

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
			metadata["stop"] = infer.StopSequences
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
	case *types.ContentBlockMemberAudio:
		return audioBlockToJSON(v.Value)
	case *types.ContentBlockMemberVideo:
		return videoBlockToJSON(v.Value)
	case *types.ContentBlockMemberReasoningContent:
		return reasoningBlockToJSON(v.Value)
	default:
		return fallbackMarshal(b)
	}
}

func imageBlockToJSON(b types.ImageBlock) map[string]any {
	image := map[string]any{"format": string(b.Format)}
	switch src := b.Source.(type) {
	case *types.ImageSourceMemberBytes:
		image["source"] = map[string]any{"bytes": base64.StdEncoding.EncodeToString(src.Value)}
	case *types.ImageSourceMemberS3Location:
		image["source"] = s3SourceToJSON(src.Value)
	}
	return map[string]any{"type": "image", "image": image}
}

func documentBlockToJSON(b types.DocumentBlock) map[string]any {
	document := map[string]any{"format": string(b.Format)}
	if b.Name != nil {
		document["name"] = *b.Name
	}
	if b.Context != nil {
		document["context"] = *b.Context
	}
	if b.Citations != nil && b.Citations.Enabled != nil {
		document["citations_enabled"] = *b.Citations.Enabled
	}
	switch src := b.Source.(type) {
	case *types.DocumentSourceMemberText:
		document["source"] = map[string]any{"text": src.Value}
	case *types.DocumentSourceMemberBytes:
		document["source"] = map[string]any{"bytes": base64.StdEncoding.EncodeToString(src.Value)}
	case *types.DocumentSourceMemberContent:
		content := make([]any, 0, len(src.Value))
		for _, block := range src.Value {
			if text, ok := block.(*types.DocumentContentBlockMemberText); ok {
				content = append(content, map[string]any{"text": text.Value})
			}
		}
		document["source"] = map[string]any{"content": content}
	case *types.DocumentSourceMemberS3Location:
		document["source"] = s3SourceToJSON(src.Value)
	}
	return map[string]any{"type": "document", "document": document}
}

func audioBlockToJSON(b types.AudioBlock) map[string]any {
	audio := map[string]any{"format": string(b.Format)}
	switch src := b.Source.(type) {
	case *types.AudioSourceMemberBytes:
		audio["source"] = map[string]any{"bytes": base64.StdEncoding.EncodeToString(src.Value)}
	case *types.AudioSourceMemberS3Location:
		audio["source"] = s3SourceToJSON(src.Value)
	}
	return map[string]any{"type": "audio", "audio": audio}
}

func videoBlockToJSON(b types.VideoBlock) map[string]any {
	video := map[string]any{"format": string(b.Format)}
	switch src := b.Source.(type) {
	case *types.VideoSourceMemberBytes:
		video["source"] = map[string]any{"bytes": base64.StdEncoding.EncodeToString(src.Value)}
	case *types.VideoSourceMemberS3Location:
		video["source"] = s3SourceToJSON(src.Value)
	}
	return map[string]any{"type": "video", "video": video}
}

func s3SourceToJSON(location types.S3Location) map[string]any {
	s3 := map[string]any{}
	if location.Uri != nil {
		s3["uri"] = *location.Uri
	}
	if location.BucketOwner != nil {
		s3["bucket_owner"] = *location.BucketOwner
	}
	return map[string]any{"s3_location": s3}
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
	if input, ok := smithyDocumentToJSON(u.Input); ok {
		out["input"] = input
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
			jv, _ := smithyDocumentToJSON(v.Value)
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

// toolsToJSON normalizes function tools to the OpenAI tool-definition shape.
// Bedrock system tools remain provider-native because they are not functions.
func toolsToJSON(tools []types.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		switch v := tool.(type) {
		case *types.ToolMemberToolSpec:
			function := map[string]any{}
			if v.Value.Name != nil {
				function["name"] = *v.Value.Name
			}
			if v.Value.Description != nil {
				function["description"] = *v.Value.Description
			}
			if v.Value.Strict != nil {
				function["strict"] = *v.Value.Strict
			}
			if schema, ok := v.Value.InputSchema.(*types.ToolInputSchemaMemberJson); ok {
				if parameters, ok := smithyDocumentToJSON(schema.Value); ok {
					function["parameters"] = parameters
				}
			}
			out = append(out, map[string]any{"type": "function", "function": function})
		case *types.ToolMemberSystemTool:
			systemTool := map[string]any{"type": "system"}
			if v.Value.Name != nil {
				systemTool["name"] = *v.Value.Name
			}
			out = append(out, systemTool)
		case *types.ToolMemberCachePoint:
			cachePoint := map[string]any{"type": "cache_point", "cache_type": string(v.Value.Type)}
			if v.Value.Ttl != "" {
				cachePoint["ttl"] = string(v.Value.Ttl)
			}
			out = append(out, cachePoint)
		}
	}
	return out
}

func toolChoiceToJSON(tc types.ToolChoice) any {
	switch v := tc.(type) {
	case *types.ToolChoiceMemberAuto:
		return "auto"
	case *types.ToolChoiceMemberAny:
		return "required"
	case *types.ToolChoiceMemberTool:
		function := map[string]any{}
		if v.Value.Name != nil {
			function["name"] = *v.Value.Name
		}
		return map[string]any{"type": "function", "function": function}
	default:
		return nil
	}
}

func smithyDocumentToJSON(document bedrockdoc.Interface) (any, bool) {
	// Request documents created by document.NewLazyDocument are marshalers.
	// Marshal first so their original value is available before AWS sends it;
	// UnmarshalSmithyDocument is primarily reliable for response documents.
	if document == nil {
		return nil, false
	}
	data, err := document.MarshalSmithyDocument()
	if err != nil {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, false
	}
	return value, true
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
