package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const testModel = "gpt-4o-mini"

// setUpTest is a helper function that sets up a new tracer provider and VCR for each test.
// It returns an openai client configured with VCR and an exporter.
func setUpTest(t *testing.T) (openai.Client, *oteltest.Exporter) {
	t.Helper()

	_, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()

	// Get API key or use dummy for replay mode
	apiKey := os.Getenv("OPENAI_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-openai-key-for-replay"
	}

	// Create HTTP client with VCR (cassette name from t.Name())
	httpClient := vcr.NewHTTPClient(t)

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMiddleware(NewMiddleware()), //nolint:bodyclose // false positive - NewMiddleware returns middleware func
	)

	return client, exporter
}

func TestError(t *testing.T) {
	_, exporter := setUpTest(t)
	assert := assert.New(t)

	errorware := func(_ *http.Request, _ NextMiddleware) (*http.Response, error) {
		return nil, errors.New("ye-olde-test-error")
	}

	client := openai.NewClient(
		option.WithMaxRetries(0),               // don't retry errors
		option.WithMiddleware(NewMiddleware()), //nolint:bodyclose // false positive - NewMiddleware returns middleware func
		option.WithMiddleware(errorware),
	)

	resp, err := client.Responses.New(context.Background(), responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hai")},
		Model: testModel,
	})
	require.Error(t, err)
	assert.Nil(resp)

	spans := exporter.Flush()
	assert.Len(spans, 1)
	span := spans[0]

	assert.Equal("openai.responses.create", span.Name())
	assert.Equal(codes.Error, span.Status().Code)
	assert.Contains(span.Status().Description, "ye-olde-test-error")

	events := span.Events()
	assert.Len(events, 1)

	// Find the exception.message attribute that contains our error message
	var errMsg string
	for _, attr := range events[0].Attributes {
		if attr.Key == "exception.message" {
			errMsg = attr.Value.AsString()
			break
		}
	}
	assert.Contains(errMsg, "ye-olde-test-error")
}

func TestOpenAIResponsesRequiredParams(t *testing.T) {
	client, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	timer := oteltest.NewTimer()
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("What is 13+4?")},
		Model: testModel,
	}

	resp, err := client.Responses.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	assert.Contains(resp.OutputText(), "17")

	ts := exporter.FlushOne()
	assertSpanValid(t, ts, timeRange)

	_ = ts.Input()
	outputRaw := ts.Output()
	outputJSON, err := json.Marshal(outputRaw)
	require.NoError(err)
	assert.Contains(string(outputJSON), "17")

}

func TestOpenAIResponsesKitchenSink(t *testing.T) {
	client, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	prompt := responses.ResponseNewParamsInputUnion{OfString: openai.String("what is 13+4?")}

	// Test with string output
	params := responses.ResponseNewParams{
		Input:             prompt,
		Model:             testModel,
		Instructions:      openai.String("Answer the question in a concise manner."),
		MaxOutputTokens:   openai.Int(100),
		ParallelToolCalls: openai.Bool(true),
		Store:             openai.Bool(false),
		Truncation:        responses.ResponseNewParamsTruncationAuto,
		Temperature:       param.Opt[float64]{Value: 0.5},
		TopP:              param.Opt[float64]{Value: 1.0},
		User:              openai.String("test user"),
	}

	timer := oteltest.NewTimer()
	resp, err := client.Responses.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertSpanValid(t, ts, timeRange)

	// Check input field
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	assert.Contains(string(inputJSON), "13+4")

	// Check output field
	output := ts.Output()
	text := getResponseText(t, output)
	assert.Contains(text, "17")

	metadata := ts.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal(testModel, metadata["model"])
	assert.Equal("Answer the question in a concise manner.", metadata["instructions"])
	assert.Equal("test user", metadata["user"])
	assert.Equal(0.5, metadata["temperature"])
	assert.Equal(1.0, metadata["top_p"])
	assert.Equal(true, metadata["parallel_tool_calls"])
	assert.Equal(false, metadata["store"])
	assert.Equal("auto", metadata["truncation"])
	assert.Equal(100.0, metadata["max_output_tokens"])
}

func TestOpenAIResponsesStreamingClose(t *testing.T) {
	client, exporter := setUpTest(t)
	require := require.New(t)
	assert := assert.New(t)

	ctx := context.Background()
	question := "Can you return me a list of the first 15 fibonacci numbers?"

	stream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(question)},
		Model: openai.ChatModelGPT4,
	})

	err := stream.Close()

	require.NoError(err)
	span := exporter.FlushOne()

	assert.Equal("openai.responses.create", span.Name())
	assert.Equal(codes.Unset, span.Status().Code)
	assert.Equal("", span.Status().Description)
	// FIXME we haven't iterated the body yet, so not much we can assert
}

func TestOpenAIResponsesStreaming(t *testing.T) {
	client, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	ctx := context.Background()
	question := "Can you return me a list of the first 15 fibonacci numbers?"

	timer := oteltest.NewTimer()
	stream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(question)},
		Model: openai.ChatModelGPT4,
	})

	var completeText string

	for stream.Next() {
		data := stream.Current()
		if data.Text != "" {
			completeText = data.Text
		}
	}
	require.NoError(stream.Err())
	timeRange := timer.Tick()

	ts := exporter.FlushOne()

	assertSpanValid(t, ts, timeRange)

	outputRaw := ts.Output()
	outputJSON, err := json.Marshal(outputRaw)
	require.NoError(err)
	output := string(outputJSON)
	for _, i := range []string{"1", "2", "3", "5", "8", "13"} {
		assert.Contains(completeText, i)
		assert.Contains(output, i)
	}
}

func TestOpenAIResponsesWithListInput(t *testing.T) {
	client, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Create a list of message inputs
	inputMessages := []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfMessage("What is 2+2?", "user"),
		responses.ResponseInputItemParamOfMessage("4", "assistant"),
		responses.ResponseInputItemParamOfMessage("What is 3+125?", "user"),
	}

	// Create the params with list input
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputMessages,
		},
		Model: testModel,
	}

	// Call the API
	timer := oteltest.NewTimer()
	resp, err := client.Responses.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	assertSpanValid(t, ts, timeRange)

	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	input := string(inputJSON)
	assert.Contains(input, "3+125")
	assert.Contains(input, "2+2")

	output := ts.Output()
	text := getResponseText(t, output)
	assert.Contains(text, "128")
}

func getResponseText(t *testing.T, resp any) string {
	t.Helper()
	// 	[]interface {}{map[string]interface {}{"content":[]interface {}{map[string]interface {}{"annotations":[]interface {}{}, "text":"Sure, here is the list of the first 15 Fibonacci numbers:\n\n[0, 1, 1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233, 377]", "type":"output_text"}}, "id":"msg_6815653f1238819281f85b0bd4f95c8e05b761f031bf9c10", "role":"assistant", "status":"completed", "type":"message"}}
	require := require.New(t)

	// Type assert the outer slice
	respSlice, ok := resp.([]interface{})
	require.True(ok, "response should be a slice")
	require.NotEmpty(respSlice, "response slice should not be empty")
	// Get first element and assert it's a map
	firstElem, ok := respSlice[0].(map[string]interface{})
	require.True(ok, "first element should be a map")
	// Get content and assert it's a map
	content, ok := firstElem["content"].([]interface{})
	require.True(ok, "content should be a slice")
	require.NotEmpty(content, "content should not be empty")
	// Get first content element and assert annotations and text
	firstContent, ok := content[0].(map[string]interface{})
	require.True(ok, "first content element should be a map")
	text, ok := firstContent["text"].(string)
	require.True(ok, "text should be a string")
	return text
}

// assertSpanValid asserts all the common properties of a span are valid.
func assertSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assert := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs("openai.responses.create")
	assert.Equal(codes.Unset, span.Stub.Status.Code)

	metadata := span.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Contains(testModel, metadata["model"])

	// validate metrics
	metrics := span.Metrics()
	gtz := func(v float64) bool { return v > 0 }
	gtez := func(v float64) bool { return v >= 0 }

	metricToValidator := map[string]func(float64) bool{
		"prompt_tokens":                         gtz,
		"completion_tokens":                     gtz,
		"tokens":                                gtz,
		"prompt_cached_tokens":                  gtez,
		"completion_cached_tokens":              gtez,
		"completion_reasoning_tokens":           gtez,
		"completion_accepted_prediction_tokens": gtez,
		"completion_rejected_prediction_tokens": gtez,
		"completion_audio_tokens":               gtez,
		"prompt_audio_tokens":                   gtez,
	}

	// Validate known metrics, but allow unknown metrics to pass through
	for n, v := range metrics {
		validator, ok := metricToValidator[n]
		if !ok {
			// Unknown metric - just log it but don't fail the test
			t.Logf("Unknown metric %s with value %v - this is likely a new OpenAI metric", n, v)
			continue
		}
		assert.True(validator(v), "metric %s is not valid", n)
	}

	// a crude check to make sure all json is parsed
	assert.NotNil(span.Metadata())
	assert.NotNil(span.Input())
	assert.NotNil(span.Output())
}

func TestTestOTelTracer(t *testing.T) {
	_, exporter := setUpTest(t)
	assert := assert.New(t)

	// crudely check we can create and test spans
	spans := exporter.Flush()
	assert.Empty(spans)

	tracer := otel.Tracer("test")
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	spans = exporter.Flush()
	assert.NotEmpty(spans)
	spans = exporter.Flush()
	assert.Empty(spans)
}

func TestParseUsageTokens(t *testing.T) {
	t.Run("basic_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"input_tokens":  float64(12),
			"output_tokens": float64(9),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(12), metrics["prompt_tokens"])
		assert.Equal(t, int64(9), metrics["completion_tokens"])
	})

	t.Run("with_tokens_details", func(t *testing.T) {
		usage := map[string]interface{}{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": float64(8),
				"audio_tokens":  float64(2),
			},
			"completion_tokens_details": map[string]interface{}{
				"reasoning_tokens": float64(3),
				"audio_tokens":     float64(2),
			},
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(10), metrics["prompt_tokens"])
		assert.Equal(t, int64(5), metrics["completion_tokens"])
		assert.Equal(t, int64(8), metrics["prompt_cached_tokens"])
		assert.Equal(t, int64(2), metrics["prompt_audio_tokens"])
		assert.Equal(t, int64(3), metrics["completion_reasoning_tokens"])
		assert.Equal(t, int64(2), metrics["completion_audio_tokens"])
	})

	t.Run("total_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"total_tokens": float64(25),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(25), metrics["tokens"])
	})

	t.Run("nil_usage", func(t *testing.T) {
		metrics := parseUsageTokens(nil)

		assert.Empty(t, metrics)
	})
}

func TestTranslateMetricPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"input", "prompt"},
		{"output", "completion"},
		{"other", "other"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := translateMetricPrefix(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestResponsesAPIUsesCorrectAttributes verifies that the Responses API uses
// braintrust.input_json and braintrust.output_json for proper UI display (issue #33)
func TestResponsesAPIUsesCorrectAttributes(t *testing.T) {
	client, exporter := setUpTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// Test with list input (array)
	inputMessages := []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfMessage("What is 2+2?", "user"),
		responses.ResponseInputItemParamOfMessage("4", "assistant"),
		responses.ResponseInputItemParamOfMessage("What is 3+5?", "user"),
	}

	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputMessages,
		},
		Model: testModel,
	}

	resp, err := client.Responses.New(context.Background(), params)
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	// Verify we're using braintrust.input_json (not braintrust.input)
	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(inputAttr, "should use braintrust.input_json for array input")
	assert.Contains(inputAttr.String(), "2+2")
	assert.Contains(inputAttr.String(), "3+5")

	// Verify we're using braintrust.output_json (not braintrust.output)
	outputAttr := ts.Attr("braintrust.output_json")
	require.NotNil(outputAttr, "should use braintrust.output_json for output")
	assert.Contains(outputAttr.String(), "8")
}

// TestResponsesAPIWithStringInput verifies that string inputs also work with _json attributes
func TestResponsesAPIWithStringInput(t *testing.T) {
	client, exporter := setUpTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// Test with string input
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("What is 7+6?")},
		Model: testModel,
	}

	resp, err := client.Responses.New(context.Background(), params)
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	// Verify string inputs also use braintrust.input_json
	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(inputAttr, "should use braintrust.input_json even for string input")
	assert.Contains(inputAttr.String(), "7+6")

	// Verify output uses braintrust.output_json
	outputAttr := ts.Attr("braintrust.output_json")
	require.NotNil(outputAttr, "should use braintrust.output_json for output")
	assert.Contains(outputAttr.String(), "13")
}

// TestResponsesAPIWithReasoning verifies that reasoning parameters are captured in metadata
func TestResponsesAPIWithReasoning(t *testing.T) {
	client, exporter := setUpTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// Test with reasoning parameters
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("What is the capital of France?")},
		Model: "gpt-5", // Use gpt-5 reasoning model
		Reasoning: shared.ReasoningParam{
			Effort:  shared.ReasoningEffortLow,
			Summary: shared.ReasoningSummaryAuto,
		},
	}

	resp, err := client.Responses.New(context.Background(), params)
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	metadata := ts.Metadata()
	assert.NotNil(metadata, "metadata should not be nil")

	reasoning, ok := metadata["reasoning"]
	require.True(ok, "reasoning not in metadata")
	assert.NotNil(reasoning, "reasoning should be captured in metadata")
	t.Logf("✓ Captured reasoning metadata: %+v", reasoning)

	reasonMap, ok := reasoning.(map[string]any)
	require.True(ok, "reasoning should be a map")
	assert.Equal("low", reasonMap["effort"], "effort should be 'low'")
	assert.Contains([]string{"auto", "detailed", "concise"}, reasonMap["summary"], "summary should be a valid value")
}
