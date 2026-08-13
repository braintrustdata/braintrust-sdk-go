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

func TestMiddlewareMatchesProxyPrefixedMessagesPath(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

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

	req := httptest.NewRequest("POST", "/clapi/transparent/aws_bedrock/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")

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

	next := func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}, nil
	}

	middleware := NewMiddleware(WithTracerProvider(tp)) //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	resp, err := middleware(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)

	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	span := exporter.FlushOne()
	span.AssertNameIs("anthropic.messages.create")
	assert.Equal(t, "anthropic", span.Metadata()["provider"])
	assert.NotContains(t, span.Metadata(), "endpoint")
}

func TestMessagesTracer(t *testing.T) {
	tp, _ := oteltest.Setup(t)
	cfg := &middlewareConfig{tracerProvider: tp}
	tracer := newMessagesTracer(cfg)
	assert.NotNil(t, tracer)
	assert.False(t, tracer.streaming)
	assert.Equal(t, "anthropic", tracer.metadata["provider"])
	assert.NotContains(t, tracer.metadata, "endpoint")

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
		"service_tier":  float64(42),
	}

	metrics := parseUsageTokens(usage)

	assert.Equal(t, map[string]int64{
		"prompt_tokens":     12,
		"completion_tokens": 9,
		"tokens":            21,
	}, metrics)
}

func TestParseUsageTokensOmitsMissingAndInvalidMetrics(t *testing.T) {
	assert.Empty(t, parseUsageTokens(nil))
	assert.Empty(t, parseUsageTokens(map[string]interface{}{}))
	assert.Empty(t, parseUsageTokens(map[string]interface{}{
		"input_tokens":  float64(-1),
		"output_tokens": "unknown",
	}))
}

func TestParseUsageTokensWithCacheTTLs(t *testing.T) {
	metrics := parseUsageTokens(map[string]interface{}{
		"input_tokens":                float64(10),
		"output_tokens":               float64(5),
		"cache_creation_input_tokens": float64(100),
		"cache_read_input_tokens":     float64(25),
		"cache_creation": map[string]interface{}{
			"ephemeral_5m_input_tokens": float64(40),
			"ephemeral_1h_input_tokens": float64(60),
		},
	})

	assert.Equal(t, map[string]int64{
		"prompt_tokens":                   135,
		"completion_tokens":               5,
		"tokens":                          140,
		"prompt_cached_tokens":            25,
		"prompt_cache_creation_5m_tokens": 40,
		"prompt_cache_creation_1h_tokens": 60,
	}, metrics)
}

func TestMessagesTracerCapturesRequestMetadata(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := newMessagesTracer(&middlewareConfig{tracerProvider: tp})
	requestBody := `{
		"model":"claude-haiku-4-5",
		"max_tokens":128,
		"temperature":0,
		"top_p":0.9,
		"stop_sequences":["END"],
		"top_k":10,
		"stream":false,
		"metadata":{"user_id":"private"},
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[{"role":"user","content":"Hello"}],
		"tools":[{
			"name":"get_weather",
			"description":"Get the weather",
			"input_schema":{"type":"object","properties":{"city":{"type":"string"}}},
			"strict":true
		}],
		"tool_choice":{"type":"any","disable_parallel_tool_use":true}
	}`

	_, span, err := tracer.StartSpan(t.Context(), time.Now(), strings.NewReader(requestBody))
	require.NoError(t, err)
	span.End()

	exported := exporter.FlushOne()
	metadata := exported.Metadata()
	assert.Equal(t, map[string]any{
		"provider":       "anthropic",
		"model":          "claude-haiku-4-5",
		"max_tokens":     float64(128),
		"temperature":    float64(0),
		"top_p":          float64(0.9),
		"top_k":          float64(10),
		"stop_sequences": []any{"END"},
		"stream":         false,
		"metadata":       map[string]any{"user_id": "private"},
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": float64(1024),
		},
		"tools": []any{map[string]any{
			"name":        "get_weather",
			"description": "Get the weather",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
			},
			"strict": true,
		}},
		"tool_choice": map[string]any{
			"type":                      "any",
			"disable_parallel_tool_use": true,
		},
	}, metadata)
}

func TestMessagesTracerCapturesNativeResponse(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := newMessagesTracer(&middlewareConfig{tracerProvider: tp})

	_, span, err := tracer.StartSpan(t.Context(), time.Now(), strings.NewReader(`{
		"model":"claude-haiku-4-5",
		"max_tokens":128,
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	require.NoError(t, err)
	require.NoError(t, tracer.TagSpan(span, strings.NewReader(`{
		"role":"assistant",
		"content":[{"type":"tool_use","id":"toolu_123","name":"lookup","input":{"q":"answer"}}],
		"model":"claude-haiku-4-5-20251001",
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`)))
	span.End()

	exported := exporter.FlushOne()
	assert.Equal(t, map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{
			"type":  "tool_use",
			"id":    "toolu_123",
			"name":  "lookup",
			"input": map[string]any{"q": "answer"},
		}},
		"stop_reason": "tool_use",
	}, exported.Output())
	assert.Equal(t, "claude-haiku-4-5-20251001", exported.Metadata()["model"])
	assert.NotContains(t, exported.Metrics(), "time_to_first_token")
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
	assert.NotContains(t, metadata, "endpoint")
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

	assertStreamingSpanValid(t, span, timeRange)

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
	assert.NotContains(t, metadata, "endpoint")
	assert.Equal(t, "claude-3-haiku-20240307", metadata["model"])
	assert.Equal(t, float64(512), metadata["max_tokens"])
	assert.Equal(t, 0.8, metadata["temperature"])
	assert.Equal(t, 0.95, metadata["top_p"])
	assert.Equal(t, true, metadata["stream"])

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

// assertSpanValid asserts all the common properties of a non-streaming Anthropic span.
func assertSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assertSpanValidWithName(t, span, timeRange, "anthropic.messages.create")
}

// assertStreamingSpanValid asserts all the common properties of a streaming Anthropic span.
func assertStreamingSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assertSpanValidWithName(t, span, timeRange, "anthropic.messages.stream")
}

func assertSpanValidWithName(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange, expectedName string) {
	t.Helper()
	assert := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs(expectedName)
	assert.Equal(codes.Unset, span.Stub.Status.Code)

	metadata := span.Metadata()
	assert.Equal("anthropic", metadata["provider"])
	assert.NotContains(metadata, "endpoint")

	// validate metrics
	metrics := span.Metrics()
	gtez := func(v float64) bool { return v >= 0 }
	gtz := func(v float64) bool { return v > 0 }

	requiredMetrics := map[string]func(float64) bool{
		"prompt_tokens":     gtz,
		"completion_tokens": gtz,
		"tokens":            gtz,
	}
	if expectedName == "anthropic.messages.stream" {
		requiredMetrics["time_to_first_token"] = gtez
	}
	allowedOptionalMetrics := map[string]func(float64) bool{
		"prompt_cached_tokens":            gtez,
		"prompt_cache_creation_tokens":    gtez,
		"prompt_cache_creation_5m_tokens": gtez,
		"prompt_cache_creation_1h_tokens": gtez,
		"completion_reasoning_tokens":     gtez,
	}

	// First, ensure all required metrics are present
	for metricName := range requiredMetrics {
		assert.Contains(metrics, metricName, "Required metric %s is missing", metricName)
	}

	// Then validate all present metrics
	for n, v := range metrics {
		validator, ok := requiredMetrics[n]
		if !ok {
			validator, ok = allowedOptionalMetrics[n]
		}
		assert.True(ok, "metric %s is not allowed by the instrumentation spec", n)
		if ok {
			assert.True(validator(v), "metric %s is not valid (value: %v)", n, v)
		}
	}

	// a crude check to make sure all json is parsed
	assert.NotNil(span.Metadata())
	assert.NotNil(span.Input())
	assert.NotNil(span.Output())
}

// TestStreamingWithThinking tests tracing with streaming and extended thinking enabled
func TestStreamingWithThinking(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	ctx := context.Background()
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 16000,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is 27 * 453?")),
		},
		Thinking: anthropic.ThinkingConfigParamOfEnabled(10000),
	})

	var thinkingText, responseText string

	for stream.Next() {
		event := stream.Current()
		switch eventVariant := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch deltaVariant := eventVariant.Delta.AsAny().(type) {
			case anthropic.ThinkingDelta:
				thinkingText += deltaVariant.Thinking
			case anthropic.TextDelta:
				responseText += deltaVariant.Text
			}
		}
	}
	require.NoError(t, stream.Err())
	timeRange := timer.Tick()

	// Verify we got thinking and response content from the stream
	assert.NotEmpty(t, thinkingText, "should have received thinking text")
	assert.NotEmpty(t, responseText, "should have received response text")

	// Validate span
	span := exporter.FlushOne()
	assertStreamingSpanValid(t, span, timeRange)

	// Verify the span output contains both thinking and text blocks
	outputStr := span.Attr("braintrust.output_json").String()
	assert.Contains(t, outputStr, `"type":"thinking"`)
	assert.Contains(t, outputStr, `"type":"text"`)

	// Verify thinking content was captured (not empty)
	assert.Contains(t, outputStr, `"thinking":`)
	assert.NotContains(t, outputStr, `"thinking":""`, "thinking content should not be empty")

	// Verify signature was captured
	assert.Contains(t, outputStr, `"signature":`)

	// Verify the streamed text matches what's in the span
	assert.Contains(t, outputStr, responseText[:10])

	metadata := span.Metadata()
	assert.Equal(t, true, metadata["stream"])
	assert.NotNil(t, metadata["thinking"])
}

// TestStreamingWithCitations tests tracing with streaming and document citations enabled.
func TestStreamingWithCitations(t *testing.T) {
	client, exporter := setUpTest(t)

	document := anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
		Data: "France's capital is Paris. Paris lies on the Seine River. The Louvre Museum is also in Paris.",
	})
	document.OfDocument.Title = anthropic.String("France facts")
	document.OfDocument.Citations.Enabled = anthropic.Bool(true)

	timer := oteltest.NewTimer()
	ctx := context.Background()
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				document,
				anthropic.NewTextBlock("Use only the provided document. In one sentence, what is the capital of France? Include citations in the answer."),
			),
		},
	})

	var responseText string
	var citationDeltas int

	for stream.Next() {
		event := stream.Current()
		switch eventVariant := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch deltaVariant := eventVariant.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				responseText += deltaVariant.Text
			case anthropic.CitationsDelta:
				citationDeltas++
			}
		}
	}
	require.NoError(t, stream.Err())
	timeRange := timer.Tick()

	require.NotEmpty(t, responseText)
	require.Greater(t, citationDeltas, 0, "expected citations_delta events in streaming response")

	span := exporter.FlushOne()
	assertStreamingSpanValid(t, span, timeRange)

	output := span.Output()
	message, ok := output.(map[string]any)
	require.True(t, ok, "expected provider-native assistant output")

	content, ok := message["content"].([]any)
	require.True(t, ok, "expected assistant message content to be an array")
	require.NotEmpty(t, content)

	foundTextBlockWithCitations := false
	for _, blockAny := range content {
		block, ok := blockAny.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] != "text" {
			continue
		}
		citations, ok := block["citations"].([]any)
		if ok && len(citations) > 0 {
			foundTextBlockWithCitations = true
			break
		}
	}

	assert.True(t, foundTextBlockWithCitations, "expected streamed span output to preserve citations")
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

	// Verify metadata contains the provider-native tool definition.
	metadata := span.Metadata()
	assertAnthropicFunctionTool(t, metadata, "get_weather")
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
	assertStreamingSpanValid(t, span, timeRange)

	// Verify metadata
	metadata := span.Metadata()
	assert.Equal(t, true, metadata["stream"])
	assertAnthropicFunctionTool(t, metadata, "get_weather")

	output, ok := span.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_use", output["stop_reason"])
	content, ok := output["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	toolUse, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "get_weather", toolUse["name"])
	assert.Equal(t, map[string]any{"location": "Tokyo"}, toolUse["input"])
}

func assertAnthropicFunctionTool(t *testing.T, metadata map[string]any, expectedName string) {
	t.Helper()
	tools, ok := metadata["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, expectedName, tool["name"])
	assert.Contains(t, tool, "input_schema")
}
