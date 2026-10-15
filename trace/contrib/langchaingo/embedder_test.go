package langchaingo

import (
	"context"
	"encoding/json"
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

func TestWrapEmbedderSuccess(t *testing.T) {
	for _, tt := range []struct {
		method   string
		dim      int
		input    any
		output   map[string]any
		metadata map[string]any
	}{
		{
			method: "embed_documents", dim: 8,
			input:    []string{"hello world", "braintrust tracing", "go sdk"},
			output:   map[string]any{"embedding_length": float64(8), "embeddings_count": float64(3)},
			metadata: map[string]any{"provider": "openai", "model": "text-embedding-3-small"},
		},
		{
			method: "embed_query", dim: 16,
			input:    "what is braintrust?",
			output:   map[string]any{"embedding_length": float64(16)},
			metadata: map[string]any{"provider": "langchain"},
		},
	} {
		t.Run(tt.method, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			opts := []EmbedderOption{WithEmbedderTracerProvider(tp)}
			if tt.method == "embed_documents" {
				opts = append(opts, WithEmbedderModel("text-embedding-3-small"), WithEmbedderProvider("openai"))
			}
			wrapped := WrapEmbedder(&fakeEmbedder{dim: tt.dim}, opts...)
			switch input := tt.input.(type) {
			case []string:
				vectors, err := wrapped.EmbedDocuments(context.Background(), input)
				require.NoError(t, err)
				require.Len(t, vectors, len(input))
				assert.Len(t, vectors[0], tt.dim)
			case string:
				vector, err := wrapped.EmbedQuery(context.Background(), input)
				require.NoError(t, err)
				require.Len(t, vector, tt.dim)
			default:
				t.Fatalf("unsupported test input: %T", input)
			}

			span := exporter.FlushOne()
			span.AssertNameIs("langchain.embedder." + tt.method)
			span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
			inputJSON, err := json.Marshal(tt.input)
			require.NoError(t, err)
			assert.JSONEq(t, string(inputJSON), span.Attr("braintrust.input_json").String())
			assert.Equal(t, tt.output, span.Output())
			assert.Equal(t, tt.metadata, span.Metadata())
		})
	}
}

func TestWrapEmbedderErrors(t *testing.T) {
	for _, method := range []string{"embed_documents", "embed_query"} {
		t.Run(method, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			wantErr := errors.New("embedding failed")
			wrapped := WrapEmbedder(&fakeEmbedder{callErr: wantErr}, WithEmbedderTracerProvider(tp))

			var err error
			if method == "embed_documents" {
				_, err = wrapped.EmbedDocuments(context.Background(), []string{"x"})
			} else {
				_, err = wrapped.EmbedQuery(context.Background(), "q")
			}
			require.ErrorIs(t, err, wantErr)

			span := exporter.FlushOne()
			span.AssertNameIs("langchain.embedder." + method)
			assert.Equal(t, codes.Error, span.Status().Code)
			assert.Equal(t, wantErr.Error(), span.Status().Description)
			events := span.Events()
			require.Len(t, events, 1)
			assert.Equal(t, "exception", events[0].Name)
		})
	}
}
