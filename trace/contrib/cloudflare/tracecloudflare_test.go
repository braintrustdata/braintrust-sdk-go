package cloudflare

import (
	"context"
	"os"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/ai"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const testChatModel = "@cf/meta/llama-3.1-8b-instruct-fast"
const testEmbeddingModel = "@cf/baai/bge-base-en-v1.5"
const testClassificationModel = "@cf/huggingface/distilbert-sst-2-int8"

// envOrDefault returns the named env var if set, or fallback otherwise.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// setUpTest sets up a new tracer provider and VCR for each test. It returns
// a Cloudflare client configured with tracing and VCR, the account ID to
// use, and the span exporter.
func setUpTest(t *testing.T) (*cf.Client, string, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	if mode != vcr.ModeReplay && (os.Getenv("CLOUDFLARE_API_TOKEN") == "" || os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "") {
		t.Fatal("CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID not set (required in record/off mode)")
	}
	apiToken := envOrDefault("CLOUDFLARE_API_TOKEN", "dummy-cloudflare-token-for-replay")
	// Unlike other providers, Workers AI puts the account ID in the URL
	// path itself, so VCR's URL-based cassette matching needs the exact
	// same value at record and replay time. After recording with a real
	// CLOUDFLARE_ACCOUNT_ID, replace it in the cassette file with this
	// placeholder before committing.
	accountID := envOrDefault("CLOUDFLARE_ACCOUNT_ID", "dummy-account-id-for-replay")

	httpClient := vcr.NewHTTPClient(t)

	client := cf.NewClient(
		option.WithAPIToken(apiToken),
		option.WithHTTPClient(httpClient),
		option.WithMiddleware(NewMiddleware(WithTracerProvider(tp))), //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	)

	return client, accountID, exporter
}

func TestTextGeneration(t *testing.T) {
	client, accountID, exporter := setUpTest(t)

	resp, err := client.AI.Run(context.Background(), testChatModel, ai.AIRunParams{
		AccountID: cf.F(accountID),
		Body: ai.AIRunParamsBodyTextGeneration{
			Messages: cf.F([]ai.AIRunParamsBodyTextGenerationMessage{
				{
					Role:    cf.F("user"),
					Content: cf.F[ai.AIRunParamsBodyTextGenerationMessagesContentUnion](shared.UnionString("Say hello")),
				},
			}),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	ts.AssertNameIs("cloudflare.ai.text_generation")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	metadata := ts.Metadata()
	assert.Equal(t, "cloudflare", metadata["provider"])
	// The response resolves the request's model alias to a concrete model
	// string, which is what metadata.model should reflect per the
	// instrumentation spec, not the alias the caller passed.
	assert.NotEqual(t, testChatModel, metadata["model"])
	assert.NotEmpty(t, metadata["model"])
	assert.Equal(t, "text_generation", metadata["task"])

	// Output MUST be an array of OpenAI Chat Completions choice objects
	// per the instrumentation spec, since Cloudflare has no dedicated UI
	// normalizer.
	output, ok := ts.Output().([]any)
	require.True(t, ok, "expected an array of choice objects")
	require.NotEmpty(t, output)
	choice, ok := output[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "stop", choice["finish_reason"])
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", message["role"])
	assert.NotEmpty(t, message["content"])

	metrics := ts.Metrics()
	assert.Greater(t, metrics["tokens"], float64(0))
}

func TestTextGenerationWithTools(t *testing.T) {
	client, accountID, exporter := setUpTest(t)

	resp, err := client.AI.Run(context.Background(), testChatModel, ai.AIRunParams{
		AccountID: cf.F(accountID),
		Body: ai.AIRunParamsBodyTextGeneration{
			Messages: cf.F([]ai.AIRunParamsBodyTextGenerationMessage{
				{
					Role:    cf.F("user"),
					Content: cf.F[ai.AIRunParamsBodyTextGenerationMessagesContentUnion](shared.UnionString("What's the weather in Paris?")),
				},
			}),
			Tools: cf.F([]ai.AIRunParamsBodyTextGenerationToolUnion{
				ai.AIRunParamsBodyTextGenerationTool{
					Type:        cf.F("function"),
					Name:        cf.F("get_weather"),
					Description: cf.F("Get the current weather for a location"),
					Parameters: cf.F[any](map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
						"required": []string{"location"},
					}),
				},
			}),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	ts.AssertNameIs("cloudflare.ai.text_generation")

	// metadata.tools MUST be the OpenAI-nested shape per the
	// instrumentation spec, converted from Workers AI's flat wire shape.
	metadata := ts.Metadata()
	tools, ok := metadata["tools"].([]any)
	require.True(t, ok, "expected tools in metadata")
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function", tool["type"])
	fn, ok := tool["function"].(map[string]any)
	require.True(t, ok, "expected tools[0] to be nested under a function key")
	assert.Equal(t, "get_weather", fn["name"])

	output, ok := ts.Output().([]any)
	require.True(t, ok, "expected an array of choice objects")
	require.NotEmpty(t, output)
	choice, ok := output[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_calls", choice["finish_reason"])
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	toolCalls, ok := message["tool_calls"].([]any)
	require.True(t, ok, "expected tool_calls in message")
	require.NotEmpty(t, toolCalls)

	call, ok := toolCalls[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function", call["type"])
	require.NotEmpty(t, call["id"], "tool call MUST have an id per the instrumentation spec")
	callFn, ok := call["function"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "get_weather", callFn["name"])
	// arguments MUST be a JSON-encoded string, not an object.
	_, isString := callFn["arguments"].(string)
	assert.True(t, isString, "tool call arguments must be a JSON string")
}

func TestTextEmbeddings(t *testing.T) {
	client, accountID, exporter := setUpTest(t)

	resp, err := client.AI.Run(context.Background(), testEmbeddingModel, ai.AIRunParams{
		AccountID: cf.F(accountID),
		Body: ai.AIRunParamsBodyTextEmbeddings{
			Text: cf.F[ai.AIRunParamsBodyTextEmbeddingsTextUnion](shared.UnionString("hello world")),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	ts.AssertNameIs("cloudflare.ai.embeddings")

	output, ok := ts.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), output["count"])

	metadata := ts.Metadata()
	assert.Equal(t, "cloudflare", metadata["provider"])
	assert.Equal(t, testEmbeddingModel, metadata["model"])
	assert.Equal(t, "embeddings", metadata["task"])
}

// TestTextClassification exercises the fallback "log Cloudflare's own
// response shape as-is" path, which every task other than text generation
// and embeddings goes through. The response here is a bare JSON array
// (Cloudflare's response envelope unwraps to []any, not a map), which is
// what the fallback path specifically has to handle correctly.
func TestTextClassification(t *testing.T) {
	client, accountID, exporter := setUpTest(t)

	resp, err := client.AI.Run(context.Background(), testClassificationModel, ai.AIRunParams{
		AccountID: cf.F(accountID),
		Body: ai.AIRunParamsBodyTextClassification{
			Text: cf.F("I love this product"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	ts.AssertNameIs("cloudflare.ai.run")

	output, ok := ts.Output().([]any)
	require.True(t, ok, "expected the classification array to be logged as-is")
	require.NotEmpty(t, output)

	metadata := ts.Metadata()
	assert.Equal(t, "cloudflare", metadata["provider"])
	assert.Equal(t, testClassificationModel, metadata["model"])
	assert.NotContains(t, metadata, "task", "task shouldn't be guessed for the fallback path")
}

func TestTruncateLargeFields(t *testing.T) {
	bigArray := make([]any, maxInlineArrayLen+1)
	for i := range bigArray {
		bigArray[i] = i
	}
	bigString := strings.Repeat("a", maxInlineStringLen+1)

	got := truncateLargeFields(map[string]any{
		"image":  bigArray,
		"prompt": bigString,
		"model":  "@cf/meta/llama-3.1-8b-instruct-fast",
		"nested": map[string]any{"audio": bigArray},
	}).(map[string]any)

	assert.Equal(t, map[string]any{"type": "array", "length": maxInlineArrayLen + 1}, got["image"])
	assert.Equal(t, map[string]any{"type": "string", "length": maxInlineStringLen + 1}, got["prompt"])
	assert.Equal(t, "@cf/meta/llama-3.1-8b-instruct-fast", got["model"])
	assert.Equal(t, map[string]any{"type": "array", "length": maxInlineArrayLen + 1}, got["nested"].(map[string]any)["audio"])
}
