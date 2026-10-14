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

func TestHandleLLMGenerateContentLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name                                        string
		generationInfo                              map[string]any
		stopReason                                  string
		promptTokens, completionTokens, totalTokens float64
	}{
		{
			name: "nested usage",
			generationInfo: map[string]any{"usage": map[string]any{
				"prompt_tokens": float64(10), "completion_tokens": float64(5), "total_tokens": float64(15),
			}},
			stopReason: "stop", promptTokens: 10, completionTokens: 5, totalTokens: 15,
		},
		{
			name:           "PascalCase tokens",
			generationInfo: map[string]any{"PromptTokens": 10, "CompletionTokens": 5, "TotalTokens": 15},
			stopReason:     "stop", promptTokens: 10, completionTokens: 5, totalTokens: 15,
		},
		{
			// LangChainGo does not expose Anthropic usage in callbacks yet, so
			// this case exercises the supported token shape directly.
			name:           "Anthropic tokens",
			generationInfo: map[string]any{"InputTokens": 17, "OutputTokens": 4},
			stopReason:     "end_turn", promptTokens: 17, completionTokens: 4, totalTokens: 21,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
			ctx, parentSpan := tp.Tracer(t.Name()).Start(context.Background(), "test-parent")
			handler.HandleLLMGenerateContentStart(ctx, []llms.MessageContent{
				llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
			})
			handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{
					Content: "Hi there!", StopReason: tt.stopReason, GenerationInfo: tt.generationInfo,
				}},
			})
			parentSpan.End()

			spans := exporter.Flush()
			require.Len(t, spans, 2)
			var llmSpan oteltest.Span
			for _, span := range spans {
				if span.Name() == "langchain.llm.generate_content" {
					llmSpan = span
					break
				}
			}
			require.NotNil(t, llmSpan, "LLM span not found")
			llmSpan.AssertNameIs("langchain.llm.generate_content")
			assert.Equal(t, parentSpan.SpanContext().SpanID(), llmSpan.Stub.Parent.SpanID())
			input, ok := llmSpan.Input().([]any)
			require.True(t, ok)
			require.Len(t, input, 1)
			message, ok := input[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "user", message["role"])
			assert.Equal(t, "Hello!", message["content"])

			output, ok := llmSpan.Output().([]any)
			require.True(t, ok)
			require.Len(t, output, 1)
			choice, ok := output[0].(map[string]any)
			require.True(t, ok)
			message, ok = choice["message"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "Hi there!", message["content"])
			assert.Equal(t, "assistant", message["role"])
			assert.Equal(t, tt.stopReason, choice["stop_reason"])
			metrics := llmSpan.Metrics()
			assert.Equal(t, tt.promptTokens, metrics["prompt_tokens"])
			assert.Equal(t, tt.completionTokens, metrics["completion_tokens"])
			assert.Equal(t, tt.totalTokens, metrics["tokens"])
		})
	}
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

func TestHandleLLMCompletionWithoutStart(t *testing.T) {
	for _, tt := range []struct {
		name     string
		complete func(*Handler, context.Context)
	}{
		{"end", func(h *Handler, ctx context.Context) {
			h.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "Hi there!"}}})
		}},
		{"error", func(h *Handler, ctx context.Context) {
			h.HandleLLMError(ctx, errors.New("test-error"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
			ctx, parentSpan := tp.Tracer(t.Name()).Start(context.Background(), "test-parent")
			tt.complete(handler, ctx)
			parentSpan.End()

			span := exporter.FlushOne()
			span.AssertNameIs("test-parent")
			assert.NotEqual(t, codes.Error, span.Status().Code)
			assert.Empty(t, span.Events(), "completion must not attach events to the parent")
		})
	}
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
