package pinecone

// this file parses the Inference API's POST /embed endpoint.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// embedTracer is a tracer for the Pinecone Inference /embed endpoint.
type embedTracer struct {
	cfg      *config
	metadata map[string]any
}

func newEmbedTracer(cfg *config) *embedTracer {
	return &embedTracer{
		cfg:      cfg,
		metadata: map[string]any{"provider": "pinecone"},
	}
}

func (et *embedTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := et.cfg.tracer().Start(ctx, "pinecone.embed", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	if model, ok := raw["model"].(string); ok {
		et.metadata["model"] = model
	}

	if err := internal.SetJSONAttr(span, "braintrust.input_json", canonicalEmbedInput(raw)); err != nil {
		return ctx, span, err
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (et *embedTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]any
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return err
	}
	if err := internal.SetJSONAttr(span, "braintrust.output_json", embedOutputSummary(raw)); err != nil {
		return err
	}

	metrics := map[string]any{}
	if usage, ok := raw["usage"].(map[string]any); ok {
		if ok, tokens := internal.ToInt64(usage["total_tokens"]); ok {
			metrics["tokens"] = tokens
		}
	}
	return internal.SetJSONAttr(span, "braintrust.metrics", metrics)
}

// canonicalEmbedInput extracts the input texts and parameters. Unlike the
// output embeddings, inputs are plain strings, so nothing here needs summarizing.
func canonicalEmbedInput(raw map[string]any) map[string]any {
	texts := []any{}
	if inputs, ok := raw["inputs"].([]any); ok {
		for _, rawInput := range inputs {
			input, ok := rawInput.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := input["text"]; ok {
				texts = append(texts, text)
			}
		}
	}

	input := map[string]any{"inputs": texts}
	if params, ok := raw["parameters"]; ok {
		input["parameters"] = params
	}
	return input
}

// embedOutputSummary intentionally reports only the number of returned
// embeddings and their vector type. Raw embedding vectors are omitted.
func embedOutputSummary(raw map[string]any) map[string]any {
	count := 0
	if data, ok := raw["data"].([]any); ok {
		count = len(data)
	}
	summary := map[string]any{"count": count}
	if vectorType, ok := raw["vector_type"].(string); ok {
		summary["vector_type"] = vectorType
	}
	return summary
}

// Ensure our tracer implements the shared interface.
var _ internal.MiddlewareTracer = &embedTracer{}
