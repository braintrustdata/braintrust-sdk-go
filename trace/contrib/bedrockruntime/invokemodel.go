package bedrockruntime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// invokeModelTracer handles the non-streaming InvokeModel operation.
//
// InvokeModel bodies are model-specific JSON; this tracer captures them
// best-effort as input_json / output_json. Token normalization runs only for
// Anthropic Claude models; other providers (Titan, Llama, Cohere) still get
// input/output logging but no metrics.
type invokeModelTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
	modelID  string
}

func (t *invokeModelTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.invoke_model", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "invoke_model",
	}

	params, ok := in.(*bedrockruntime.InvokeModelInput)
	if !ok || params == nil {
		return ctx, span
	}
	if params.ModelId != nil {
		t.modelID = *params.ModelId
		t.metadata["model"] = t.modelID
	}
	setAttrBytes(span, "braintrust.input_json", params.Body)
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
	setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	return ctx, span
}

func (t *invokeModelTracer) TagOutput(span trace.Span, out any, start time.Time) {
	defer span.End()
	timeToLast := time.Since(start).Seconds()

	resp, ok := out.(*bedrockruntime.InvokeModelOutput)
	if !ok || resp == nil {
		return
	}

	// Store the raw response JSON directly — no round-trip via decoded-and-
	// re-marshaled intermediate.
	setAttrBytes(span, "braintrust.output_json", resp.Body)

	metrics := map[string]any{"time_to_first_token": timeToLast}
	if usage := extractUsageFromRawBody(t.modelID, resp.Body); usage != nil {
		for k, v := range usage {
			metrics[k] = v
		}
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metrics", metrics)
}

// invokeModelStreamTracer handles the streaming variant of InvokeModel.
//
// Body chunks arrive as raw JSON bytes in `*types.PayloadPart.Bytes`. We
// accumulate them as they pass through and best-effort parse the full response
// at stream end. Only Claude models get token normalization.
type invokeModelStreamTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
	modelID  string
}

func (t *invokeModelStreamTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.invoke_model_stream", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "invoke_model_stream",
		"stream":   true,
	}

	params, ok := in.(*bedrockruntime.InvokeModelWithResponseStreamInput)
	if !ok || params == nil {
		return ctx, span
	}
	if params.ModelId != nil {
		t.modelID = *params.ModelId
		t.metadata["model"] = t.modelID
	}
	setAttrBytes(span, "braintrust.input_json", params.Body)
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
	setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	return ctx, span
}

func (t *invokeModelStreamTracer) TagOutput(span trace.Span, _ any, start time.Time) {
	// Stream-wrapping for InvokeModelWithResponseStream isn't implemented yet;
	// the bidirectional-style reader interface differs from ConverseStream.
	// Close out the span with a minimal metric so users still see it.
	defer span.End()
	metrics := map[string]any{"time_to_first_token": time.Since(start).Seconds()}
	setJSONAttr(t.cfg.logger, span, "braintrust.metrics", metrics)
}

// setAttrBytes writes JSON bytes directly onto the span as a string
// attribute, skipping the decode + re-marshal round-trip that SetJSONAttr
// would do. Invalid or empty bodies are ignored.
func setAttrBytes(span trace.Span, key string, body []byte) {
	if len(body) == 0 || !json.Valid(body) {
		return
	}
	span.SetAttributes(attribute.String(key, string(body)))
}

// extractUsageFromRawBody decodes Claude's Messages-format response usage
// field into normalized Braintrust metrics. Returns nil for non-Claude models
// or malformed bodies.
func extractUsageFromRawBody(modelID string, body []byte) map[string]any {
	if !strings.Contains(strings.ToLower(modelID), "anthropic.claude") {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	return extractUsageForModel(modelID, decoded)
}

// extractUsageForModel normalizes token metrics from an already-decoded
// Claude response body. Exported to the package-level test for coverage of
// the dispatch logic.
func extractUsageForModel(modelID string, body any) map[string]any {
	if body == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(modelID), "anthropic.claude") {
		return nil
	}
	m, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := m["usage"].(map[string]any)
	if !ok {
		return nil
	}

	// Reuse the anthropic-style map-based normalization.
	metrics := make(map[string]any)
	var input, cacheRead, cacheCreate int64
	for k, v := range usage {
		ok, i := internal.ToInt64(v)
		if !ok {
			continue
		}
		switch k {
		case "input_tokens":
			input = i
		case "cache_creation_input_tokens":
			cacheCreate = i
			metrics["prompt_cache_creation_tokens"] = i
		case "cache_read_input_tokens":
			cacheRead = i
			metrics["prompt_cached_tokens"] = i
		case "output_tokens":
			metrics["completion_tokens"] = i
		}
	}
	promptTotal := input + cacheRead + cacheCreate
	metrics["prompt_tokens"] = promptTotal
	if completion, ok := metrics["completion_tokens"].(int64); ok {
		metrics["tokens"] = promptTotal + completion
	}
	return metrics
}
