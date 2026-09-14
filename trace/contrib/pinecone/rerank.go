package pinecone

// this file parses the Inference API's POST /rerank endpoint.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// rerankTracer is a tracer for the Pinecone Inference /rerank endpoint.
type rerankTracer struct {
	cfg      *config
	metadata map[string]any
}

func newRerankTracer(cfg *config) *rerankTracer {
	return &rerankTracer{
		cfg:      cfg,
		metadata: map[string]any{"provider": "pinecone"},
	}
}

func (rt *rerankTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := rt.cfg.tracer().Start(ctx, "pinecone.rerank", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	if model, ok := raw["model"].(string); ok {
		rt.metadata["model"] = model
	}

	if err := internal.SetJSONAttr(span, "braintrust.input_json", raw); err != nil {
		return ctx, span, err
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (rt *rerankTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]any
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
		return err
	}
	if err := internal.SetJSONAttr(span, "braintrust.output_json", raw["data"]); err != nil {
		return err
	}

	metrics := map[string]any{}
	if usage, ok := raw["usage"].(map[string]any); ok {
		if ok, units := internal.ToInt64(usage["rerank_units"]); ok {
			metrics["rerank_units"] = units
		}
	}
	return internal.SetJSONAttr(span, "braintrust.metrics", metrics)
}

// Ensure our tracer implements the shared interface.
var _ internal.MiddlewareTracer = &rerankTracer{}
