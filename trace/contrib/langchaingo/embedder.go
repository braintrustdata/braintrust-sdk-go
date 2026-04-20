package langchaingo

// This file provides a tracing decorator for the LangChainGo
// `embeddings.Embedder` interface. The LangChainGo callbacks.Handler interface
// has no embedding hooks, so embedding calls cannot be traced through the
// callback system used elsewhere in this package. Instead, wrap the embedder
// with WrapEmbedder to create a Braintrust `llm` span around every
// EmbedDocuments / EmbedQuery call.

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tmc/langchaingo/embeddings"

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

// WithEmbedderModel records the model name in span metadata. LangChainGo does
// not expose the model from the embedder, so pass it here when known.
func WithEmbedderModel(model string) EmbedderOption {
	return func(c *embedderConfig) {
		c.model = model
	}
}

// WithEmbedderProvider records the provider name (e.g. "openai") in span
// metadata. Defaults to "langchain".
func WithEmbedderProvider(provider string) EmbedderOption {
	return func(c *embedderConfig) {
		c.provider = provider
	}
}

// WrapEmbedder returns an embeddings.Embedder that traces each EmbedDocuments
// and EmbedQuery call as a Braintrust `llm` span. The wrapper delegates all
// actual embedding work to the underlying embedder.
func WrapEmbedder(embedder embeddings.Embedder, opts ...EmbedderOption) embeddings.Embedder {
	cfg := &embedderConfig{
		provider: "langchain",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return &tracedEmbedder{inner: embedder, cfg: cfg}
}

type tracedEmbedder struct {
	inner embeddings.Embedder
	cfg   *embedderConfig
}

func (e *tracedEmbedder) tracer() trace.Tracer {
	tp := e.cfg.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

func (e *tracedEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, span := e.tracer().Start(ctx, "langchain.embedder.embed_documents")
	defer span.End()

	e.tagStart(span, texts)

	vectors, err := e.inner.EmbedDocuments(ctx, texts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	out := map[string]any{
		"embeddings_count": len(vectors),
	}
	if len(vectors) > 0 {
		out["embedding_length"] = len(vectors[0])
	}
	// Instrumentation errors from SetJSONAttr are silently dropped — the
	// embedding itself succeeded, and marking the span as errored would
	// misreport the caller's operation.
	_ = internal.SetJSONAttr(span, "braintrust.output_json", out)

	return vectors, nil
}

func (e *tracedEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	ctx, span := e.tracer().Start(ctx, "langchain.embedder.embed_query")
	defer span.End()

	e.tagStart(span, text)

	vector, err := e.inner.EmbedQuery(ctx, text)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	out := map[string]any{
		"embedding_length": len(vector),
	}
	_ = internal.SetJSONAttr(span, "braintrust.output_json", out)

	return vector, nil
}

// tagStart writes the common span attributes shared by EmbedDocuments and
// EmbedQuery: span type, input, and metadata. Instrumentation errors are
// silently dropped — the caller's embedding operation has not yet run or has
// already succeeded, and marking the span as errored would misreport it.
func (e *tracedEmbedder) tagStart(span trace.Span, input any) {
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	_ = internal.SetJSONAttr(span, "braintrust.input_json", input)

	metadata := map[string]any{
		"provider": e.cfg.provider,
	}
	if e.cfg.model != "" {
		metadata["model"] = e.cfg.model
	}
	_ = internal.SetJSONAttr(span, "braintrust.metadata", metadata)
}

// Compile-time assertion that tracedEmbedder implements the Embedder interface.
var _ embeddings.Embedder = (*tracedEmbedder)(nil)
