package genai

// this file parses the embedContent / batchEmbedContents API.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// embedContentTracer is a tracer for the Gemini :embedContent and
// :batchEmbedContents endpoints (Gemini API + Vertex AI).
type embedContentTracer struct {
	cfg      *config
	metadata map[string]any
	model    string
}

func newEmbedContentTracer(cfg *config, model string) *embedContentTracer {
	return &embedContentTracer{
		cfg:   cfg,
		model: model,
		metadata: map[string]any{
			"provider": "gemini",
		},
	}
}

func (et *embedContentTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := et.cfg.tracer().Start(
		ctx,
		"embed_content",
		trace.WithTimestamp(t),
	)

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	if et.model != "" {
		et.metadata["model"] = et.model
	}

	// Fields can live at the top level (legacy :embedContent body) or inside
	// each element of `requests` (the `batchEmbedContents` body that the Go
	// SDK always sends, even for a single input).
	metaFields := []string{"taskType", "title", "outputDimensionality"}
	for _, field := range metaFields {
		if v, ok := raw[field]; ok {
			et.metadata[field] = v
		}
	}
	if reqs, ok := raw["requests"].([]interface{}); ok && len(reqs) > 0 {
		if first, ok := reqs[0].(map[string]interface{}); ok {
			for _, field := range metaFields {
				if _, already := et.metadata[field]; already {
					continue
				}
				if v, ok := first[field]; ok {
					et.metadata[field] = v
				}
			}
		}
	}

	inputLog := map[string]any{}
	if et.model != "" {
		inputLog["model"] = et.model
	}
	if reqs, ok := raw["requests"]; ok {
		inputLog["requests"] = reqs
	} else if content, ok := raw["content"]; ok {
		inputLog["content"] = content
	}
	if len(inputLog) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", inputLog); err != nil {
			return ctx, span, err
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (et *embedContentTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return err
	}

	output := embedContentOutputSummary(raw)
	if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
		return err
	}

	metrics := make(map[string]any)
	if usage, ok := raw["usageMetadata"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	return nil
}

// embedContentOutputSummary returns shape-only metadata about embeddings to
// match the Python SDK convention for Google GenAI:
//
//	{"embedding_length": N, "embeddings_count": M}.
//
// For single-input responses (`{"embedding": {"values": [...]}}`),
// embeddings_count is 1. For batch responses (`{"embeddings": [...]}`) it is
// the length of the list.
func embedContentOutputSummary(raw map[string]interface{}) map[string]any {
	out := map[string]any{}
	// Single-input embedContent response
	if emb, ok := raw["embedding"].(map[string]interface{}); ok {
		if values, ok := emb["values"].([]interface{}); ok {
			out["embedding_length"] = len(values)
			out["embeddings_count"] = 1
			return out
		}
	}
	// Batch response
	if list, ok := raw["embeddings"].([]interface{}); ok {
		out["embeddings_count"] = len(list)
		if len(list) > 0 {
			if first, ok := list[0].(map[string]interface{}); ok {
				if values, ok := first["values"].([]interface{}); ok {
					out["embedding_length"] = len(values)
				}
			}
		}
	}
	return out
}

// Ensure our tracer implements the shared interface
var _ internal.MiddlewareTracer = &embedContentTracer{}
