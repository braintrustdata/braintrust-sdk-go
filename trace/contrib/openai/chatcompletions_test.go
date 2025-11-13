package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openai/openai-go"
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
	assert.Equal("openai.chat.completions.create", ts.Name())

	// Check input field
	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	assert.Contains(string(inputJSON), "What is 2+2?")

	// Check output field
	output := ts.Output()
	assert.NotNil(output)
	// The output should contain choices array
	choices, ok := output.([]interface{})
	require.True(ok, "output should be an array of choices")
	require.Greater(len(choices), 0, "should have at least one choice")

	// Check first choice has message with content
	firstChoice, ok := choices[0].(map[string]interface{})
	require.True(ok, "first choice should be a map")
	message, ok := firstChoice["message"].(map[string]interface{})
	require.True(ok, "choice should have a message")
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
	assert.Equal("openai.chat.completions.create", ts.Name())

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
	assert.Equal("openai.chat.completions.create", ts.Name())

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

	// Role should be set (not nil/null)
	role := message["role"]
	assert.NotNil(role, "role should be set for tool calls")
	if role != nil {
		if roleStr, ok := role.(string); ok {
			assert.Equal("assistant", roleStr)
		}
	}

	// Check metadata contains tools
	metadata := ts.Metadata()
	tools, ok := metadata["tools"].([]interface{})
	require.True(ok, "metadata should contain tools array")
	assert.Len(tools, 1)
}

func TestStreamingToolCallsPostprocessing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	ct := newChatCompletionsTracer(&middlewareConfig{})

	t.Run("EmptyResults", func(t *testing.T) {
		result := ct.postprocessStreamingResults([]map[string]any{})
		require.Len(result, 1)
		choice := result[0]

		message, ok := choice["message"].(map[string]interface{})
		require.True(ok)
		assert.Nil(message["role"])
		assert.Equal("", message["content"])
		assert.Nil(message["tool_calls"])
	})

	t.Run("SingleToolCall", func(t *testing.T) {
		// Simulate a streaming response with a single tool call
		results := []map[string]any{
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role": "assistant",
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []interface{}{
								map[string]any{
									"id":   "call_123",
									"type": "function",
									"function": map[string]any{
										"name":      "get_weather",
										"arguments": `{"location":"`,
									},
								},
							},
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []interface{}{
								map[string]any{
									"function": map[string]any{
										"arguments": `San Francisco"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index":         0,
						"finish_reason": "tool_calls",
						"delta":         map[string]any{},
					},
				},
			},
		}

		result := ct.postprocessStreamingResults(results)
		require.Len(result, 1)

		choice := result[0]
		message, ok := choice["message"].(map[string]interface{})
		require.True(ok)

		assert.Equal("assistant", message["role"])
		assert.Equal("", message["content"])
		assert.Equal("tool_calls", choice["finish_reason"])

		toolCalls, ok := message["tool_calls"].([]interface{})
		require.True(ok, "should have tool_calls")
		require.Len(toolCalls, 1)

		toolCall, ok := toolCalls[0].(map[string]interface{})
		require.True(ok)
		assert.Equal("call_123", toolCall["id"])
		assert.Equal("function", toolCall["type"])

		function, ok := toolCall["function"].(map[string]interface{})
		require.True(ok)
		assert.Equal("get_weather", function["name"])
		assert.Equal(`{"location":"San Francisco"}`, function["arguments"])
	})

	t.Run("MultipleToolCalls", func(t *testing.T) {
		// Simulate a streaming response with multiple tool calls
		results := []map[string]any{
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role": "assistant",
							"tool_calls": []interface{}{
								map[string]any{
									"id":   "call_1",
									"type": "function",
									"function": map[string]any{
										"name":      "get_weather",
										"arguments": `{"location":"SF"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []interface{}{
								map[string]any{
									"id":   "call_2",
									"type": "function",
									"function": map[string]any{
										"name":      "get_time",
										"arguments": `{"timezone":"EST"}`,
									},
								},
							},
						},
					},
				},
			},
		}

		result := ct.postprocessStreamingResults(results)
		require.Len(result, 1)

		choice := result[0]
		message, ok := choice["message"].(map[string]interface{})
		require.True(ok)

		toolCalls, ok := message["tool_calls"].([]interface{})
		require.True(ok, "should have tool_calls")
		require.Len(toolCalls, 2)

		// Check first tool call
		toolCall1, ok := toolCalls[0].(map[string]interface{})
		require.True(ok)
		assert.Equal("call_1", toolCall1["id"])
		function1, ok := toolCall1["function"].(map[string]interface{})
		require.True(ok)
		assert.Equal("get_weather", function1["name"])

		// Check second tool call
		toolCall2, ok := toolCalls[1].(map[string]interface{})
		require.True(ok)
		assert.Equal("call_2", toolCall2["id"])
		function2, ok := toolCall2["function"].(map[string]interface{})
		require.True(ok)
		assert.Equal("get_time", function2["name"])
	})

	t.Run("ContentAndToolCalls", func(t *testing.T) {
		// Test mixed content and tool calls
		results := []map[string]any{
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role":    "assistant",
							"content": "I'll help you with that. ",
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"content": "Let me check the weather.",
						},
					},
				},
			},
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []interface{}{
								map[string]any{
									"id":   "call_weather",
									"type": "function",
									"function": map[string]any{
										"name":      "get_weather",
										"arguments": `{"location":"NYC"}`,
									},
								},
							},
						},
					},
				},
			},
		}

		result := ct.postprocessStreamingResults(results)
		require.Len(result, 1)

		choice := result[0]
		message, ok := choice["message"].(map[string]interface{})
		require.True(ok)

		assert.Equal("assistant", message["role"])
		assert.Equal("I'll help you with that. Let me check the weather.", message["content"])

		toolCalls, ok := message["tool_calls"].([]interface{})
		require.True(ok)
		require.Len(toolCalls, 1)
	})

	t.Run("EmptyToolCallsArray", func(t *testing.T) {
		// Test the edge case that caused the original bug
		results := []map[string]any{
			{
				"choices": []interface{}{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role": "assistant",
							"tool_calls": []interface{}{
								map[string]any{
									"id":   "call_first",
									"type": "function",
									"function": map[string]any{
										"name":      "test_function",
										"arguments": `{"param":"value"}`,
									},
								},
							},
						},
					},
				},
			},
		}

		// This should not panic (the bug we fixed)
		result := ct.postprocessStreamingResults(results)
		require.Len(result, 1)

		choice := result[0]
		message, ok := choice["message"].(map[string]interface{})
		require.True(ok)
		toolCalls, ok := message["tool_calls"].([]interface{})
		require.True(ok)
		require.Len(toolCalls, 1)
	})
}

func TestChatCompletionsStructuredAssertions(t *testing.T) {
	client, _, exporter := setUpTest(t)
	require := require.New(t)

	// Test simple chat completion with structured assertions
	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is 2 + 2?"),
		},
		Model:       testModel,
		Temperature: openai.Float(0.5),
		MaxTokens:   openai.Int(50),
	}

	resp, err := client.Chat.Completions.New(context.Background(), params)
	require.NoError(err)
	require.NotNil(resp)

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	// Convert span to structured format for assertion
	spanData := map[string]interface{}{
		"span_attributes": map[string]interface{}{
			"name": ts.Name(),
			"type": "llm",
		},
		"input":    ts.Input(),
		"output":   ts.Output(),
		"metadata": ts.Metadata(),
		"metrics":  ts.Metrics(),
	}

	// Assert the entire structure at once
	AssertMatchesObject(t, spanData, map[string]interface{}{
		"span_attributes": map[string]interface{}{
			"name": "openai.chat.completions.create",
			"type": "llm",
		},
		"input": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "What is 2 + 2?",
			},
		},
		"output": []interface{}{
			map[string]interface{}{
				"index":         AnyNum(),
				"message":       AnyObj(),
				"finish_reason": AnyStr(),
			},
		},
		"metadata": map[string]interface{}{
			"provider":    "openai",
			"endpoint":    "/v1/chat/completions",
			"model":       StrContains(testModel),
			"temperature": 0.5,
			"max_tokens":  50.0, // JSON numbers are floats
		},
		"metrics": map[string]interface{}{
			"prompt_tokens":     NumGT(0),
			"completion_tokens": NumGT(0),
			"tokens":            NumGT(0),
		},
	})
}

func TestToolCallsStructuredAssertions(t *testing.T) {
	client, _, exporter := setUpTest(t)
	require := require.New(t)

	// Test streaming tool calls with structured assertions
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

	stream := client.Chat.Completions.NewStreaming(context.Background(), params)
	for stream.Next() {
		// Just consume the stream
	}
	require.NoError(stream.Err())

	// Wait for spans to be exported
	ts := exporter.FlushOne()

	// Convert span to structured format for assertion
	spanData := map[string]interface{}{
		"span_attributes": map[string]interface{}{
			"name": ts.Name(),
			"type": "llm",
		},
		"input":    ts.Input(),
		"output":   ts.Output(),
		"metadata": ts.Metadata(),
		"metrics":  ts.Metrics(),
	}

	// Assert the entire tool calls structure at once
	AssertMatchesObject(t, spanData, map[string]interface{}{
		"span_attributes": map[string]interface{}{
			"name": "openai.chat.completions.create",
			"type": "llm",
		},
		"input": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "What's the weather in San Francisco?",
			},
		},
		"output": []interface{}{
			map[string]interface{}{
				"index": 0.0, // JSON numbers are floats
				"message": map[string]interface{}{
					"role":       "assistant",
					"content":    AnyStr(), // Could be empty for tool calls
					"tool_calls": AnyArr(), // Should have tool calls array
				},
				"finish_reason": AnyStr(),
			},
		},
		"metadata": map[string]interface{}{
			"provider": "openai",
			"endpoint": "/v1/chat/completions",
			"model":    StrContains(testModel),
			"tools": []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_weather",
						"description": "Get weather for a location",
						"parameters":  AnyObj(),
					},
				},
			},
		},
		"metrics": map[string]interface{}{
			"prompt_tokens":     NumGT(0),
			"completion_tokens": NumGT(0),
			"tokens":            NumGT(0),
		},
	})
}

// Matcher interface for flexible value matching
type Matcher interface {
	Matches(value interface{}) bool
	String() string
}

// AnyString matches any string value
type AnyString struct{}

func (m AnyString) Matches(value interface{}) bool {
	_, ok := value.(string)
	return ok
}

func (m AnyString) String() string { return "any(string)" }

// AnyNumber matches any numeric value (int, float, etc.)
type AnyNumber struct{}

func (m AnyNumber) Matches(value interface{}) bool {
	switch value.(type) {
	case int, int32, int64, float32, float64:
		return true
	default:
		return false
	}
}

func (m AnyNumber) String() string { return "any(number)" }

// AnyArray matches any array/slice
type AnyArray struct{}

func (m AnyArray) Matches(value interface{}) bool {
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Slice || v.Kind() == reflect.Array
}

func (m AnyArray) String() string { return "any(array)" }

// AnyObject matches any map/object
type AnyObject struct{}

func (m AnyObject) Matches(value interface{}) bool {
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Map
}

func (m AnyObject) String() string { return "any(object)" }

// StringContaining matches strings that contain a substring
type StringContaining struct {
	Substring string
}

func (m StringContaining) Matches(value interface{}) bool {
	if str, ok := value.(string); ok {
		return strings.Contains(str, m.Substring)
	}
	return false
}

func (m StringContaining) String() string { return fmt.Sprintf("string_containing(%q)", m.Substring) }

// NumberGreaterThan matches numbers greater than a threshold
type NumberGreaterThan struct {
	Threshold float64
}

func (m NumberGreaterThan) Matches(value interface{}) bool {
	switch v := value.(type) {
	case int:
		return float64(v) > m.Threshold
	case int32:
		return float64(v) > m.Threshold
	case int64:
		return float64(v) > m.Threshold
	case float32:
		return float64(v) > m.Threshold
	case float64:
		return v > m.Threshold
	default:
		return false
	}
}

func (m NumberGreaterThan) String() string { return fmt.Sprintf("number_gt(%.2f)", m.Threshold) }

// AssertMatchesObject asserts that actual matches the expected structure
// Similar to Python's assert_matches_object and TypeScript's toMatchObject
func AssertMatchesObject(t *testing.T, actual interface{}, expected interface{}) {
	t.Helper()
	if !matchesObject(actual, expected, "") {
		t.Errorf("Object does not match expected structure")
	}
}

func matchesObject(actual, expected interface{}, path string) bool {
	// Handle matchers
	if matcher, ok := expected.(Matcher); ok {
		if !matcher.Matches(actual) {
			fmt.Printf("MISMATCH at %s: expected %s, got %v (%T)\n", path, matcher.String(), actual, actual)
			return false
		}
		return true
	}

	// Handle exact values
	if reflect.DeepEqual(actual, expected) {
		return true
	}

	// Handle maps/objects - support both map[string]interface{} and map[string]float64
	expectedMap, expectedIsMap := expected.(map[string]interface{})

	var actualMap map[string]interface{}
	var actualIsMap bool

	// Handle different map types
	switch v := actual.(type) {
	case map[string]interface{}:
		actualMap = v
		actualIsMap = true
	case map[string]float64:
		// Convert map[string]float64 to map[string]interface{}
		actualMap = make(map[string]interface{})
		for key, val := range v {
			actualMap[key] = val
		}
		actualIsMap = true
	}

	if expectedIsMap && actualIsMap {
		// Partial matching: check that all expected keys exist and match
		// (actual map can have additional keys not specified in expected)
		for key, expectedValue := range expectedMap {
			actualValue, exists := actualMap[key]
			if !exists {
				fmt.Printf("MISMATCH at %s.%s: key missing in actual\n", path, key)
				return false
			}
			keyPath := path + "." + key
			if path == "" {
				keyPath = key
			}
			if !matchesObject(actualValue, expectedValue, keyPath) {
				return false
			}
		}
		return true
	}

	// Handle arrays/slices
	expectedSlice := reflect.ValueOf(expected)
	actualSlice := reflect.ValueOf(actual)

	if expectedSlice.Kind() == reflect.Slice && actualSlice.Kind() == reflect.Slice {
		if expectedSlice.Len() != actualSlice.Len() {
			fmt.Printf("MISMATCH at %s: slice length differs, expected %d, got %d\n", path, expectedSlice.Len(), actualSlice.Len())
			return false
		}

		for i := 0; i < expectedSlice.Len(); i++ {
			indexPath := fmt.Sprintf("%s[%d]", path, i)
			if !matchesObject(actualSlice.Index(i).Interface(), expectedSlice.Index(i).Interface(), indexPath) {
				return false
			}
		}
		return true
	}

	fmt.Printf("MISMATCH at %s: expected %v (%T), got %v (%T)\n", path, expected, expected, actual, actual)
	return false
}

// Convenience matcher constructors
func AnyStr() Matcher              { return AnyString{} }
func AnyNum() Matcher              { return AnyNumber{} }
func AnyArr() Matcher              { return AnyArray{} }
func AnyObj() Matcher              { return AnyObject{} }
func StrContains(s string) Matcher { return StringContaining{s} }
func NumGT(n float64) Matcher      { return NumberGreaterThan{n} }

// assertChatSpanValid asserts all the common properties of a chat completion span are valid.
func assertChatSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange) {
	t.Helper()
	assert := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs("openai.chat.completions.create")
	assert.Equal(codes.Unset, span.Status().Code)

	metadata := span.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/chat/completions", metadata["endpoint"])
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
