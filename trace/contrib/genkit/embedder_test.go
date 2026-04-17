package genkit

import (
	"context"
	"errors"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// fakeEmbedder is a deterministic ai.Embedder used by the tests.
type fakeEmbedder struct {
	name    string
	dim     int
	callErr error
}

func (f *fakeEmbedder) Name() string { return f.name }

func (f *fakeEmbedder) Embed(_ context.Context, req *ai.EmbedRequest) (*ai.EmbedResponse, error) {
	if f.callErr != nil {
		return nil, f.callErr
	}
	embeddings := make([]*ai.Embedding, len(req.Input))
	for i := range req.Input {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim; j++ {
			v[j] = float32(i + j)
		}
		embeddings[i] = &ai.Embedding{Embedding: v}
	}
	return &ai.EmbedResponse{Embeddings: embeddings}, nil
}

func (f *fakeEmbedder) Register(_ api.Registry) {}

func TestWrapEmbedderBatch(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	texts := []string{"hello world", "braintrust tracing", "go sdk"}
	docs := make([]*ai.Document, len(texts))
	for i, text := range texts {
		docs[i] = ai.DocumentFromText(text, nil)
	}

	inner := &fakeEmbedder{name: "openai/text-embedding-3-small", dim: 8}
	wrapped := WrapEmbedder(inner,
		WithEmbedderTracerProvider(tp),
		WithEmbedderModel("text-embedding-3-small"),
		WithEmbedderProvider("openai"),
	)

	resp, err := wrapped.Embed(context.Background(), &ai.EmbedRequest{Input: docs})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 3)
	assert.Len(t, resp.Embeddings[0].Embedding, 8)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.embed")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inp := span.Input()
	arr, ok := inp.([]interface{})
	require.True(t, ok)
	require.Len(t, arr, 3)
	assert.Equal(t, "hello world", arr[0])

	out := span.Output()
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(8), outMap["embedding_length"])
	assert.Equal(t, float64(3), outMap["embeddings_count"])

	metadata := span.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "text-embedding-3-small", metadata["model"])
}

func TestWrapEmbedderSingle(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	inner := &fakeEmbedder{name: "openai/text-embedding-3-small", dim: 16}
	wrapped := WrapEmbedder(inner, WithEmbedderTracerProvider(tp))

	resp, err := wrapped.Embed(context.Background(), &ai.EmbedRequest{
		Input: []*ai.Document{ai.DocumentFromText("what is braintrust?", nil)},
	})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 1)
	assert.Len(t, resp.Embeddings[0].Embedding, 16)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.embed")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inp := span.Input()
	assert.Equal(t, "what is braintrust?", inp)

	out := span.Output()
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(16), outMap["embedding_length"])
	assert.Equal(t, float64(1), outMap["embeddings_count"])

	metadata := span.Metadata()
	assert.Equal(t, "genkit", metadata["provider"])
	assert.Equal(t, "openai/text-embedding-3-small", metadata["embedder"])
}

func TestWrapEmbedderError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	wantErr := errors.New("rate limit exceeded")
	wrapped := WrapEmbedder(&fakeEmbedder{name: "x", callErr: wantErr},
		WithEmbedderTracerProvider(tp),
	)

	_, err := wrapped.Embed(context.Background(), &ai.EmbedRequest{
		Input: []*ai.Document{ai.DocumentFromText("x", nil)},
	})
	require.Error(t, err)
	assert.Equal(t, wantErr, err)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.embed")
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
}

func TestWrapEmbedderName(t *testing.T) {
	inner := &fakeEmbedder{name: "openai/text-embedding-3-small"}
	wrapped := WrapEmbedder(inner)
	assert.Equal(t, "openai/text-embedding-3-small", wrapped.Name())
}
