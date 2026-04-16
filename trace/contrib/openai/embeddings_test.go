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
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	timer := oteltest.NewTimer()
	resp, err := client.Embeddings.New(context.Background(), openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("The quick brown fox jumps over the lazy dog"),
		},
		Model: testEmbeddingModel,
	})
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)
	require.NotEmpty(resp.Data)
	assert.NotEmpty(resp.Data[0].Embedding, "response should include embedding vector")

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("openai.embeddings.create")
	assert.Equal(codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(inputAttr, "should set braintrust.input_json")
	assert.Contains(inputAttr.String(), "quick brown fox")

	output := ts.Output()
	outputMap, ok := output.(map[string]interface{})
	require.True(ok, "output should be a map")
	embLen, ok := outputMap["embedding_length"].(float64)
	require.True(ok, "output should include embedding_length")
	assert.Greater(embLen, float64(0))

	metadata := ts.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/embeddings", metadata["endpoint"])
	assert.Contains(metadata["model"], testEmbeddingModel)

	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0), "should have prompt_tokens")
	assert.Greater(metrics["tokens"], float64(0), "should have tokens (total_tokens)")
	_, hasCompletion := metrics["completion_tokens"]
	assert.False(hasCompletion, "embeddings do not have completion_tokens")
}

func TestOpenAIEmbeddingsBatch(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	inputs := []string{"hello world", "goodbye world", "braintrust tracing"}
	resp, err := client.Embeddings.New(context.Background(), openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: inputs,
		},
		Model: testEmbeddingModel,
	})
	require.NoError(err)
	require.NotNil(resp)
	require.Len(resp.Data, len(inputs))

	ts := exporter.FlushOne()

	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(inputAttr)
	for _, s := range inputs {
		assert.Contains(inputAttr.String(), s)
	}

	output := ts.Output()
	outputMap, ok := output.(map[string]interface{})
	require.True(ok)
	embLen, ok := outputMap["embedding_length"].(float64)
	require.True(ok)
	assert.Greater(embLen, float64(0))
}

func TestOpenAIEmbeddingsWithDimensions(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	dims := int64(256)
	resp, err := client.Embeddings.New(context.Background(), openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("dimensionality test"),
		},
		Model:      testEmbeddingModel,
		Dimensions: openai.Int(dims),
	})
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()
	metadata := ts.Metadata()
	dimsMeta, ok := metadata["dimensions"].(float64)
	require.True(ok, "dimensions should be in metadata")
	assert.Equal(float64(dims), dimsMeta)

	output := ts.Output()
	outputMap, ok := output.(map[string]interface{})
	require.True(ok)
	assert.Equal(float64(dims), outputMap["embedding_length"])
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddingsOutputSummary(tc.raw)
			assert.Equal(t, tc.want, got)
		})
	}
}
