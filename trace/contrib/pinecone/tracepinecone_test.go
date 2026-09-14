package pinecone

import (
	"context"
	"os"
	"testing"

	"github.com/pinecone-io/go-pinecone/v6/pinecone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const testEmbedModel = "multilingual-e5-large"
const testRerankModel = "bge-reranker-v2-m3"

// setUpTest sets up a new tracer provider and VCR for each test. It returns
// a Pinecone client configured with tracing and VCR, plus the span exporter.
func setUpTest(t *testing.T) (*pinecone.Client, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("PINECONE_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("PINECONE_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-pinecone-key-for-replay"
	}

	httpClient := vcr.NewHTTPClient(t)
	tracedClient := WrapClient(httpClient, WithTracerProvider(tp))

	client, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey:     apiKey,
		RestClient: tracedClient,
	})
	require.NoError(t, err)

	return client, exporter
}

func TestEmbed(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	resp, err := client.Inference.Embed(context.Background(), &pinecone.EmbedRequest{
		Model:      testEmbedModel,
		TextInputs: []string{"Who created the first computer?"},
		Parameters: pinecone.EmbedParameters{
			"input_type": "passage",
			"truncate":   "END",
		},
	})
	timeRange := timer.Tick()
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Data)

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("pinecone.embed")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	assert.Equal(t, map[string]any{
		"inputs": []any{"Who created the first computer?"},
		"parameters": map[string]any{
			"input_type": "passage",
			"truncate":   "END",
		},
	}, ts.Input())

	output, ok := ts.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), output["count"])
	assert.Equal(t, "dense", output["vector_type"])

	metadata := ts.Metadata()
	assert.Equal(t, "pinecone", metadata["provider"])
	assert.Equal(t, testEmbedModel, metadata["model"])

	metrics := ts.Metrics()
	assert.Greater(t, metrics["tokens"], float64(0))
}

func TestEmbedBatch(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Inference.Embed(context.Background(), &pinecone.EmbedRequest{
		Model:      testEmbedModel,
		TextInputs: []string{"hello world", "goodbye world", "braintrust tracing"},
		Parameters: pinecone.EmbedParameters{"input_type": "passage"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 3)

	ts := exporter.FlushOne()
	ts.AssertNameIs("pinecone.embed")

	assert.Equal(t, []any{"hello world", "goodbye world", "braintrust tracing"}, ts.Input().(map[string]any)["inputs"])
	output, ok := ts.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(3), output["count"])
}

func TestRerank(t *testing.T) {
	client, exporter := setUpTest(t)

	returnDocuments := true
	topN := 2
	timer := oteltest.NewTimer()
	resp, err := client.Inference.Rerank(context.Background(), &pinecone.RerankRequest{
		Model:           testRerankModel,
		Query:           "i love to eat apples",
		ReturnDocuments: &returnDocuments,
		TopN:            &topN,
		RankFields:      &[]string{"text"},
		Documents: []pinecone.Document{
			{"id": "doc1", "text": "Apple is a popular fruit known for its sweetness."},
			{"id": "doc2", "text": "Many people enjoy eating apples as a healthy snack."},
			{"id": "doc3", "text": "Apple Inc. has revolutionized the tech industry."},
		},
	})
	timeRange := timer.Tick()
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Data)

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("pinecone.rerank")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	input, ok := ts.Input().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, testRerankModel, input["model"])
	assert.Equal(t, "i love to eat apples", input["query"])
	assert.Equal(t, float64(2), input["top_n"])

	output, ok := ts.Output().([]any)
	require.True(t, ok)
	assert.LessOrEqual(t, len(output), 2)

	metadata := ts.Metadata()
	assert.Equal(t, "pinecone", metadata["provider"])
	assert.Equal(t, testRerankModel, metadata["model"])

	metrics := ts.Metrics()
	assert.Greater(t, metrics["rerank_units"], float64(0))
}
