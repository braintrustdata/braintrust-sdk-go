package traceopenai

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestChatCompletions(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set")
	}

	_, exporter := oteltest.Setup(t)

	// Create traced HTTP client
	httpClient := Client()

	// Create OpenAI client with traced HTTP client
	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = httpClient
	client := openai.NewClientWithConfig(config)

	// Make a chat completion request
	timer := oteltest.NewTimer()
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "What is 2+2?",
			},
		},
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotEmpty(t, resp.ID)
	require.NotEmpty(t, resp.Choices)

	// Validate response
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Contains(t, resp.Choices[0].Message.Content, "4")

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	// Validate span basics
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("openai.chat.completions.create")

	// Check metadata contains expected fields
	metadata := ts.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "/v1/chat/completions", metadata["endpoint"])
	assert.Equal(t, "gpt-4o-mini", metadata["model"])

	// Check input/output
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(t, err)
	assert.Contains(t, string(inputJSON), "What is 2+2?")

	output := ts.Output()
	outputJSON, err := json.Marshal(output)
	require.NoError(t, err)
	assert.Contains(t, string(outputJSON), "4")

	// Check metrics exist and have positive values
	metrics := ts.Metrics()
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
	assert.Greater(t, metrics["tokens"], float64(0))
}

func TestWrapClient(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set")
	}

	_, exporter := oteltest.Setup(t)

	// Create existing HTTP client
	existingClient := &http.Client{}

	// Wrap it with tracing
	httpClient := WrapClient(existingClient)

	// Create OpenAI client with wrapped HTTP client
	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = httpClient
	client := openai.NewClientWithConfig(config)

	// Make a chat completion request
	timer := oteltest.NewTimer()
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Say hello",
			},
		},
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotEmpty(t, resp.ID)

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	// Validate span
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("openai.chat.completions.create")

	// Check metadata
	metadata := ts.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "/v1/chat/completions", metadata["endpoint"])
}

func TestStreamingChatCompletions(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set")
	}

	_, exporter := oteltest.Setup(t)

	// Create traced HTTP client
	httpClient := Client()

	// Create OpenAI client with traced HTTP client
	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = httpClient
	client := openai.NewClientWithConfig(config)

	// Make a streaming chat completion request
	timer := oteltest.NewTimer()
	stream, err := client.CreateChatCompletionStream(context.Background(), openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Count from 1 to 3",
			},
		},
		StreamOptions: &openai.StreamOptions{
			IncludeUsage: true,
		},
	})
	require.NoError(t, err)
	defer func() {
		_ = stream.Close()
	}()

	var fullContent string
	var hasValidChunks bool
	for {
		response, err := stream.Recv()
		if err != nil {
			// io.EOF is expected when stream ends
			if err.Error() != "EOF" {
				require.NoError(t, err)
			}
			break
		}
		hasValidChunks = true

		// Validate chunk structure
		assert.NotEmpty(t, response.ID)
		assert.Equal(t, "chat.completion.chunk", response.Object)

		// Accumulate content
		if len(response.Choices) > 0 && response.Choices[0].Delta.Content != "" {
			fullContent += response.Choices[0].Delta.Content
		}
	}

	require.True(t, hasValidChunks, "should have received valid chunks")
	require.NotEmpty(t, fullContent, "should have accumulated content")

	// Close stream to complete the request
	_ = stream.Close()
	timeRange := timer.Tick()

	// Wait for spans to be exported
	spans := exporter.Flush()
	require.Len(t, spans, 1)
	ts := spans[0]

	// Validate span
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("openai.chat.completions.create")

	// Check metadata indicates streaming
	metadata := ts.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "/v1/chat/completions", metadata["endpoint"])
	assert.Equal(t, true, metadata["stream"])

	// Check input
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(t, err)
	assert.Contains(t, string(inputJSON), "Count from 1 to 3")

	// Check output - should have accumulated streaming content
	output := ts.Output()
	assert.NotNil(t, output)

	// Check metrics (should be available with IncludeUsage: true)
	require.True(t, ts.HasAttr("braintrust.metrics"), "metrics should be present with IncludeUsage: true")
	metrics := ts.Metrics()
	assert.Greater(t, metrics["tokens"], float64(0))
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
}

func TestErrorHandling(t *testing.T) {
	_, exporter := oteltest.Setup(t)

	// Create traced HTTP client
	httpClient := Client()

	// Create OpenAI client with invalid API key to trigger an error
	config := openai.DefaultConfig("invalid-api-key")
	config.HTTPClient = httpClient
	client := openai.NewClientWithConfig(config)

	// Make a chat completion request that will fail
	timer := oteltest.NewTimer()
	_, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Hello",
			},
		},
	})
	timeRange := timer.Tick()

	// Should get an error
	require.Error(t, err)

	// Wait for spans to be exported
	spans := exporter.Flush()
	require.Len(t, spans, 1)

	ts := spans[0]

	// Validate span
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("openai.chat.completions.create")

	// Check metadata
	metadata := ts.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "/v1/chat/completions", metadata["endpoint"])
}
