package together

// this file parses the (non-chat) completions API.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// completionsTracer is a tracer for the Together AI completions POST endpoint.
type completionsTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
}

func newCompletionsTracer(cfg *middlewareConfig) *completionsTracer {
	return &completionsTracer{
		cfg: cfg,
		metadata: map[string]any{
			"provider": "together",
		},
	}
}

func (ct *completionsTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := ct.cfg.tracer().Start(ctx, "together.completions.create", trace.WithTimestamp(t))

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]any
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	metadataFields := []string{
		"model", "max_tokens", "stop", "temperature", "top_p", "top_k",
		"repetition_penalty", "n", "logprobs", "seed", "min_p",
	}
	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			ct.metadata[field] = value
		}
	}

	if prompt, ok := raw["prompt"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.input_json", prompt); err != nil {
			return ctx, span, err
		}
	}

	return ctx, span, internal.SetJSONAttr(span, "braintrust.metadata", ct.metadata)
}

func (ct *completionsTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]any
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	for _, field := range []string{"id", "object", "created"} {
		if v, ok := raw[field]; ok {
			ct.metadata[field] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", ct.metadata); err != nil {
		return err
	}

	metrics := map[string]any{}
	if usage, ok := raw["usage"].(map[string]any); ok {
		for k, v := range parseUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	if choices, ok := raw["choices"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", choices); err != nil {
			return err
		}
	}

	return nil
}
