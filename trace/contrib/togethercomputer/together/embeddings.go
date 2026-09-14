package together

// this file parses the embeddings API.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// embeddingsTracer is a tracer for the Together AI embeddings POST endpoint.
type embeddingsTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
}

func newEmbeddingsTracer(cfg *middlewareConfig) *embeddingsTracer {
	return &embeddingsTracer{
		cfg: cfg,
		metadata: map[string]any{
			"provider": "together",
		},
	}
}

func (et *embeddingsTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := et.cfg.tracer().Start(ctx, "together.embeddings.create", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	if model, ok := raw["model"]; ok {
		et.metadata["model"] = model
	}

	if input, ok := raw["input"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", input); err != nil {
			return ctx, span, err
		}
	}

	return ctx, span, internal.SetJSONAttr(span, "braintrust.metadata", et.metadata)
}

func (et *embeddingsTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]any
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	if model, ok := raw["model"]; ok {
		et.metadata["model"] = model
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return err
	}

	return internal.SetJSONAttr(span, "braintrust.output_json", embeddingsOutputSummary(raw))
}

// embeddingsOutputSummary reports only the number and dimension of the
// returned embeddings, omitting the raw vectors.
func embeddingsOutputSummary(raw map[string]any) map[string]any {
	out := map[string]any{"count": 0}
	data, ok := raw["data"].([]any)
	if !ok {
		return out
	}
	out["count"] = len(data)
	if len(data) == 0 {
		return out
	}
	if first, ok := data[0].(map[string]any); ok {
		if emb, ok := first["embedding"].([]any); ok {
			out["embedding_length"] = len(emb)
		}
	}
	return out
}
