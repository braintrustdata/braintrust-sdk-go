package together

import (
	"context"
	"os"
	"testing"

	together "github.com/togethercomputer/together-go"
	"github.com/togethercomputer/together-go/option"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const testModel = "meta-llama/Llama-3.3-70B-Instruct-Turbo"
const testEmbeddingModel = "togethercomputer/m2-bert-80M-8k-retrieval"

// setUpTest sets up a new tracer provider and VCR for each test. It returns a
// Together client configured with tracing and VCR, plus the span exporter.
func setUpTest(t *testing.T) (together.Client, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("TOGETHER_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("TOGETHER_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-together-key-for-replay"
	}

	httpClient := vcr.NewHTTPClient(t)

	client := together.NewClient(
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMiddleware(NewMiddleware(WithTracerProvider(tp))), //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	)

	return client, exporter
}

func userMessage(text string) []together.ChatCompletionNewParamsMessageUnion {
	return []together.ChatCompletionNewParamsMessageUnion{{
		OfChatCompletionNewsMessageChatCompletionUserMessageParam: &together.ChatCompletionNewParamsMessageChatCompletionUserMessageParam{
			Role: "user",
			Content: together.ChatCompletionNewParamsMessageChatCompletionUserMessageParamContentUnion{
				OfString: together.String(text),
			},
		},
	}}
}

func TestChatCompletion(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Chat.Completions.New(context.Background(), together.ChatCompletionNewParams{
		Model:    testModel,
		Messages: userMessage("Say hello"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Choices)

	ts := exporter.FlushOne()
	ts.AssertNameIs("Chat Completion")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	metadata := ts.Metadata()
	assert.Equal(t, "together", metadata["provider"])
	assert.Equal(t, testModel, metadata["model"])

	metrics := ts.Metrics()
	assert.Greater(t, metrics["tokens"], float64(0))
}

func TestChatCompletionStreaming(t *testing.T) {
	client, exporter := setUpTest(t)

	stream := client.Chat.Completions.NewStreaming(context.Background(), together.ChatCompletionNewParams{
		Model:    testModel,
		Messages: userMessage("Count 1 to 3"),
	})
	for stream.Next() {
	}
	require.NoError(t, stream.Err())

	ts := exporter.FlushOne()
	ts.AssertNameIs("Chat Completion")

	output, ok := ts.Output().([]any)
	require.True(t, ok)
	require.NotEmpty(t, output)
}

func TestCompletion(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Completions.New(context.Background(), together.CompletionNewParams{
		Model:  together.CompletionNewParamsModel(testModel),
		Prompt: "The capital of France is",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Choices)

	ts := exporter.FlushOne()
	ts.AssertNameIs("together.completions.create")
	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	metadata := ts.Metadata()
	assert.Equal(t, "together", metadata["provider"])
	assert.Equal(t, testModel, metadata["model"])
}

func TestEmbeddings(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Embeddings.New(context.Background(), together.EmbeddingNewParams{
		Model: together.EmbeddingNewParamsModel(testEmbeddingModel),
		Input: together.EmbeddingNewParamsInputUnion{OfString: together.String("hello world")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Data)

	ts := exporter.FlushOne()
	ts.AssertNameIs("together.embeddings.create")

	output, ok := ts.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), output["count"])

	metadata := ts.Metadata()
	assert.Equal(t, "together", metadata["provider"])
	assert.Equal(t, testEmbeddingModel, metadata["model"])
}
