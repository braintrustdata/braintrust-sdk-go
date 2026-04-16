package openai

// this file parses the embeddings API.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// embeddingsTracer is a tracer for the openai v1/embeddings POST endpoint.
// See docs here: https://platform.openai.com/docs/api-reference/embeddings/create
type embeddingsTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
}

func newEmbeddingsTracer(cfg *middlewareConfig) *embeddingsTracer {
	return &embeddingsTracer{
		cfg: cfg,
		metadata: map[string]any{
			"provider": "openai",
			"endpoint": "/v1/embeddings",
		},
	}
}

func (et *embeddingsTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := et.cfg.tracer().Start(
		ctx,
		"openai.embeddings.create",
		trace.WithTimestamp(t),
	)

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	metadataFields := []string{
		"model",
		"encoding_format",
		"dimensions",
		"user",
	}
	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			et.metadata[field] = value
		}
	}

	if input, ok := raw["input"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", input); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (et *embeddingsTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	for _, field := range []string{"model", "object"} {
		if v, ok := raw[field]; ok {
			et.metadata[field] = v
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return err
	}

	output := embeddingsOutputSummary(raw)
	if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
		return err
	}

	metrics := make(map[string]any)
	if usage, ok := raw["usage"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	return nil
}

// embeddingsOutputSummary returns shape-only metadata about embeddings to match
// the Python SDK convention, which records {"embedding_length": N} instead of
// the full vectors.
//
// When the request set encoding_format=base64, the embedding arrives as a
// string rather than a []float; we can't extract a dimension count and return
// an empty map, matching the Python SDK's behavior.
func embeddingsOutputSummary(raw map[string]interface{}) map[string]any {
	out := map[string]any{}
	data, ok := raw["data"].([]interface{})
	if !ok || len(data) == 0 {
		return out
	}
	first, ok := data[0].(map[string]interface{})
	if !ok {
		return out
	}
	if emb, ok := first["embedding"].([]interface{}); ok {
		out["embedding_length"] = len(emb)
	}
	return out
}
