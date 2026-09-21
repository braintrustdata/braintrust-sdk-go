package cloudflare

// this file parses the Workers AI accounts/{account_id}/ai/run/{model} endpoint.
//
// Workers AI runs about 14 different task types (text generation, text and
// multimodal embeddings, image classification, object detection, speech,
// translation, summarization, image captioning, text-to-image, ...) through
// this one endpoint. Two pairs of request shapes are genuinely
// indistinguishable on the wire (a bare {"image": [...]} body is either
// image classification or object detection; a bare {"text": "..."} body is
// either text classification or single-string text embeddings), so this
// tracer does not attempt to classify the task from the request at all.
//
// Instead it classifies from the RESPONSE, which is always unambiguous:
// {"data": [...], "shape": [...]} is an embeddings result (text or
// multimodal, they're identical on the wire), a response with a "usage"
// key is a text generation result (its "choices" array is already valid
// OpenAI Chat Completions shape, per the Braintrust instrumentation spec's
// requirement to convert non-OpenAI/Anthropic/Google providers into that
// format), and everything else (classification/detection arrays,
// translation, summarization, speech, captioning, ...) is logged as-is,
// since the spec's completion-API conversion rules don't apply to these
// non-chat task types (they're TODO in the spec as of this writing) and
// Cloudflare's own field names are already a reasonable output shape.
//
// Text-to-image and text-to-speech models return raw binary (PNG/MP3), not
// a JSON envelope, so those spans get a placeholder output instead of an
// error. Streaming (`stream: true`) isn't handled: cloudflare-go v7's
// AI.Run has no streaming variant, so the Go SDK itself can't return an SSE
// stream from this call today.
//
// Known gaps against the instrumentation spec, left for follow-up:
//   - Attachments: image/audio bytes in requests or responses aren't
//     uploaded as braintrust_attachment references (spec section
//     "Multimodal / Attachments"). Oversized binary fields are currently
//     just summarized to {type, length} via truncateLargeFields so a span
//     never carries megabytes of raw bytes, but that's not the same thing
//     as a real attachment upload.
//   - The ~10 non-chat, non-embedding task types (classification,
//     detection, translation, summarization, speech, captioning, image
//     generation) aren't converted to any canonical shape, because the
//     spec doesn't define one for them yet (its "Multimodal API surfaces"
//     and "Embedding APIs" sections are still TODO). They're logged as
//     Cloudflare's own native response shape.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// maxInlineArrayLen and maxInlineStringLen bound how much of a raw
// image/audio payload (pixel arrays, base64 blobs) gets copied into a span.
// Above this, only a summary is recorded. This is a stopgap, not a
// substitute for real attachment upload (see file doc comment).
const (
	maxInlineArrayLen  = 64
	maxInlineStringLen = 2048
)

// aiRunTracer is a tracer for the Workers AI ai/run/{model} endpoint.
type aiRunTracer struct {
	cfg      *middlewareConfig
	model    string
	metadata map[string]any
}

func newAIRunTracer(cfg *middlewareConfig, model string) *aiRunTracer {
	return &aiRunTracer{
		cfg:   cfg,
		model: model,
		metadata: map[string]any{
			"provider": "cloudflare",
			// Overwritten in TagSpan with the API response's own model
			// string when the task type reports one (text generation
			// does); this is just the fallback.
			"model": model,
		},
	}
}

func (rt *aiRunTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, nil, err
	}

	// The task isn't known yet (see file doc comment), so the span starts
	// with a generic name; TagSpan renames it once the response reveals
	// the task.
	ctx, span := rt.cfg.tracer().Start(ctx, "cloudflare.ai.run", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	// The instrumentation spec requires chat input to be exactly the
	// OpenAI-style messages array, not the whole request body. Every other
	// task type has no spec-defined input shape yet, so the whole
	// (truncated) body is logged for those.
	if messages, ok := raw["messages"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", truncateLargeFields(messages)); err != nil {
			return ctx, span, err
		}
	} else if err := internal.SetJSONAttr(span, "braintrust.input_json", truncateLargeFields(raw)); err != nil {
		return ctx, span, err
	}

	// Fields the instrumentation spec explicitly allows in metadata.
	for _, field := range []string{"max_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty", "response_format"} {
		if v, ok := raw[field]; ok {
			rt.metadata[field] = v
		}
	}
	// Domain-specific fields beyond the spec's base allowlist, useful for
	// Workers AI callers: top_k/seed control generation determinism, and
	// stream/task tell the reader what kind of call this was.
	for _, field := range []string{"top_k", "seed", "stream"} {
		if v, ok := raw[field]; ok {
			rt.metadata[field] = v
		}
	}

	if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
		rt.metadata["tools"] = convertToolDefinitions(tools)
	}

	return ctx, span, internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata)
}

// convertToolDefinitions converts Workers AI's tool definitions into the
// OpenAI Chat Completions shape the instrumentation spec requires
// (metadata.tools = [{type:"function", function:{name,description,parameters}}]).
// Workers AI's API accepts three different wire shapes for a tool
// definition (flat, an alternate flat "object" shape, and one already
// nested under "function"); this handles all three.
func convertToolDefinitions(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			// Already OpenAI-nested on the wire.
			out = append(out, map[string]any{"type": "function", "function": fn})
			continue
		}
		fn := map[string]any{}
		if name, ok := tool["name"]; ok {
			fn["name"] = name
		}
		if desc, ok := tool["description"]; ok {
			fn["description"] = desc
		}
		if params, ok := tool["parameters"]; ok {
			fn["parameters"] = params
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func (rt *aiRunTracer) TagSpan(span trace.Span, body io.Reader) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(body).Decode(&envelope); err != nil {
		return rt.tagNonJSONResponse(span)
	}

	var result any
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return rt.tagNonJSONResponse(span)
	}

	resultObj, isObject := result.(map[string]any)
	_, hasData := resultObj["data"]
	_, hasShape := resultObj["shape"]
	// A tool-calling-only reply sets "response" to JSON null rather than
	// omitting it, so key presence is checked instead of a non-nil value.
	// "usage" is only ever present on text generation replies.
	_, hasUsage := resultObj["usage"]
	switch {
	case isObject && hasData && hasShape:
		span.SetName("cloudflare.ai.embeddings")
		rt.metadata["task"] = "embeddings"
		return rt.tagEmbeddings(span, resultObj)
	case isObject && hasUsage:
		span.SetName("cloudflare.ai.text_generation")
		rt.metadata["task"] = "text_generation"
		return rt.tagTextGeneration(span, resultObj)
	default:
		// Classification/detection arrays, translation, summarization,
		// speech, captioning, and anything else: no spec-defined shape
		// exists for these yet, so Cloudflare's own field names are
		// logged as-is.
		if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
			return err
		}
		return internal.SetJSONAttr(span, "braintrust.output_json", truncateLargeFields(result))
	}
}

// tagNonJSONResponse handles text-to-image and text-to-speech, which return
// raw binary (PNG/MP3) instead of a JSON envelope.
func (rt *aiRunTracer) tagNonJSONResponse(span trace.Span) error {
	return internal.SetJSONAttr(span, "braintrust.output_json", map[string]any{
		"note": "binary response (e.g. image or audio) not captured",
	})
}

// tagTextGeneration uses result.choices directly as output: Workers AI's
// chat models already return it in valid OpenAI Chat Completions shape
// (index, finish_reason, message.role/content/tool_calls with tool_calls[].id
// /type/function.name/function.arguments-as-a-JSON-string), which is exactly
// what the instrumentation spec requires for a non-OpenAI/Anthropic/Google
// provider. Cloudflare-specific null fields inside choices/message (audio,
// reasoning, routed_experts, ...) are left in place; the spec doesn't
// forbid extra fields.
func (rt *aiRunTracer) tagTextGeneration(span trace.Span, result map[string]any) error {
	if model, ok := result["model"].(string); ok && model != "" {
		rt.metadata["model"] = model
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
		return err
	}
	if err := internal.SetJSONAttr(span, "braintrust.output_json", result["choices"]); err != nil {
		return err
	}

	metrics := map[string]any{}
	if usage, ok := result["usage"].(map[string]any); ok {
		if ok, v := internal.ToInt64(usage["prompt_tokens"]); ok {
			metrics["prompt_tokens"] = v
		}
		if ok, v := internal.ToInt64(usage["completion_tokens"]); ok {
			metrics["completion_tokens"] = v
		}
		if ok, v := internal.ToInt64(usage["total_tokens"]); ok {
			metrics["tokens"] = v
		}
	}
	return internal.SetJSONAttr(span, "braintrust.metrics", metrics)
}

// tagEmbeddings covers both text and multimodal embeddings: both return the
// identical {data, shape} shape, so the raw vectors are summarized to a
// count rather than copied in full, matching this repo's other embedding
// integrations (genai, genkit, eino, langchaingo all emit {"count": N}).
func (rt *aiRunTracer) tagEmbeddings(span trace.Span, result map[string]any) error {
	if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
		return err
	}

	count := 0
	if data, ok := result["data"].([]any); ok {
		count = len(data)
	}
	return internal.SetJSONAttr(span, "braintrust.output_json", map[string]any{"count": count})
}

// truncateLargeFields replaces oversized arrays and strings (raw image
// pixel data, audio samples, base64 blobs) with a short summary, so a span
// doesn't end up carrying megabytes of binary data as JSON. This is a
// stopgap, not attachment upload (see file doc comment).
func truncateLargeFields(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, field := range val {
			out[k] = truncateLargeFields(field)
		}
		return out
	case []any:
		if len(val) > maxInlineArrayLen {
			return map[string]any{"type": "array", "length": len(val)}
		}
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = truncateLargeFields(item)
		}
		return out
	case string:
		if len(val) > maxInlineStringLen {
			return map[string]any{"type": "string", "length": len(val)}
		}
		return val
	default:
		return val
	}
}

// Ensure our tracer implements the shared interface.
var _ internal.MiddlewareTracer = &aiRunTracer{}
