package genkit

import (
	"context"
	"errors"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestWrapEmbedderBatch(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	ctx := context.Background()
	g := genkit.Init(ctx, genkit.WithPlugins(newOpenAIPlugin(t)))
	inner := genkit.LookupEmbedder(g, "openai/text-embedding-3-small")
	require.NotNil(t, inner)

	wrapped := WrapEmbedder(inner, WithEmbedderTracerProvider(tp))
	resp, err := wrapped.Embed(ctx, &ai.EmbedRequest{Input: []*ai.Document{
		ai.DocumentFromText("hello world", nil),
		ai.DocumentFromText("braintrust tracing", nil),
		ai.DocumentFromText("go sdk", nil),
	}})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 3)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.embed")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
	assert.Equal(t, map[string]any{"inputs": []any{
		map[string]any{"content": "hello world"},
		map[string]any{"content": "braintrust tracing"},
		map[string]any{"content": "go sdk"},
	}}, span.Input())
	assert.Equal(t, map[string]any{"count": float64(3)}, span.Output())
	assert.Equal(t, map[string]any{
		"provider": "openai",
		"model":    "text-embedding-3-small",
	}, span.Metadata())
}

func TestWrapEmbedderSingle(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	ctx := context.Background()
	g := genkit.Init(ctx, genkit.WithPlugins(newOpenAIPlugin(t)))
	inner := genkit.LookupEmbedder(g, "openai/text-embedding-3-small")
	require.NotNil(t, inner)

	wrapped := WrapEmbedder(inner, WithEmbedderTracerProvider(tp))
	resp, err := wrapped.Embed(ctx, &ai.EmbedRequest{
		Input: []*ai.Document{ai.DocumentFromText("what is braintrust?", nil)},
	})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 1)

	span := exporter.FlushOne()
	assert.Equal(t, map[string]any{
		"inputs": []any{map[string]any{"content": "what is braintrust?"}},
	}, span.Input())
	assert.Equal(t, map[string]any{"count": float64(1)}, span.Output())
}

func TestWrapEmbedderError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	ctx := context.Background()
	plugin := newOpenAIPlugin(t)
	_ = genkit.Init(ctx, genkit.WithPlugins(plugin))
	inner := plugin.DefineEmbedder("nonexistent-embedding-model", &ai.EmbedderOptions{})

	wrapped := WrapEmbedder(inner, WithEmbedderTracerProvider(tp))
	_, err := wrapped.Embed(ctx, &ai.EmbedRequest{
		Input: []*ai.Document{ai.DocumentFromText("x", nil)},
	})
	require.Error(t, err)

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
	assert.Equal(t, map[string]any{"count": float64(0)}, span.Output())
}

func TestWrapEmbedderPartialError(t *testing.T) {
	// OpenAI does not return partial embedding batches together with an SDK
	// error, so this provider-contract edge case cannot be recorded with VCR.
	tp, exporter := oteltest.Setup(t)
	wantErr := errors.New("partial batch")
	wrapped := WrapEmbedder(&partialEmbedder{err: wantErr}, WithEmbedderTracerProvider(tp))

	resp, err := wrapped.Embed(context.Background(), &ai.EmbedRequest{
		Input: []*ai.Document{ai.DocumentFromText("first", nil), ai.DocumentFromText("second", nil)},
	})
	require.ErrorIs(t, err, wantErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Embeddings, 1)

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, map[string]any{"count": float64(1)}, span.Output())
}

func TestEmbedderIdentityGoogleAI(t *testing.T) {
	provider, model := embedderIdentity("googleai/gemini-embedding-001")
	assert.Equal(t, "google", provider)
	assert.Equal(t, "gemini-embedding-001", model)
}

func TestEmbedInputMultimodalAndDimensions(t *testing.T) {
	req := &ai.EmbedRequest{
		Input: []*ai.Document{{Content: []*ai.Part{
			ai.NewTextPart("describe this"),
			ai.NewMediaPart("image/png", "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"),
		}}},
		Options: &compatopenai.TextEmbeddingConfig{Dimensions: 256},
	}

	assert.Equal(t, map[string]any{
		"inputs": []map[string]any{{
			"content": []map[string]any{
				{"type": "text", "text": "describe this"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}},
			},
		}},
		"output_dimensions": 256,
	}, embedInput(req))
}

type partialEmbedder struct {
	err error
}

func (e *partialEmbedder) Name() string          { return "openai/text-embedding-3-small" }
func (e *partialEmbedder) Register(api.Registry) {}
func (e *partialEmbedder) Embed(context.Context, *ai.EmbedRequest) (*ai.EmbedResponse, error) {
	return &ai.EmbedResponse{Embeddings: []*ai.Embedding{{Embedding: []float32{1}}}}, e.err
}
