package langchaingo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestHandleLLMLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prompts []string
		choice  llms.ContentChoice
		metrics map[string]float64
	}{
		{
			name: "nested usage",
			choice: llms.ContentChoice{Content: "Hi there!", StopReason: "stop", GenerationInfo: map[string]any{
				"usage": map[string]any{"prompt_tokens": float64(10), "completion_tokens": float64(5), "total_tokens": float64(15)},
			}},
			metrics: map[string]float64{"prompt_tokens": 10, "completion_tokens": 5, "tokens": 15},
		},
		{
			name: "PascalCase tokens",
			choice: llms.ContentChoice{Content: "Hi there!", StopReason: "stop", GenerationInfo: map[string]any{
				"PromptTokens": 10, "CompletionTokens": 5, "TotalTokens": 15,
			}},
			metrics: map[string]float64{"prompt_tokens": 10, "completion_tokens": 5, "tokens": 15},
		},
		{
			// LangChainGo does not expose Anthropic usage in callbacks yet, so
			// this case exercises the supported token shape directly.
			name: "Anthropic tokens",
			choice: llms.ContentChoice{Content: "Hi there!", StopReason: "end_turn", GenerationInfo: map[string]any{
				"InputTokens": 17, "OutputTokens": 4,
			}},
			metrics: map[string]float64{"prompt_tokens": 17, "completion_tokens": 4, "tokens": 21},
		},
		{
			name:    "string prompts",
			prompts: []string{"What is the capital of France?", "Tell me a joke"},
			choice:  llms.ContentChoice{Content: "Hi there!", StopReason: "stop"},
		},
		{
			name: "tool calls",
			choice: llms.ContentChoice{StopReason: "tool_calls", ToolCalls: []llms.ToolCall{{
				ID: "call_123", Type: "function",
				FunctionCall: &llms.FunctionCall{Name: "get_weather", Arguments: `{"location":"Boston"}`},
			}}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
			ctx, parent := tp.Tracer(t.Name()).Start(context.Background(), "test-parent")
			defer parent.End()

			name := "langchain.llm.generate_content"
			var input any = []any{map[string]any{"role": "user", "content": "Hello!"}}
			if tt.prompts != nil {
				handler.HandleLLMStart(ctx, tt.prompts)
				name = "langchain.llm.call"
				input = tt.prompts
			} else {
				handler.HandleLLMGenerateContentStart(ctx, []llms.MessageContent{
					llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
				})
			}
			handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{Choices: []*llms.ContentChoice{&tt.choice}})

			// The ambient parent remains active, so only the completed LLM is exported.
			span := exporter.FlushOne()
			span.AssertNameIs(name)
			assert.Equal(t, parent.SpanContext().SpanID(), span.Stub.Parent.SpanID())
			inputJSON, err := json.Marshal(input)
			require.NoError(t, err)
			assert.JSONEq(t, string(inputJSON), span.Attr("braintrust.input_json").String())
			message := map[string]any{"role": "assistant", "content": tt.choice.Content}
			if len(tt.choice.ToolCalls) > 0 {
				// Use the SDK's serialization: its tool-call envelope differs
				// between the workspace and standalone module versions.
				message["tool_calls"] = tt.choice.ToolCalls
			}
			output, err := json.Marshal([]any{map[string]any{
				"index": float64(0), "message": message,
				"stop_reason": tt.choice.StopReason, "finish_reason": tt.choice.StopReason,
			}})
			require.NoError(t, err)
			assert.JSONEq(t, string(output), span.Attr("braintrust.output_json").String())
			if tt.metrics != nil {
				assert.Equal(t, tt.metrics, span.Metrics())
			}
			assert.Empty(t, handler.spans)
		})
	}
}

func TestHandleLLMError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
	ctx := context.Background()
	handler.HandleLLMGenerateContentStart(ctx, []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "Hello!")})
	handler.HandleLLMError(ctx, errors.New("test-error"))

	span := exporter.FlushOne()
	span.AssertNameIs("langchain.llm.generate_content")
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Contains(t, span.Status().Description, "test-error")
	events := span.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)
	assert.Empty(t, handler.spans)
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
