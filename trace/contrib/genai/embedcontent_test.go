package genai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

const testEmbeddingModel = "gemini-embedding-001"

func TestEmbedContentSingle(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		genai.Text("The quick brown fox jumps over the lazy dog"),
		nil,
	)
	timeRange := timer.Tick()
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Embeddings)

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("embed_content")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(t, inputAttr)
	assert.Contains(t, inputAttr.String(), "quick brown fox")

	output := ts.Output()
	outputMap, ok := output.(map[string]interface{})
	require.True(t, ok)
	embLen, ok := outputMap["embedding_length"].(float64)
	require.True(t, ok, "output should include embedding_length")
	assert.Greater(t, embLen, float64(0))
	count, ok := outputMap["embeddings_count"].(float64)
	require.True(t, ok, "output should include embeddings_count")
	assert.Equal(t, float64(1), count)

	metadata := ts.Metadata()
	assert.Equal(t, "gemini", metadata["provider"])
	assert.Equal(t, testEmbeddingModel, metadata["model"])
}

func TestEmbedContentBatch(t *testing.T) {
	client, exporter := setUpTest(t)

	contents := []*genai.Content{
		{Parts: []*genai.Part{{Text: "hello world"}}},
		{Parts: []*genai.Part{{Text: "goodbye world"}}},
		{Parts: []*genai.Part{{Text: "braintrust tracing"}}},
	}

	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		contents,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Embeddings, 3)

	ts := exporter.FlushOne()
	ts.AssertNameIs("embed_content")

	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(t, inputAttr)
	for _, want := range []string{"hello world", "goodbye world", "braintrust tracing"} {
		assert.Contains(t, inputAttr.String(), want)
	}

	output := ts.Output()
	outputMap, ok := output.(map[string]interface{})
	require.True(t, ok)
	embLen, ok := outputMap["embedding_length"].(float64)
	require.True(t, ok)
	assert.Greater(t, embLen, float64(0))
	count, ok := outputMap["embeddings_count"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(3), count)
}

func TestEmbedContentWithTaskType(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		genai.Text("What is the capital of France?"),
		&genai.EmbedContentConfig{
			TaskType: "RETRIEVAL_QUERY",
		},
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	metadata := ts.Metadata()
	assert.Equal(t, "RETRIEVAL_QUERY", metadata["taskType"])
}
