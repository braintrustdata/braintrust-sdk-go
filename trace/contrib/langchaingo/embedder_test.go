package langchaingo

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// fakeEmbedder is a deterministic Embedder used by the tests.
type fakeEmbedder struct {
	dim     int
	callErr error
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if f.callErr != nil {
		return nil, f.callErr
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim; j++ {
			v[j] = float32(i + j)
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	if f.callErr != nil {
		return nil, f.callErr
	}
	v := make([]float32, f.dim)
	for j := 0; j < f.dim; j++ {
		v[j] = float32(j)
	}
	return v, nil
}

func TestWrapEmbedderEmbedDocuments(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	texts := []string{"hello world", "braintrust tracing", "go sdk"}
	inner := &fakeEmbedder{dim: 8}
	wrapped := WrapEmbedder(inner,
		WithEmbedderTracerProvider(tp),
		WithEmbedderModel("text-embedding-3-small"),
		WithEmbedderProvider("openai"),
	)

	vectors, err := wrapped.EmbedDocuments(context.Background(), texts)
	require.NoError(t, err)
	require.Len(t, vectors, 3)
	assert.Len(t, vectors[0], 8)

	span := exporter.FlushOne()
	span.AssertNameIs("langchain.embedder.embed_documents")
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

func TestWrapEmbedderEmbedQuery(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	inner := &fakeEmbedder{dim: 16}
	wrapped := WrapEmbedder(inner, WithEmbedderTracerProvider(tp))

	vector, err := wrapped.EmbedQuery(context.Background(), "what is braintrust?")
	require.NoError(t, err)
	require.Len(t, vector, 16)

	span := exporter.FlushOne()
	span.AssertNameIs("langchain.embedder.embed_query")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inp := span.Input()
	arr, ok := inp.([]interface{})
	require.True(t, ok)
	require.Len(t, arr, 1)
	assert.Equal(t, "what is braintrust?", arr[0])

	out := span.Output()
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(16), outMap["embedding_length"])
	_, hasCount := outMap["embeddings_count"]
	assert.False(t, hasCount, "EmbedQuery output should omit embeddings_count")

	metadata := span.Metadata()
	assert.Equal(t, "langchain", metadata["provider"])
}

func TestWrapEmbedderEmbedDocumentsError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	wantErr := errors.New("rate limit exceeded")
	wrapped := WrapEmbedder(&fakeEmbedder{callErr: wantErr}, WithEmbedderTracerProvider(tp))

	_, err := wrapped.EmbedDocuments(context.Background(), []string{"x"})
	require.Error(t, err)
	assert.Equal(t, wantErr, err)

	span := exporter.FlushOne()
	span.AssertNameIs("langchain.embedder.embed_documents")
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
}

func TestWrapEmbedderEmbedQueryError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	wantErr := errors.New("server error")
	wrapped := WrapEmbedder(&fakeEmbedder{callErr: wantErr}, WithEmbedderTracerProvider(tp))

	_, err := wrapped.EmbedQuery(context.Background(), "q")
	require.Error(t, err)

	span := exporter.FlushOne()
	span.AssertNameIs("langchain.embedder.embed_query")
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
}
