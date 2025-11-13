package langchaingo

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestHandleLLMGenerateContentStart(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Call HandleLLMGenerateContentStart
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextContent{Text: "Hello!"},
			},
		},
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End the LLM span with a dummy response
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "dummy"}},
	})

	// End the parent span to flush traces
	parentSpan.End()

	// Check that a span was created
	spans := exporter.Flush()
	require.Len(t, spans, 2) // parent + LLM span

	// Find the LLM span (not the parent)
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify span name
	llmSpan.AssertNameIs("langchain.llm.generate_content")

	// Verify input is captured
	input := llmSpan.Input()
	require.NotNil(t, input)

	inputSlice, ok := input.([]interface{})
	require.True(t, ok, "input should be a slice")
	require.Len(t, inputSlice, 1)

	message, ok := inputSlice[0].(map[string]interface{})
	require.True(t, ok)
	// Role should be mapped to OpenAI standard ("user" not "human")
	assert.Equal(t, "user", message["role"])
}

func TestHandleLLMGenerateContentEnd(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Start an LLM call
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End the LLM call with a response
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content:    "Hi there!",
				StopReason: "stop",
				GenerationInfo: map[string]any{
					"usage": map[string]any{
						"prompt_tokens":     float64(10),
						"completion_tokens": float64(5),
						"total_tokens":      float64(15),
					},
				},
			},
		},
	}
	handler.HandleLLMGenerateContentEnd(ctx, response)

	// End the parent span to flush traces
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify output is captured
	output := llmSpan.Output()
	require.NotNil(t, output)

	outputSlice, ok := output.([]interface{})
	require.True(t, ok, "output should be a slice")
	require.Len(t, outputSlice, 1)

	choice, ok := outputSlice[0].(map[string]interface{})
	require.True(t, ok)

	// Check OpenAI-style format: message is nested
	message, ok := choice["message"].(map[string]interface{})
	require.True(t, ok, "choice should contain a message object")
	assert.Equal(t, "Hi there!", message["content"])
	assert.Equal(t, "assistant", message["role"])
	assert.Equal(t, "stop", choice["stop_reason"])

	// Verify metrics are captured
	metrics := llmSpan.Metrics()
	assert.Equal(t, float64(10), metrics["prompt_tokens"])
	assert.Equal(t, float64(5), metrics["completion_tokens"])
	assert.Equal(t, float64(15), metrics["tokens"])
}

func TestHandleLLMError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Start an LLM call
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// Simulate an error
	testErr := errors.New("test-error")
	handler.HandleLLMError(ctx, testErr)

	// End the parent span to flush traces
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify error status
	assert.Equal(t, codes.Error, llmSpan.Status().Code)
	assert.Contains(t, llmSpan.Status().Description, "test-error")

	// Verify error event was recorded
	events := llmSpan.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)
}

func TestHandleLLMStart(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Call HandleLLMStart with string prompts
	prompts := []string{"What is the capital of France?", "Tell me a joke"}
	handler.HandleLLMStart(ctx, prompts)

	// Need to end the LLM span manually for this test
	// Use a dummy successful response to end cleanly
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "dummy"}},
	})

	// End the parent span to flush traces
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.call" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify span name
	llmSpan.AssertNameIs("langchain.llm.call")

	// Verify input is captured
	input := llmSpan.Input()
	require.NotNil(t, input)

	inputSlice, ok := input.([]interface{})
	require.True(t, ok, "input should be a slice")
	require.Len(t, inputSlice, 2)
	assert.Equal(t, "What is the capital of France?", inputSlice[0])
	assert.Equal(t, "Tell me a joke", inputSlice[1])
}

func TestHandleLLMGenerateContentEndWithoutStart(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Call End without Start - should not crash
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{Content: "Hi there!"},
		},
	}
	handler.HandleLLMGenerateContentEnd(ctx, response)

	// End the parent span to flush traces
	parentSpan.End()

	// Should only have the parent span
	spans := exporter.Flush()
	assert.Len(t, spans, 1)
	assert.Equal(t, "test-parent", spans[0].Name())
}

func TestHandleLLMErrorWithoutStart(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Call Error without Start - should not crash
	testErr := errors.New("test-error")
	handler.HandleLLMError(ctx, testErr)

	// End the parent span to flush traces
	parentSpan.End()

	// Should only have the parent span (no error recorded on non-existent span)
	spans := exporter.Flush()
	assert.Len(t, spans, 1)
	assert.Equal(t, "test-parent", spans[0].Name())
	assert.NotEqual(t, codes.Error, spans[0].Status().Code)
}

func TestHandleLLMGenerateContentWithToolCalls(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Start an LLM call
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "What's the weather in Boston?"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End with tool calls
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content:    "",
				StopReason: "tool_calls",
				ToolCalls: []llms.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "get_weather",
							Arguments: `{"location":"Boston"}`,
						},
					},
				},
			},
		},
	}
	handler.HandleLLMGenerateContentEnd(ctx, response)

	// End the parent span
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan)

	// Verify tool calls are captured
	output := llmSpan.Output()
	outputSlice, ok := output.([]interface{})
	require.True(t, ok)
	require.Len(t, outputSlice, 1)

	choice, ok := outputSlice[0].(map[string]interface{})
	require.True(t, ok)

	// Check OpenAI-style format: message is nested, tool_calls inside message
	message, ok := choice["message"].(map[string]interface{})
	require.True(t, ok, "choice should contain a message object")

	assert.Contains(t, message, "tool_calls")
	assert.Equal(t, "", message["content"]) // Content is empty when tool calls are present
	assert.Equal(t, "assistant", message["role"])
	assert.Equal(t, "tool_calls", choice["stop_reason"])
}

func TestHandleLLMGenerateContentWithPascalCaseTokens(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Start an LLM call
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End with response containing PascalCase token fields (like LangChainGo OpenAI)
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content:    "Hi there!",
				StopReason: "stop",
				GenerationInfo: map[string]any{
					// PascalCase fields (LangChainGo OpenAI format)
					"CompletionTokens": 5,
					"PromptTokens":     10,
					"TotalTokens":      15,
				},
			},
		},
	}
	handler.HandleLLMGenerateContentEnd(ctx, response)

	// End the parent span to flush traces
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify metrics are extracted from PascalCase fields
	metrics := llmSpan.Metrics()
	assert.Equal(t, float64(10), metrics["prompt_tokens"], "Should extract PromptTokens")
	assert.Equal(t, float64(5), metrics["completion_tokens"], "Should extract CompletionTokens")
	assert.Equal(t, float64(15), metrics["tokens"], "Should extract TotalTokens")
}

func TestHandleLLMGenerateContentWithAnthropicTokens(t *testing.T) {
	// NOTE: This test uses hacked data because Anthropic token data in GenerationInfo
	// is not supported in the official langchaingo client yet. Once Anthropic callback
	// support is merged upstream, this test can be replaced with real data from actual
	// Anthropic responses.
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx, parentSpan := tracer.Start(context.Background(), "test-parent")

	// Start an LLM call
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End with response containing Anthropic's InputTokens/OutputTokens format
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content:    "Hi there!",
				StopReason: "end_turn",
				GenerationInfo: map[string]any{
					// Anthropic format (InputTokens/OutputTokens)
					"InputTokens":  17,
					"OutputTokens": 4,
				},
			},
		},
	}
	handler.HandleLLMGenerateContentEnd(ctx, response)

	// End the parent span to flush traces
	parentSpan.End()

	// Check spans
	spans := exporter.Flush()
	require.Len(t, spans, 2)

	// Find the LLM span
	var llmSpan oteltest.Span
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			llmSpan = span
			break
		}
	}
	require.NotNil(t, llmSpan, "LLM span not found")

	// Verify metrics are extracted from Anthropic's InputTokens/OutputTokens fields
	metrics := llmSpan.Metrics()
	assert.Equal(t, float64(17), metrics["prompt_tokens"], "Should extract InputTokens as prompt_tokens")
	assert.Equal(t, float64(4), metrics["completion_tokens"], "Should extract OutputTokens as completion_tokens")
	assert.Equal(t, float64(21), metrics["tokens"], "Should calculate total tokens")
}
