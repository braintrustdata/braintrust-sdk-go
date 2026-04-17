package genkit

// This file provides a tracing decorator for the Firebase Genkit
// `ai.Embedder` interface. Genkit exposes `ai.ModelMiddleware` for generation
// but has no equivalent hook for embedders, so embedding calls cannot be
// traced through the middleware used elsewhere in this package. Instead, wrap
// the embedder with WrapEmbedder to create a Braintrust `llm` span around
// every Embed call.

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

// WithEmbedderTracerProvider sets a custom TracerProvider for the wrapped
// embedder. If not provided, the global otel.GetTracerProvider() is used.
func WithEmbedderTracerProvider(tp trace.TracerProvider) EmbedderOption {
	return func(c *embedderConfig) {
		c.tracerProvider = tp
	}
}

// WithEmbedderModel records the model name in span metadata. Genkit embedders
// register under a provider-qualified name (e.g. "openai/text-embedding-3-small")
// which is captured separately; pass the bare model name here when known.
func WithEmbedderModel(model string) EmbedderOption {
	return func(c *embedderConfig) {
		c.model = model
	}
}

// WithEmbedderProvider records the provider name (e.g. "openai") in span
// metadata. Defaults to "genkit".
func WithEmbedderProvider(provider string) EmbedderOption {
	return func(c *embedderConfig) {
		c.provider = provider
	}
}

// WrapEmbedder returns an ai.Embedder that traces each Embed call as a
// Braintrust `llm` span. The wrapper delegates Name, Embed, and Register to
// the underlying embedder.
func WrapEmbedder(embedder ai.Embedder, opts ...EmbedderOption) ai.Embedder {
	cfg := &embedderConfig{
		provider: "genkit",
	}
	for _, opt := range opts {
		opt(cfg)
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
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	out := map[string]any{
		"embeddings_count": len(resp.Embeddings),
	}
	if len(resp.Embeddings) > 0 && resp.Embeddings[0] != nil {
		out["embedding_length"] = len(resp.Embeddings[0].Embedding)
	}
	// Instrumentation errors from SetJSONAttr are silently dropped — the
	// embedding itself succeeded, and marking the span as errored would
	// misreport the caller's operation.
	_ = internal.SetJSONAttr(span, "braintrust.output_json", out)

	return resp, nil
}

// tagStart writes the span type, input (extracted from documents), and
// metadata. Instrumentation errors are silently dropped — the caller's
// embedding operation has not yet run, and marking the span as errored would
// misreport it.
func (e *tracedEmbedder) tagStart(span trace.Span, req *ai.EmbedRequest) {
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"})

	if input := embedInput(req); input != nil {
		_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
	}

	metadata := map[string]any{
		"provider": e.cfg.provider,
	}
	if e.cfg.model != "" {
		metadata["model"] = e.cfg.model
	}
	if name := e.inner.Name(); name != "" {
		metadata["embedder"] = name
	}
	if req != nil && req.Options != nil {
		metadata["options"] = req.Options
	}
	_ = internal.SetJSONAttr(span, "braintrust.metadata", metadata)
}

// embedInput extracts the text payload from an EmbedRequest for tracing.
// Single-document inputs are recorded as a bare string to match the
// convention used by the other embedding tracers in this SDK; multi-document
// inputs are recorded as a []string.
func embedInput(req *ai.EmbedRequest) any {
	if req == nil || len(req.Input) == 0 {
		return nil
	}

	texts := make([]string, 0, len(req.Input))
	for _, doc := range req.Input {
		texts = append(texts, documentText(doc))
	}

	if len(texts) == 1 {
		return texts[0]
	}
	return texts
}

// documentText concatenates the text content of a Document's parts, joining
// multiple text parts with a single newline.
func documentText(doc *ai.Document) string {
	if doc == nil {
		return ""
	}
	var parts []string
	for _, part := range doc.Content {
		if part == nil || part.Text == "" {
			continue
		}
		parts = append(parts, part.Text)
	}
	return strings.Join(parts, "\n")
}

// Compile-time assertion that tracedEmbedder implements the ai.Embedder interface.
var _ ai.Embedder = (*tracedEmbedder)(nil)
