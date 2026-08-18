package genkit

// Genkit has no embedder middleware hook, so WrapEmbedder decorates an
// ai.Embedder and emits one Braintrust llm span per embedding request.

import (
	"context"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// EmbedderOption configures a traced embedder.
type EmbedderOption func(*embedderConfig)

type embedderConfig struct {
	tracerProvider trace.TracerProvider
	model          string
	provider       string
}

// WithEmbedderTracerProvider sets a custom TracerProvider.
func WithEmbedderTracerProvider(tp trace.TracerProvider) EmbedderOption {
	return func(c *embedderConfig) { c.tracerProvider = tp }
}

// WithEmbedderModel records the model name in span metadata.
func WithEmbedderModel(model string) EmbedderOption {
	return func(c *embedderConfig) { c.model = model }
}

// WithEmbedderProvider records the underlying provider whose pricing applies.
func WithEmbedderProvider(provider string) EmbedderOption {
	return func(c *embedderConfig) { c.provider = provider }
}

// WrapEmbedder returns an ai.Embedder that traces each Embed call.
func WrapEmbedder(embedder ai.Embedder, opts ...EmbedderOption) ai.Embedder {
	cfg := &embedderConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if embedder != nil {
		provider, model := embedderIdentity(embedder.Name())
		if cfg.provider == "" {
			cfg.provider = provider
		}
		if cfg.model == "" {
			cfg.model = model
		}
	}
	return &tracedEmbedder{inner: embedder, cfg: cfg}
}

type tracedEmbedder struct {
	inner ai.Embedder
	cfg   *embedderConfig
}

func (e *tracedEmbedder) Name() string { return e.inner.Name() }

func (e *tracedEmbedder) Register(r api.Registry) { e.inner.Register(r) }

func (e *tracedEmbedder) tracer() trace.Tracer {
	tp := e.cfg.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

func (e *tracedEmbedder) Embed(ctx context.Context, req *ai.EmbedRequest) (*ai.EmbedResponse, error) {
	ctx, span := e.tracer().Start(ctx, "genkit.embed", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	e.tagStart(span, req)
	resp, err := e.inner.Embed(ctx, req)
	count := 0
	if resp != nil {
		count = len(resp.Embeddings)
	}
	_ = internal.SetJSONAttr(span, "braintrust.output_json", map[string]any{"count": count})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}
	return resp, nil
}

func (e *tracedEmbedder) tagStart(span trace.Span, req *ai.EmbedRequest) {
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	if input := embedInput(req); input != nil {
		_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
	}
	metadata := map[string]any{}
	if e.cfg.provider != "" {
		metadata["provider"] = e.cfg.provider
	}
	if e.cfg.model != "" {
		metadata["model"] = e.cfg.model
	}
	_ = internal.SetJSONAttr(span, "braintrust.metadata", metadata)
}

// embedInput implements the canonical Braintrust embedding input schema.
func embedInput(req *ai.EmbedRequest) any {
	if req == nil || len(req.Input) == 0 {
		return nil
	}
	inputs := make([]map[string]any, 0, len(req.Input))
	for _, document := range req.Input {
		inputs = append(inputs, map[string]any{"content": embedDocumentContent(document)})
	}
	input := map[string]any{"inputs": inputs}
	if dimensions, ok := embedDimensions(req.Options); ok {
		input["output_dimensions"] = dimensions
	}
	return input
}

func embedDocumentContent(document *ai.Document) any {
	if document == nil || len(document.Content) == 0 {
		return ""
	}
	if len(document.Content) == 1 && document.Content[0] != nil && document.Content[0].IsText() {
		return document.Content[0].Text
	}
	parts := make([]map[string]any, 0, len(document.Content))
	for _, part := range document.Content {
		if part == nil {
			continue
		}
		switch {
		case part.IsText(), part.IsData():
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		case part.IsImage():
			parts = append(parts, map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": part.Text},
			})
		case part.IsMedia():
			parts = append(parts, map[string]any{
				"type": "file", "file": map[string]any{"file_data": part.Text},
			})
		case part.IsResource() && part.Resource != nil:
			parts = append(parts, map[string]any{
				"type": "file", "file": map[string]any{"file_data": part.Resource.Uri},
			})
		}
	}
	return parts
}

func embedDimensions(options any) (int, bool) {
	config := normalizedConfig(options)
	return firstInt(config, "output_dimensions", "outputDimensions", "dimensions", "Dimensions")
}

func embedderIdentity(name string) (string, string) {
	provider, model, ok := strings.Cut(name, "/")
	if !ok {
		return "", name
	}
	if provider == "googleai" {
		provider = "google"
	}
	return provider, model
}

var _ ai.Embedder = (*tracedEmbedder)(nil)
