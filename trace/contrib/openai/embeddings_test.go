package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

const testEmbeddingModel = "text-embedding-3-small"

func TestOpenAIEmbeddings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      openai.EmbeddingNewParamsInputUnion
		wantInput  any
		count      int
		dimensions int64
	}{
		{
			name:       "with_dimensions",
			input:      openai.EmbeddingNewParamsInputUnion{OfString: openai.String("dimensionality test")},
			wantInput:  "dimensionality test",
			count:      1,
			dimensions: 256,
		},
		{
			name:      "batch",
			input:     openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: []string{"hello world", "goodbye world", "braintrust tracing"}},
			wantInput: []any{"hello world", "goodbye world", "braintrust tracing"},
			count:     3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, exporter := setUpTest(t)
			params := openai.EmbeddingNewParams{Input: tc.input, Model: testEmbeddingModel}
			if tc.dimensions > 0 {
				params.Dimensions = openai.Int(tc.dimensions)
			}
			timer := oteltest.NewTimer()
			resp, err := client.Embeddings.New(context.Background(), params)
			timeRange := timer.Tick()
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, resp.Data, tc.count)
			for _, embedding := range resp.Data {
				assert.NotEmpty(t, embedding.Embedding)
				if tc.dimensions > 0 {
					assert.Len(t, embedding.Embedding, int(tc.dimensions))
				}
			}

			span := exporter.FlushOne()
			span.AssertInTimeRange(timeRange)
			span.AssertNameIs("openai.embeddings.create")
			assert.Equal(t, codes.Unset, span.Status().Code)
			span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
			span.AssertJSONAttrEquals("braintrust.input_json", tc.wantInput)
			assert.Equal(t, map[string]any{"embedding_length": float64(len(resp.Data[0].Embedding))}, span.Output())

			metadata := span.Metadata()
			assert.Equal(t, "openai", metadata["provider"])
			assert.Equal(t, "/v1/embeddings", metadata["endpoint"])
			assert.Contains(t, metadata["model"], testEmbeddingModel)
			if tc.dimensions > 0 {
				assert.Equal(t, float64(tc.dimensions), metadata["dimensions"])
			} else {
				assert.NotContains(t, metadata, "dimensions", "default dimensions should stay absent from metadata")
			}

			metrics := span.Metrics()
			assert.Greater(t, metrics["prompt_tokens"], float64(0))
			assert.Equal(t, float64(resp.Usage.PromptTokens), metrics["prompt_tokens"])
			assert.Equal(t, float64(resp.Usage.TotalTokens), metrics["tokens"])
			assert.NotContains(t, metrics, "completion_tokens", "embeddings do not have completion_tokens")
		})
	}
}

func TestEmbeddingsPathRouting(t *testing.T) {
	cases := []struct {
		path   string
		routed bool
	}{
		{"/v1/embeddings", true},
		{"/api/v1/embeddings", true},
		{"/v1/chat/completions", true},
		{"/v1/responses", true},
		{"/v1/models", false},
		{"/v1/embed", false},
	}
	cfg := &middlewareConfig{}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			tracer := openaiRouter(cfg, c.path)
			if c.routed {
				assert.NotNil(t, tracer, "%s should route", c.path)
			} else {
				assert.Nil(t, tracer, "%s should not route", c.path)
			}
		})
	}
}

func TestEmbeddingsOutputSummary(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]interface{}
		want map[string]any
	}{
		{
			name: "single embedding",
			raw: map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"embedding": []interface{}{0.1, 0.2, 0.3}},
				},
			},
			want: map[string]any{"embedding_length": 3},
		},
		{
			name: "batch embeddings uses first length",
			raw: map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"embedding": []interface{}{0.1, 0.2}},
					map[string]interface{}{"embedding": []interface{}{0.3, 0.4}},
				},
			},
			want: map[string]any{"embedding_length": 2},
		},
		{
			name: "empty data",
			raw:  map[string]interface{}{"data": []interface{}{}},
			want: map[string]any{},
		},
		{
			name: "no data field",
			raw:  map[string]interface{}{},
			want: map[string]any{},
		},
		{
			// When encoding_format=base64, the embedding arrives as a string, not
			// a []float. We can't report a dimension count for it — the Python SDK
			// has the same gap. Document the silent-empty behavior.
			name: "base64 encoding returns empty summary",
			raw: map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"embedding": "gAAAAA=="},
				},
			},
			want: map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddingsOutputSummary(tc.raw)
			assert.Equal(t, tc.want, got)
		})
	}
}
