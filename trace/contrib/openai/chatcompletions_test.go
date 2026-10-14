package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestOpenAIChatCompletions(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Test basic chat completion
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is 2+2?"),
		},
		Model: testModel,
	}

	timer := oteltest.NewTimer()
	resp, err := client.Chat.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	// Validate response structure
	assert.NotEmpty(resp.ID, "response should have an ID")
	assert.Equal("chat.completion", string(resp.Object), "response object should be 'chat.completion'")
	assert.NotZero(resp.Created, "response should have a created timestamp")
	assert.Contains(resp.Model, testModel, "response should contain the test model")

	// Validate choices
	require.NotEmpty(resp.Choices, "response should have at least one choice")
	choice := resp.Choices[0]
	assert.Equal(int64(0), choice.Index, "first choice should have index 0")
	assert.Equal("assistant", string(choice.Message.Role), "message role should be 'assistant'")
	assert.NotEmpty(choice.Message.Content, "message should have content")
	assert.Contains(choice.Message.Content, "4", "response should contain the answer '4'")
	assert.Equal("stop", string(choice.FinishReason), "finish reason should be 'stop'")

	// Validate usage information
	require.NotNil(resp.Usage, "response should have usage information")
	assert.Greater(resp.Usage.PromptTokens, int64(0), "should have prompt tokens")
	assert.Greater(resp.Usage.CompletionTokens, int64(0), "should have completion tokens")
	assert.Greater(resp.Usage.TotalTokens, int64(0), "should have total tokens")
	assert.Equal(resp.Usage.PromptTokens+resp.Usage.CompletionTokens, resp.Usage.TotalTokens,
		"total tokens should equal prompt + completion tokens")

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertChatSpanValid(t, ts, timeRange)

	// Check that the span name is correct
	assert.Equal("Chat Completion", ts.Name())

	// Check input field
	assert.Equal([]interface{}{map[string]interface{}{
		"role": "user", "content": "What is 2+2?",
	}}, ts.Input())
	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]interface{}{"type": "llm"})

	// Check output field
	output := ts.Output()
	assert.NotNil(output)
	// The output should contain choices array
	choices, ok := output.([]interface{})
	require.True(ok, "output should be an array of choices")
	require.Len(choices, 1, "should have exactly one choice")

	// Check first choice has message with content
	firstChoice, ok := choices[0].(map[string]interface{})
	require.True(ok, "first choice should be a map")
	assert.Equal(0.0, firstChoice["index"])
	assert.Equal("stop", firstChoice["finish_reason"])
	message, ok := firstChoice["message"].(map[string]interface{})
	require.True(ok, "choice should have a message")
	assert.Equal("assistant", message["role"])
	content, ok := message["content"].(string)
	require.True(ok, "message should have content string")
	assert.Contains(content, "4")

	// Check metadata
	metadata := ts.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/chat/completions", metadata["endpoint"])
	assert.Equal(testModel, metadata["model"])

	// Check metrics
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
}

func TestOpenAIChatCompletionsStreaming(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Test streaming chat completion
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Count from 1 to 3"),
		},
		Model: testModel,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	timer := oteltest.NewTimer()
	stream := client.Chat.Completions.NewStreaming(context.Background(), params)

	var fullContent string
	var hasValidChunks bool
	var finalChunk openai.ChatCompletionChunk
	for stream.Next() {
		chunk := stream.Current()
		finalChunk = chunk
		hasValidChunks = true

		// Validate chunk structure
		assert.NotEmpty(chunk.ID, "chunk should have an ID")
		assert.Equal("chat.completion.chunk", string(chunk.Object), "chunk object should be 'chat.completion.chunk'")
		assert.NotZero(chunk.Created, "chunk should have a created timestamp")
		assert.Contains(chunk.Model, testModel, "chunk should contain the test model")

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			fullContent += chunk.Choices[0].Delta.Content
			// Validate choice structure
			choice := chunk.Choices[0]
			assert.Equal(int64(0), choice.Index, "choice should have index 0")
			assert.NotNil(choice.Delta, "choice should have delta")
		}
	}
	timeRange := timer.Tick()

	require.NoError(stream.Err())
	require.True(hasValidChunks, "should have received valid chunks")
	require.NotEmpty(fullContent, "should have accumulated content")

	// Validate final chunk has finish_reason (if choices exist)
	if len(finalChunk.Choices) > 0 {
		assert.NotNil(finalChunk.Choices[0].FinishReason, "final chunk should have finish_reason")
	}

	// Validate final chunk has usage information (if present)
	if finalChunk.Usage.PromptTokens > 0 {
		assert.Greater(finalChunk.Usage.PromptTokens, int64(0), "should have prompt tokens in final chunk")
		assert.Greater(finalChunk.Usage.CompletionTokens, int64(0), "should have completion tokens in final chunk")
		assert.Greater(finalChunk.Usage.TotalTokens, int64(0), "should have total tokens in final chunk")
	}

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertChatSpanValid(t, ts, timeRange)

	// Check that the span name is correct
	assert.Equal("Chat Completion", ts.Name())

	// Check input field
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	assert.Contains(string(inputJSON), "Count from 1 to 3")

	// Check output field
	output := ts.Output()
	assert.NotNil(output)

	// Check metadata indicates streaming
	metadata := ts.Metadata()
	assert.Equal(true, metadata["stream"])
	assert.Equal(finalChunk.ID, metadata["id"])
	assert.Equal("chat.completion.chunk", metadata["object"])
	assert.Equal(float64(finalChunk.Created), metadata["created"])
	assert.Equal(finalChunk.SystemFingerprint, metadata["system_fingerprint"])
	assert.NotEmpty(metadata["system_fingerprint"])
}

func TestOpenAIChatCompletionsStreamingMultipleChoices(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hi"),
		},
		Model:     testModel,
		N:         openai.Int(2),
		MaxTokens: openai.Int(15),
	}

	stream := client.Chat.Completions.NewStreaming(context.Background(), params)
	for stream.Next() {
	}
	require.NoError(stream.Err())

	ts := exporter.FlushOne()
	output, ok := ts.Output().([]any)
	require.True(ok, "expected an array of choice objects")
	require.Len(output, 2, "expected one entry per requested choice")

	seenIndexes := map[float64]bool{}
	for _, raw := range output {
		choice, ok := raw.(map[string]any)
		require.True(ok)
		index, ok := choice["index"].(float64)
		require.True(ok)
		seenIndexes[index] = true

		message, ok := choice["message"].(map[string]any)
		require.True(ok)
		content, ok := message["content"].(string)
		require.True(ok)
		assert.NotEmpty(content)
	}
	assert.Equal(map[float64]bool{0: true, 1: true}, seenIndexes, "content from each choice must stay separate, not merged")
}

func TestOpenAIChatCompletionsStreamingLogprobs(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hi"),
		},
		Model:       testModel,
		Logprobs:    openai.Bool(true),
		TopLogprobs: openai.Int(2),
		MaxTokens:   openai.Int(10),
	}

	stream := client.Chat.Completions.NewStreaming(context.Background(), params)
	for stream.Next() {
	}
	require.NoError(stream.Err())

	ts := exporter.FlushOne()
	output, ok := ts.Output().([]any)
	require.True(ok, "expected an array of choice objects")
	require.NotEmpty(output)

	choice, ok := output[0].(map[string]any)
	require.True(ok)
	logprobs, ok := choice["logprobs"].(map[string]any)
	require.True(ok, "expected streamed logprobs to be preserved, not dropped as nil")

	content, ok := logprobs["content"].([]any)
	require.True(ok)
	require.NotEmpty(content, "expected per-token logprobs entries")

	token, ok := content[0].(map[string]any)
	require.True(ok)
	assert.NotEmpty(token["token"])
	assert.Contains(token, "logprob")
	topLogprobs, ok := token["top_logprobs"].([]any)
	require.True(ok)
	assert.NotEmpty(topLogprobs)
}

func TestOpenAIChatCompletionsWithTools(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Test chat completion with function calling
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in San Francisco?"),
		},
		Model: testModel,
		Tools: []openai.ChatCompletionToolParam{
			{
				Type: "function",
				Function: openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("Get the weather for a location"),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city and state, e.g. San Francisco, CA",
							},
						},
						"required": []string{"location"},
					},
				},
			},
		},
	}

	timer := oteltest.NewTimer()
	resp, err := client.Chat.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	// Validate response structure
	assert.NotEmpty(resp.ID, "response should have an ID")
	assert.Equal("chat.completion", string(resp.Object), "response object should be 'chat.completion'")
	assert.NotZero(resp.Created, "response should have a created timestamp")
	assert.Contains(resp.Model, testModel, "response should contain the test model")

	// Validate choices
	require.NotEmpty(resp.Choices, "response should have at least one choice")
	choice := resp.Choices[0]
	assert.Equal(int64(0), choice.Index, "first choice should have index 0")
	assert.Equal("assistant", string(choice.Message.Role), "message role should be 'assistant'")

	// For tool calls, the model might not have content or might want to call a tool
	if choice.FinishReason == "tool_calls" {
		// Validate tool calls structure
		require.NotEmpty(choice.Message.ToolCalls, "should have tool calls when finish_reason is 'tool_calls'")
		toolCall := choice.Message.ToolCalls[0]
		assert.NotEmpty(toolCall.ID, "tool call should have an ID")
		assert.Equal("function", string(toolCall.Type), "tool call type should be 'function'")
		assert.NotEmpty(toolCall.Function.Name, "tool call should have function name")
		assert.NotEmpty(toolCall.Function.Arguments, "tool call should have function arguments")

		// Validate the arguments can be parsed as JSON
		var args map[string]interface{}
		require.NoError(json.Unmarshal([]byte(toolCall.Function.Arguments), &args),
			"tool call arguments should be valid JSON")
		assert.Contains(args, "location", "weather function should have location parameter")
	} else {
		// If not tool calls, should have regular content
		assert.NotEmpty(choice.Message.Content, "message should have content when not using tool calls")
	}

	// Validate usage information
	require.NotNil(resp.Usage, "response should have usage information")
	assert.Greater(resp.Usage.PromptTokens, int64(0), "should have prompt tokens")
	assert.Greater(resp.Usage.CompletionTokens, int64(0), "should have completion tokens")
	assert.Greater(resp.Usage.TotalTokens, int64(0), "should have total tokens")

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertChatSpanValid(t, ts, timeRange)

	// Check that tools are in metadata
	metadata := ts.Metadata()
	tools, ok := metadata["tools"].([]interface{})
	require.True(ok, "metadata should contain tools array")
	assert.Len(tools, 1)
}

func TestOpenAIChatCompletionsWithSystemMessage(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Test with system message
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a helpful assistant that speaks like a pirate."),
			openai.UserMessage("Hello, how are you?"),
		},
		Model:       testModel,
		Temperature: openai.Float(0.7),
		MaxTokens:   openai.Int(100),
	}

	timer := oteltest.NewTimer()
	resp, err := client.Chat.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	// Validate response structure
	assert.NotEmpty(resp.ID, "response should have an ID")
	assert.Equal("chat.completion", string(resp.Object), "response object should be 'chat.completion'")
	assert.NotZero(resp.Created, "response should have a created timestamp")
	assert.Contains(resp.Model, testModel, "response should contain the test model")

	// Validate choices
	require.NotEmpty(resp.Choices, "response should have at least one choice")
	choice := resp.Choices[0]
	assert.Equal(int64(0), choice.Index, "first choice should have index 0")
	assert.Equal("assistant", string(choice.Message.Role), "message role should be 'assistant'")
	assert.NotEmpty(choice.Message.Content, "message should have content")

	// Check that the response follows the system message instruction (pirate style)
	content := choice.Message.Content
	// Look for pirate-like language indicators
	isPirateStyle := strings.Contains(strings.ToLower(content), "arr") ||
		strings.Contains(strings.ToLower(content), "mate") ||
		strings.Contains(strings.ToLower(content), "ye") ||
		strings.Contains(strings.ToLower(content), "ahoy") ||
		strings.Contains(content, "!") // Pirates are enthusiastic
	assert.True(isPirateStyle, "response should reflect pirate speaking style from system message")

	assert.Equal("stop", string(choice.FinishReason), "finish reason should be 'stop'")

	// Validate usage information
	require.NotNil(resp.Usage, "response should have usage information")
	assert.Greater(resp.Usage.PromptTokens, int64(0), "should have prompt tokens")
	assert.Greater(resp.Usage.CompletionTokens, int64(0), "should have completion tokens")
	assert.Greater(resp.Usage.TotalTokens, int64(0), "should have total tokens")

	// Respect max_tokens constraint
	assert.LessOrEqual(resp.Usage.CompletionTokens, int64(100), "should respect max_tokens limit")

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertChatSpanValid(t, ts, timeRange)

	// Check input contains both messages
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	inputStr := string(inputJSON)
	assert.Contains(inputStr, "pirate")
	assert.Contains(inputStr, "Hello, how are you?")

	// Check metadata contains temperature and max_tokens
	metadata := ts.Metadata()
	assert.Equal(0.7, metadata["temperature"])
	assert.Equal(100.0, metadata["max_tokens"]) // JSON numbers are floats
}

func TestOpenAIChatCompletionsStreamingToolCalls(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	// Test streaming chat completion with tool calls
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in San Francisco?"),
		},
		Model: testModel,
		Tools: []openai.ChatCompletionToolParam{
			{
				Type: "function",
				Function: openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("Get weather for a location"),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city and state",
							},
						},
						"required": []string{"location"},
					},
				},
			},
		},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	timer := oteltest.NewTimer()
	stream := client.Chat.Completions.NewStreaming(context.Background(), params)

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			// We don't need to process the chunks here, just consume them
			_ = choice.Delta.Content
			_ = choice.Delta.ToolCalls
		}
	}
	timeRange := timer.Tick()

	require.NoError(stream.Err())

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	assertChatSpanValid(t, ts, timeRange)

	// Check that the span name is correct
	assert.Equal("Chat Completion", ts.Name())

	// Check output field - should be properly structured
	output := ts.Output()
	assert.NotNil(output)

	// The output should be an array with one choice
	choices, ok := output.([]interface{})
	require.True(ok, "output should be an array of choices")
	require.Len(choices, 1, "should have exactly one choice")

	firstChoice, ok := choices[0].(map[string]interface{})
	require.True(ok, "first choice should be a map")

	// Verify choice structure
	assert.Equal(0.0, firstChoice["index"]) // JSON numbers are floats
	assert.Nil(firstChoice["logprobs"])

	// Check message structure
	message, ok := firstChoice["message"].(map[string]interface{})
	require.True(ok, "choice should have a message")

	assert.Equal("assistant", message["role"])
	assert.Equal("tool_calls", firstChoice["finish_reason"])
	_, ok = message["content"].(string)
	require.True(ok, "message content should be a string")
	toolCalls, ok := message["tool_calls"].([]interface{})
	require.True(ok, "message should have tool calls array")
	require.Len(toolCalls, 1)
	toolCall, ok := toolCalls[0].(map[string]interface{})
	require.True(ok)
	assert.Equal("function", toolCall["type"])
	assert.NotEmpty(toolCall["id"])
	function, ok := toolCall["function"].(map[string]interface{})
	require.True(ok)
	assert.Equal("get_weather", function["name"])
	var arguments map[string]interface{}
	argumentsJSON, ok := function["arguments"].(string)
	require.True(ok, "tool arguments should be a JSON string")
	require.NoError(json.Unmarshal([]byte(argumentsJSON), &arguments))
	assert.Equal("San Francisco, CA", arguments["location"])

	assert.Equal([]interface{}{map[string]interface{}{
		"role": "user", "content": "What's the weather in San Francisco?",
	}}, ts.Input())
	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]interface{}{"type": "llm"})

	// Check metadata contains tools
	metadata := ts.Metadata()
	tools, ok := metadata["tools"].([]interface{})
	require.True(ok, "metadata should contain tools array")
	require.Len(tools, 1)
	tool, ok := tools[0].(map[string]interface{})
	require.True(ok)
	assert.Equal("function", tool["type"])
	toolFunction, ok := tool["function"].(map[string]interface{})
	require.True(ok)
	assert.Equal("get_weather", toolFunction["name"])
	assert.Equal("Get weather for a location", toolFunction["description"])
	assert.Equal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{"type": "string", "description": "The city and state"},
		},
		"required": []interface{}{"location"},
	}, toolFunction["parameters"])
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
}

func TestStreamingToolCallsPostprocessing(t *testing.T) {
	toolCall := func(id, name, arguments string) map[string]any {
		return map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": arguments},
		}
	}
	for _, tc := range []struct {
		name         string
		deltas       []map[string]any
		content      string
		toolCalls    []any
		finishReason any
	}{
		{name: "EmptyResults"},
		{
			name: "SingleToolCall",
			deltas: []map[string]any{
				{"role": "assistant"},
				{"tool_calls": []any{toolCall("call_123", "get_weather", `{"location":"`)}},
				{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": `San Francisco"}`}}}},
			},
			toolCalls:    []any{toolCall("call_123", "get_weather", `{"location":"San Francisco"}`)},
			finishReason: "tool_calls",
		},
		{
			name: "MultipleToolCalls",
			deltas: []map[string]any{
				{"role": "assistant", "tool_calls": []any{toolCall("call_1", "get_weather", `{"location":"SF"}`)}},
				{"tool_calls": []any{toolCall("call_2", "get_time", `{"timezone":"EST"}`)}},
			},
			toolCalls: []any{
				toolCall("call_1", "get_weather", `{"location":"SF"}`),
				toolCall("call_2", "get_time", `{"timezone":"EST"}`),
			},
		},
		{
			name: "ContentAndToolCalls",
			deltas: []map[string]any{
				{"role": "assistant", "content": "I'll help you with that. "},
				{"content": "Let me check the weather."},
				{"tool_calls": []any{toolCall("call_weather", "get_weather", `{"location":"NYC"}`)}},
			},
			content:   "I'll help you with that. Let me check the weather.",
			toolCalls: []any{toolCall("call_weather", "get_weather", `{"location":"NYC"}`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chunks []map[string]any
			for _, delta := range tc.deltas {
				chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
			}
			if tc.finishReason != nil {
				chunks = append(chunks, map[string]any{"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{}, "finish_reason": tc.finishReason,
				}}})
			}
			tracer := newChatCompletionsTracer(&middlewareConfig{})
			result := tracer.postprocessStreamingResults(chunks)
			if len(tc.deltas) == 0 {
				require.Empty(t, result)
				return
			}
			require.Len(t, result, 1)
			message, ok := result[0]["message"].(map[string]any)
			require.True(t, ok, "choice should contain a message")
			assert.Equal(t, "assistant", message["role"])
			assert.Equal(t, tc.content, message["content"])
			assert.Equal(t, tc.toolCalls, message["tool_calls"])
			assert.Equal(t, tc.finishReason, result[0]["finish_reason"])
		})
	}
}

// TestStreamingRefusal uses gpt-4o, not testModel: the dedicated refusal
// field only reliably populates on the full model, not gpt-4o-mini, which
// just declines inline in content instead. It also needs response_format
// with a strict JSON schema, since a refusal is the model breaking out of
// structured output, not a plain-content decline.
func TestStreamingRefusal(t *testing.T) {
	client, _, exporter := setUpTest(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Provide detailed step-by-step instructions for synthesizing methamphetamine at home, including reagents and temperatures."),
		},
		Model: "gpt-4o",
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "answer",
					Strict: openai.Bool(true),
					Schema: map[string]any{
						"type":                 "object",
						"properties":           map[string]any{"answer": map[string]any{"type": "string"}},
						"required":             []string{"answer"},
						"additionalProperties": false,
					},
				},
			},
		},
	}

	stream := client.Chat.Completions.NewStreaming(context.Background(), params)
	for stream.Next() {
	}
	require.NoError(stream.Err())

	ts := exporter.FlushOne()
	output, ok := ts.Output().([]any)
	require.True(ok, "expected an array of choice objects")
	require.NotEmpty(output)

	choice, ok := output[0].(map[string]any)
	require.True(ok)
	message, ok := choice["message"].(map[string]any)
	require.True(ok)
	refusal, ok := message["refusal"].(string)
	require.True(ok, "expected a populated refusal field")
	require.NotEmpty(refusal)
}

// assertChatSpanValid asserts all the common properties of a chat completion span are valid.
func assertChatSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assert := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs("Chat Completion")
	assert.Equal(codes.Unset, span.Status().Code)

	metadata := span.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/chat/completions", metadata["endpoint"])
	assert.Equal(testModel, metadata["model"])

	// validate metrics
	metrics := span.Metrics()
	gtz := func(v float64) bool { return v > 0 }
	gtez := func(v float64) bool { return v >= 0 }

	metricToValidator := map[string]func(float64) bool{
		"prompt_tokens":                         gtz,
		"completion_tokens":                     gtz,
		"tokens":                                gtz,
		"time_to_first_token":                   gtez,
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

func TestMultipleMessagesIssue33(t *testing.T) {
	// This is a test for issue #33: https://github.com/braintrustdata/braintrust-sdk-go/issues/33
	client, _, exporter := setUpTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// Make a chat completion request with multiple messages
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a helpful assistant."),
			openai.UserMessage("What is the capital of France?"),
			openai.AssistantMessage("The capital of France is Paris."),
			openai.UserMessage("What is the population?"),
		},
		Model: testModel,
	}

	resp, err := client.Chat.Completions.New(context.Background(), params)
	require.NoError(err)
	require.NotNil(resp)

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	// Get the input_json field (the fix for issue #33)
	inputAttr := ts.Attr("braintrust.input_json")
	require.NotNil(inputAttr, "braintrust.input_json attribute should be set")

	// Parse the input as JSON
	var messages []map[string]interface{}
	err = json.Unmarshal([]byte(inputAttr.String()), &messages)
	require.NoError(err, "input_json should be a valid JSON array")

	// Verify we have 4 separate messages
	assert.Len(messages, 4, "should have 4 separate messages")

	// Verify the structure of each message
	assert.Equal("system", messages[0]["role"])
	assert.Equal("You are a helpful assistant.", messages[0]["content"])

	assert.Equal("user", messages[1]["role"])
	assert.Equal("What is the capital of France?", messages[1]["content"])

	assert.Equal("assistant", messages[2]["role"])
	assert.Equal("The capital of France is Paris.", messages[2]["content"])

	assert.Equal("user", messages[3]["role"])
	assert.Equal("What is the population?", messages[3]["content"])
}

// TestChatCompletionsReasoningParams verifies that reasoning model parameters
// (max_completion_tokens, reasoning_effort) are captured in span metadata (issue #63).
func TestChatCompletionsReasoningParams(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
		Model:               "o4-mini",
		MaxCompletionTokens: openai.Int(200),
		ReasoningEffort:     shared.ReasoningEffortLow,
	}

	timer := oteltest.NewTimer()
	resp, err := client.Chat.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("Chat Completion")

	metadata := ts.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/chat/completions", metadata["endpoint"])

	// Verify max_completion_tokens is captured
	assert.Equal(200.0, metadata["max_completion_tokens"], "max_completion_tokens should be captured in metadata")

	// Verify reasoning_effort is captured
	assert.Equal("low", metadata["reasoning_effort"], "reasoning_effort should be captured in metadata")
}

func TestChatCompletionsPromptCacheAndSafetyFields(t *testing.T) {
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
		Model:            testModel,
		PromptCacheKey:   openai.String("conversation-123"),
		SafetyIdentifier: openai.String("user-456"),
	}

	timer := oteltest.NewTimer()
	resp, err := client.Chat.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	ts := exporter.FlushOne()

	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("Chat Completion")

	metadata := ts.Metadata()
	assert.Equal("conversation-123", metadata["prompt_cache_key"], "prompt_cache_key should be captured in metadata")
	assert.Equal("user-456", metadata["safety_identifier"], "safety_identifier should be captured in metadata")
}

// TestChatCompletionsWebSearchAndPredictionMetadata is a unit test on the
// tracer's request parsing directly (no VCR/network): web_search_options
// requires a search-preview model and prediction requires specific model
// support, so this exercises StartSpan against a marshaled request body
// instead of recording a live call.
func TestChatCompletionsWebSearchAndPredictionMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	tp, exporter := oteltest.Setup(t)
	ct := newChatCompletionsTracer(&middlewareConfig{tracerProvider: tp})

	params := openai.ChatCompletionNewParams{
		Model: "gpt-4o-search-preview",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in San Francisco?"),
		},
		WebSearchOptions: openai.ChatCompletionNewParamsWebSearchOptions{
			UserLocation: openai.ChatCompletionNewParamsWebSearchOptionsUserLocation{
				Approximate: openai.ChatCompletionNewParamsWebSearchOptionsUserLocationApproximate{
					Country: openai.String("US"),
					City:    openai.String("San Francisco"),
				},
			},
		},
		Prediction: openai.ChatCompletionPredictionContentParam{
			Content: openai.ChatCompletionPredictionContentContentUnionParam{
				OfString: openai.String("predicted text"),
			},
		},
	}
	body, err := json.Marshal(params)
	require.NoError(err)

	_, span, err := ct.StartSpan(context.Background(), time.Now(), bytes.NewReader(body))
	require.NoError(err)
	span.End()

	ts := exporter.FlushOne()
	metadata := ts.Metadata()

	webSearchOptions, ok := metadata["web_search_options"].(map[string]any)
	require.True(ok, "web_search_options should be captured in metadata")
	userLocation, ok := webSearchOptions["user_location"].(map[string]any)
	require.True(ok)
	approximate, ok := userLocation["approximate"].(map[string]any)
	require.True(ok)
	assert.Equal("US", approximate["country"])
	assert.Equal("San Francisco", approximate["city"])

	prediction, ok := metadata["prediction"].(map[string]any)
	require.True(ok, "prediction should be captured in metadata")
	assert.Equal("predicted text", prediction["content"])
}

// TestOpenAIChatCompletionsStreamingWebSearchAnnotations verifies that URL
// citation annotations, which OpenAI sends whole in a single delta chunk
// rather than incrementally like content, survive streaming aggregation.
func TestOpenAIChatCompletionsStreamingWebSearchAnnotations(t *testing.T) {
	client, _, exporter := setUpTest(t)
	require := require.New(t)

	params := openai.ChatCompletionNewParams{
		Model: "gpt-5-search-api",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is a notable news headline from today? Include a source URL."),
		},
		WebSearchOptions: openai.ChatCompletionNewParamsWebSearchOptions{},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	stream := client.Chat.Completions.NewStreaming(context.Background(), params)
	for stream.Next() {
	}
	require.NoError(stream.Err())

	ts := exporter.FlushOne()
	output, ok := ts.Output().([]any)
	require.True(ok)
	require.NotEmpty(output)

	choice, ok := output[0].(map[string]any)
	require.True(ok)
	message, ok := choice["message"].(map[string]any)
	require.True(ok)

	annotations, ok := message["annotations"].([]any)
	require.True(ok, "annotations should survive streaming aggregation")
	require.NotEmpty(annotations)

	annotation, ok := annotations[0].(map[string]any)
	require.True(ok)
	require.Equal("url_citation", annotation["type"])
	urlCitation, ok := annotation["url_citation"].(map[string]any)
	require.True(ok)
	require.NotEmpty(urlCitation["url"])
}
