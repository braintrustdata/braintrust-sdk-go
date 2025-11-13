package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

func TestMiddleware(t *testing.T) {
	// Set up test tracer provider
	_, _ = oteltest.Setup(t)

	// Create a test request with a Messages API call
	requestBody := `{
		"model": "claude-3-haiku-20240307",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello, Claude!"
			}
		]
	}`

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")

	// Create a mock response
	responseBody := `{
		"id": "msg_01Aq9w938a90dw8q",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "text",
				"text": "Hello! How can I help you today?"
			}
		],
		"model": "claude-3-haiku-20240307",
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"usage": {
			"input_tokens": 12,
			"output_tokens": 9
		}
	}`

	// Create a mock next middleware
	next := func(req *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	}

	// Call the middleware - uses global TracerProvider by default
	middleware := NewMiddleware() //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	resp, err := middleware(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Read the response body to trigger the tracer
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Hello! How can I help you today?")

	// Close the response body to trigger completion
	err = resp.Body.Close()
	require.NoError(t, err)
}

func TestMessagesTracer(t *testing.T) {
	tp, _ := oteltest.Setup(t)
	cfg := &middlewareConfig{tracerProvider: tp}
	tracer := newMessagesTracer(cfg)
	assert.NotNil(t, tracer)
	assert.False(t, tracer.streaming)
	assert.Equal(t, "anthropic", tracer.metadata["provider"])
	assert.Equal(t, "/v1/messages", tracer.metadata["endpoint"])

	// Test StartSpan
	requestBody := `{
		"model": "claude-3-haiku-20240307",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello, Claude!"
			}
		],
		"stream": false
	}`

	ctx := context.Background()
	start := time.Now()
	reader := strings.NewReader(requestBody)

	newCtx, span, err := tracer.StartSpan(ctx, start, reader)
	require.NoError(t, err)
	require.NotNil(t, span)
	require.NotNil(t, newCtx)

	// Verify metadata was parsed
	assert.Equal(t, "claude-3-haiku-20240307", tracer.metadata["model"])
	assert.Equal(t, float64(1024), tracer.metadata["max_tokens"])
	assert.Equal(t, false, tracer.metadata["stream"])
	assert.False(t, tracer.streaming)
}

func TestMessagesTracerStreaming(t *testing.T) {
	tp, _ := oteltest.Setup(t)
	cfg := &middlewareConfig{tracerProvider: tp}
	tracer := newMessagesTracer(cfg)

	// Test streaming request
	requestBody := `{
		"model": "claude-3-haiku-20240307",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello, Claude!"
			}
		],
		"stream": true
	}`

	ctx := context.Background()
	start := time.Now()
	reader := strings.NewReader(requestBody)

	_, span, err := tracer.StartSpan(ctx, start, reader)
	require.NoError(t, err)
	require.NotNil(t, span)

	// Verify streaming was detected
	assert.True(t, tracer.streaming)
	assert.Equal(t, true, tracer.metadata["stream"])
}

func TestParseUsageTokens(t *testing.T) {
	usage := map[string]interface{}{
		"input_tokens":  float64(12),
		"output_tokens": float64(9),
	}

	metrics := parseUsageTokens(usage)

	assert.Equal(t, int64(12), metrics["prompt_tokens"])
	assert.Equal(t, int64(9), metrics["completion_tokens"])
}

func TestParseUsageTokensWithCache(t *testing.T) {
	t.Run("cache_creation_input_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"input_tokens":                float64(10),
			"output_tokens":               float64(5),
			"cache_creation_input_tokens": float64(100),
		}

		metrics := parseUsageTokens(usage)

		// Should include cache creation tokens in the total
		assert.Equal(t, int64(110), metrics["prompt_tokens"]) // 10 + 100
		assert.Equal(t, int64(5), metrics["completion_tokens"])
		assert.Equal(t, int64(100), metrics["prompt_cache_creation_tokens"])
	})

	t.Run("cache_read_input_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"input_tokens":            float64(8),
			"output_tokens":           float64(12),
			"cache_read_input_tokens": float64(50),
		}

		metrics := parseUsageTokens(usage)

		// Should include cache read tokens in the total
		assert.Equal(t, int64(58), metrics["prompt_tokens"]) // 8 + 50
		assert.Equal(t, int64(12), metrics["completion_tokens"])
		assert.Equal(t, int64(50), metrics["prompt_cached_tokens"])
	})

	t.Run("both_cache_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"input_tokens":                float64(15),
			"output_tokens":               float64(20),
			"cache_creation_input_tokens": float64(200),
			"cache_read_input_tokens":     float64(75),
		}

		metrics := parseUsageTokens(usage)

		// Should include both cache tokens in the total
		assert.Equal(t, int64(290), metrics["prompt_tokens"]) // 15 + 200 + 75
		assert.Equal(t, int64(20), metrics["completion_tokens"])
		assert.Equal(t, int64(200), metrics["prompt_cache_creation_tokens"])
		assert.Equal(t, int64(75), metrics["prompt_cached_tokens"])
	})

	t.Run("cache_tokens_without_input_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"output_tokens":               float64(10),
			"cache_creation_input_tokens": float64(150),
			"cache_read_input_tokens":     float64(25),
		}

		metrics := parseUsageTokens(usage)

		// Should still account for cache tokens even without explicit input_tokens
		assert.Equal(t, int64(175), metrics["prompt_tokens"]) // 150 + 25
		assert.Equal(t, int64(10), metrics["completion_tokens"])
		assert.Equal(t, int64(150), metrics["prompt_cache_creation_tokens"])
		assert.Equal(t, int64(25), metrics["prompt_cached_tokens"])
	})
}

// TestMiddlewareIntegration tests the middleware with real Anthropic API calls
func TestMiddlewareIntegration(t *testing.T) {
	// Set up test tracer and client with VCR
	client, exporter := setUpTest(t)

	// Make a simple API call
	timer := oteltest.NewTimer()
	ctx := context.Background()
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model("claude-3-haiku-20240307"), // Use cheapest model
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is the capital of France?")),
		},
		MaxTokens: 1024, // Using higher token count - very low MaxTokens (like 10) cause timeouts
	})
	timeRange := timer.Tick()

	// Verify the API call succeeded
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Basic response validation
	assert.Equal(t, anthropic.Model("claude-3-haiku-20240307"), resp.Model)
	assert.Equal(t, "assistant", string(resp.Role))
	assert.NotEmpty(t, resp.Content)

	// Verify usage metrics are present
	assert.Greater(t, resp.Usage.InputTokens, int64(0))
	assert.Greater(t, resp.Usage.OutputTokens, int64(0))

	// Verify we got some content back
	require.Len(t, resp.Content, 1)
	assert.NotEmpty(t, resp.Content[0].Text)

	// Validate spans were generated correctly
	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange)

	// Verify span content
	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, "What is the capital of France?")

	output := span.Output()
	assert.NotNil(t, output)

	metadata := span.Metadata()
	assert.Equal(t, "anthropic", metadata["provider"])
	assert.Equal(t, "/v1/messages", metadata["endpoint"])
	assert.Equal(t, "claude-3-haiku-20240307", metadata["model"])
	assert.Equal(t, float64(1024), metadata["max_tokens"])

}

// TestMiddlewareIntegrationStreaming tests the middleware with real Anthropic streaming API calls
func TestMiddlewareIntegrationStreaming(t *testing.T) {
	// Set up test tracer and client
	client, exporter := setUpTest(t)

	// Make a streaming API call
	timer := oteltest.NewTimer()
	ctx := context.Background()
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model("claude-3-haiku-20240307"), // Use cheapest model
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Tell me a very short joke.")),
		},
		MaxTokens:   512,
		Temperature: anthropic.Float(0.8),
		TopP:        anthropic.Float(0.95),
	})

	var completeText string

	// Iterate through the streaming response
	for stream.Next() {
		event := stream.Current()
		switch eventVariant := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch deltaVariant := eventVariant.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				completeText += deltaVariant.Text
			}
		}
	}
	require.NoError(t, stream.Err())
	timeRange := timer.Tick()

	// Basic response validation
	assert.NotEmpty(t, completeText)

	// Validate spans were generated correctly
	span := exporter.FlushOne()

	assertSpanValid(t, span, timeRange)

	// Verify span content
	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, "Tell me a very short joke.")

	output := span.Output()
	assert.NotNil(t, output)

	// The output should contain the complete streamed text in JSON format
	outputStr := span.Attr("braintrust.output_json").String()
	// For streaming, the output is stored as JSON: [{"text":"...", "type":"text"}]
	// So we check that both the accumulated text and the JSON contain expected content
	assert.Contains(t, outputStr, "joke")    // Should contain the word "joke"
	assert.Contains(t, completeText, "joke") // Ensure we got the text from streaming
	// Also verify that some of the streamed content matches what's in the span
	assert.Contains(t, outputStr, completeText[:10]) // Check first 10 chars are in the output

	metadata := span.Metadata()
	assert.Equal(t, "anthropic", metadata["provider"])
	assert.Equal(t, "/v1/messages", metadata["endpoint"])
	assert.Equal(t, "claude-3-haiku-20240307", metadata["model"])
	assert.Equal(t, float64(512), metadata["max_tokens"])
	assert.Equal(t, 0.8, metadata["temperature"])
	assert.Equal(t, 0.95, metadata["top_p"])
	assert.Equal(t, true, metadata["stream"]) // Should detect streaming mode

}

// setUpTest is a helper function that sets up a new tracer provider and VCR for each test.
// It returns an anthropic client configured with VCR and an exporter.
func setUpTest(t *testing.T) (anthropic.Client, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()

	// Get API key or use dummy for replay mode
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-anthropic-key-for-replay"
	}

	// Create HTTP client with VCR (cassette name from t.Name())
	httpClient := vcr.NewHTTPClient(t)

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMiddleware(NewMiddleware(WithTracerProvider(tp))), //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	)

	return client, exporter
}

// assertSpanValid asserts all the common properties of an Anthropic span are valid.
func assertSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assert := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs("anthropic.messages.create")
	assert.Equal(codes.Unset, span.Stub.Status.Code)

	metadata := span.Metadata()
	assert.Equal("anthropic", metadata["provider"])
	assert.Equal("/v1/messages", metadata["endpoint"])

	// validate metrics
	metrics := span.Metrics()
	gtez := func(v float64) bool { return v >= 0 }
	gtz := func(v float64) bool { return v > 0 }

	// All expected metrics must be present - core metrics and cache metrics
	requiredMetrics := map[string]func(float64) bool{
		"prompt_tokens":                gtz,  // Should always be > 0
		"completion_tokens":            gtz,  // Should always be > 0
		"tokens":                       gtz,  // Should always be > 0
		"prompt_cached_tokens":         gtez, // Should always be ≥ 0 (even if 0)
		"prompt_cache_creation_tokens": gtez, // Should always be ≥ 0 (even if 0)
	}

	// First, ensure all required metrics are present
	for metricName := range requiredMetrics {
		assert.Contains(metrics, metricName, "Required metric %s is missing", metricName)
	}

	// Then validate all present metrics
	for n, v := range metrics {
		validator, ok := requiredMetrics[n]
		if !ok {
			// Unknown metric - just log it but don't fail the test
			t.Logf("Unknown metric %s with value %v - this is likely a new Anthropic metric", n, v)
			continue
		}
		assert.True(validator(v), "metric %s is not valid (value: %v)", n, v)
	}

	// a crude check to make sure all json is parsed
	assert.NotNil(span.Metadata())
	assert.NotNil(span.Input())
	assert.NotNil(span.Output())
}

// TestMultipleMessages tests tracing with multiple messages (conversation history)
func TestMultipleMessages(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	ctx := context.Background()
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model("claude-3-haiku-20240307"),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is the capital of France?")),
			anthropic.NewAssistantMessage(anthropic.NewTextBlock("The capital of France is Paris.")),
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is its population?")),
		},
		MaxTokens: 1024,
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response
	assert.Equal(t, "assistant", string(resp.Role))
	assert.NotEmpty(t, resp.Content)
	assert.Contains(t, strings.ToLower(resp.Content[0].Text), "million")

	// Validate span
	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange)

	// Verify input contains all messages
	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, "What is the capital of France?")
	assert.Contains(t, input, "The capital of France is Paris")
	assert.Contains(t, input, "What is its population?")
}

// TestWithTools tests tracing with tool use
func TestWithTools(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	ctx := context.Background()
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model("claude-3-haiku-20240307"),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What's the weather in San Francisco?")),
		},
		MaxTokens: 1024,
		Tools: []anthropic.ToolUnionParam{
			anthropic.ToolUnionParamOfTool(
				anthropic.ToolInputSchemaParam{
					Properties: map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state, e.g. San Francisco, CA",
						},
					},
					Required: []string{"location"},
				},
				"get_weather",
			),
		},
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response
	assert.Equal(t, "assistant", string(resp.Role))
	assert.NotEmpty(t, resp.Content)

	// Check if tool was called
	var foundToolUse bool
	for _, content := range resp.Content {
		if content.Type == "tool_use" {
			foundToolUse = true
			assert.Equal(t, "get_weather", content.Name)
			assert.NotNil(t, content.Input)
		}
	}
	assert.True(t, foundToolUse, "Expected tool_use in response")

	// Validate span
	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange)

	// Verify metadata contains tools
	metadata := span.Metadata()
	assert.Contains(t, metadata, "tools")
}

// TestStreamingWithTools tests tracing with streaming and tool use
func TestStreamingWithTools(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	ctx := context.Background()
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model("claude-3-haiku-20240307"),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What's the weather in Tokyo?")),
		},
		MaxTokens: 1024,
		Tools: []anthropic.ToolUnionParam{
			anthropic.ToolUnionParamOfTool(
				anthropic.ToolInputSchemaParam{
					Properties: map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state or country",
						},
					},
					Required: []string{"location"},
				},
				"get_weather",
			),
		},
	})

	var foundToolUse bool
	var toolName string

	// Iterate through streaming events
	for stream.Next() {
		event := stream.Current()
		switch eventVariant := event.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			if eventVariant.ContentBlock.Type == "tool_use" {
				foundToolUse = true
				toolName = eventVariant.ContentBlock.Name
			}
		}
	}
	require.NoError(t, stream.Err())
	timeRange := timer.Tick()

	// Verify tool was called
	assert.True(t, foundToolUse, "Expected tool_use in streaming response")
	assert.Equal(t, "get_weather", toolName)

	// Validate span
	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange)

	// Verify metadata
	metadata := span.Metadata()
	assert.Equal(t, true, metadata["stream"])
	assert.Contains(t, metadata, "tools")
}
