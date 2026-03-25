package eino

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// runInfo returns a RunInfo for testing.
func makeRunInfo(name, typ string) *callbacks.RunInfo {
	return &callbacks.RunInfo{
		Name: name,
		Type: typ,
	}
}

// invokeChatModel simulates what the eino framework does for a non-streaming Generate call.
// The framework calls OnStart, the model generates a response, then OnEnd is called
// with the context returned by OnStart.
func invokeChatModel(ctx context.Context, h *Handler, info *callbacks.RunInfo, input *model.CallbackInput, output *model.CallbackOutput) {
	ctx = h.OnStart(ctx, info, input)
	h.OnEnd(ctx, info, output)
}

func TestOnStart(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("my-llm", "OpenAI")

	input := &model.CallbackInput{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "Hello!"},
		},
		Config: &model.Config{
			Model: "gpt-4o",
		},
	}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "Hi!"},
	})

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	span := spans[0]
	span.AssertNameIs("eino.my-llm")

	inp := span.Input()
	require.NotNil(t, inp)
	msgs, ok := inp.([]interface{})
	require.True(t, ok)
	require.Len(t, msgs, 1)
	msg, ok2 := msgs[0].(map[string]interface{})
	require.True(t, ok2)
	assert.Equal(t, "user", msg["role"])
	assert.Equal(t, "Hello!", msg["content"])
}

func TestOnEnd_WithTokenUsage(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "Anthropic")

	input := &model.CallbackInput{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "Say hi"},
		},
	}
	output := &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "Hello there!"},
		TokenUsage: &model.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	invokeChatModel(ctx, handler, info, input, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	// span name falls back to type
	span.AssertNameIs("eino.Anthropic")

	out := span.Output()
	require.NotNil(t, out)
	choice, ok := out.(map[string]interface{})
	require.True(t, ok, "output should be a message object")
	assert.Equal(t, "assistant", choice["role"])
	assert.Equal(t, "Hello there!", choice["content"])

	metrics := span.Metrics()
	require.NotNil(t, metrics)
	assert.Equal(t, float64(10), metrics["prompt_tokens"])
	assert.Equal(t, float64(5), metrics["completion_tokens"])
	assert.Equal(t, float64(15), metrics["tokens"])
}

func TestOnError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "fail"},
		},
	}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnError(ctx2, info, errors.New("rate limit exceeded"))

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
}

func TestOnEndWithStreamOutput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("stream-model", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "Count to 3"},
		},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	// Build a stream of message chunks
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "1"},
		{Role: schema.Assistant, Content: " 2"},
		{Role: schema.Assistant, Content: " 3", ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     8,
				CompletionTokens: 6,
				TotalTokens:      14,
			},
		}},
	}

	sr, sw := schema.Pipe[callbacks.CallbackOutput](len(chunks))
	for _, chunk := range chunks {
		sw.Send(&model.CallbackOutput{Message: chunk}, nil)
	}
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait() // wait for internal streaming goroutine to end the span

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]
	span.AssertNameIs("eino.stream-model")

	out := span.Output()
	require.NotNil(t, out)
	choice, ok := out.(map[string]interface{})
	require.True(t, ok, "output should be a message object")
	assert.Equal(t, "1 2 3", choice["content"])

	metrics := span.Metrics()
	require.NotNil(t, metrics)
	assert.Equal(t, float64(8), metrics["prompt_tokens"])
	assert.Equal(t, float64(6), metrics["completion_tokens"])
	assert.Equal(t, float64(14), metrics["tokens"])
}

func TestOnEndWithStreamOutput_EarlyClose(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	sr, sw := schema.Pipe[callbacks.CallbackOutput](10)
	sw.Send(&model.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: "chunk1"}}, nil)
	// close stream early before all data is sent
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait()

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	// span was ended (no panic or leak)
	_ = spans[0]
}

func TestOnStartWithStreamInput_ModelInput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("stream-llm", "OpenAI")

	sr, sw := schema.Pipe[callbacks.CallbackInput](2)
	sw.Send(
		&model.CallbackInput{
			Messages: []*schema.Message{{Role: schema.User, Content: "streamed input"}},
			Config:   &model.Config{Model: "gpt-4o"},
		},
		nil,
	)
	sw.Close()

	// OnStartWithStreamInput should create a span from the collected input.
	ctx2 := handler.OnStartWithStreamInput(ctx, info, sr)
	require.NotEqual(t, ctx, ctx2, "context should contain a span")

	// End the span so it gets exported.
	handler.OnEnd(ctx2, info, &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "response"},
	})

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	span.AssertNameIs("eino.stream-llm")

	inp := span.Input()
	require.NotNil(t, inp)

	out := span.Output()
	require.NotNil(t, out)
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "response", outMap["content"])

	meta := span.Metadata()
	require.NotNil(t, meta)
	assert.Equal(t, "gpt-4o", meta["model"])
}

func TestOnStartWithStreamInput_ToolInput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("get_weather", "")

	sr, sw := schema.Pipe[callbacks.CallbackInput](2)
	sw.Send(
		&tool.CallbackInput{ArgumentsInJSON: `{"location":"NYC"}`},
		nil,
	)
	sw.Close()

	ctx2 := handler.OnStartWithStreamInput(ctx, info, sr)
	require.NotEqual(t, ctx, ctx2)

	handler.OnEnd(ctx2, info, &tool.CallbackOutput{Response: "72F"})

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]
	span.AssertNameIs("eino.get_weather")

	out := span.Output()
	require.NotNil(t, out)
	assert.Equal(t, "72F", out)
}

func TestOnStartWithStreamInput_UnrecognizedInput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("retriever", "")

	type customInput struct{ Query string }
	sr, sw := schema.Pipe[callbacks.CallbackInput](2)
	sw.Send(&customInput{Query: "docs"}, nil)
	sw.Close()

	// Unrecognized input should not create a span.
	ctx2 := handler.OnStartWithStreamInput(ctx, info, sr)
	assert.Equal(t, ctx, ctx2)

	spans := exporter.Flush()
	assert.Len(t, spans, 0)
}

func TestNeeded(t *testing.T) {
	handler := NewHandler()
	ctx := context.Background()
	info := makeRunInfo("", "")

	assert.True(t, handler.Needed(ctx, info, callbacks.TimingOnStart))
	assert.True(t, handler.Needed(ctx, info, callbacks.TimingOnEnd))
	assert.True(t, handler.Needed(ctx, info, callbacks.TimingOnError))
	assert.True(t, handler.Needed(ctx, info, callbacks.TimingOnEndWithStreamOutput))
	assert.True(t, handler.Needed(ctx, info, callbacks.TimingOnStartWithStreamInput))
}

func TestMetadata(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "OpenAI")

	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
		Config: &model.Config{
			Model:       "gpt-4o-mini",
			MaxTokens:   100,
			Temperature: 0.7,
		},
	}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "hello"},
	})

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	meta := spans[0].Metadata()
	require.NotNil(t, meta)
	assert.Equal(t, "gpt-4o-mini", meta["model"])
	// provider is derived from info.Type when available
	assert.Equal(t, "OpenAI", meta["provider"])
}

func TestToolCallOutput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "What's the weather?"}},
	}

	idx := 0
	output := &model.CallbackOutput{
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: "",
			ToolCalls: []schema.ToolCall{
				{
					Index: &idx,
					ID:    "call_abc123",
					Type:  "function",
					Function: schema.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"location":"New York"}`,
					},
				},
			},
		},
	}

	invokeChatModel(ctx, handler, info, input, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	inp := span.Input()
	require.NotNil(t, inp)
}

func TestStreamError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	sr, sw := schema.Pipe[callbacks.CallbackOutput](2)
	sw.Send(nil, io.ErrUnexpectedEOF)
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait()

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Stub.Status.Code)
}

func TestToolSpan(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("get_weather", "")

	input := &tool.CallbackInput{ArgumentsInJSON: `{"location":"New York"}`}
	output := &tool.CallbackOutput{Response: "72°F and sunny"}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	span.AssertNameIs("eino.get_weather")

	// span type should be "tool"
	attrs := span.Stub.Attributes
	var spanAttrsStr string
	for _, a := range attrs {
		if string(a.Key) == "braintrust.span_attributes" {
			spanAttrsStr = a.Value.AsString()
		}
	}
	assert.Contains(t, spanAttrsStr, `"type":"tool"`)

	inp := span.Input()
	require.NotNil(t, inp)
	// Verify input is a parsed JSON object, not a double-encoded string.
	inpMap, ok := inp.(map[string]any)
	require.True(t, ok, "expected parsed JSON object, got %T", inp)
	assert.Equal(t, "New York", inpMap["location"])

	out := span.Output()
	require.NotNil(t, out)
	assert.Equal(t, "72°F and sunny", out)
}

func TestToolOutputJSON_NotDoubleEncoded(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("multiply", "")

	input := &tool.CallbackInput{ArgumentsInJSON: `{"a":6,"b":7}`}
	// Tool returns JSON-formatted result
	output := &tool.CallbackOutput{Response: `{"result":42}`}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	out := spans[0].Output()
	require.NotNil(t, out)
	// Output should be a parsed JSON object, not a double-encoded string.
	outMap, ok := out.(map[string]any)
	require.True(t, ok, "expected parsed JSON object, got %T", out)
	assert.Equal(t, float64(42), outMap["result"])
}

func TestToolOutputPlainString(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("greet", "")

	input := &tool.CallbackInput{ArgumentsInJSON: `{}`}
	output := &tool.CallbackOutput{Response: "hello world"}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	out := spans[0].Output()
	require.NotNil(t, out)
	// Plain string output should still be retrievable.
	assert.Equal(t, "hello world", out)
}

func TestUnrecognizedInput_StillIgnored(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("retriever", "VectorStore")

	// An arbitrary struct type — not model, tool, or string CallbackInput
	type customInput struct{ Query string }
	ctx2 := handler.OnStart(ctx, info, &customInput{Query: "docs"})
	handler.OnEnd(ctx2, info, &customInput{Query: "docs"})

	spans := exporter.Flush()
	assert.Len(t, spans, 0)
}

func TestOnEndWithStreamOutput_ConcatError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	// Send chunks with conflicting roles to trigger a ConcatMessages error.
	sr, sw := schema.Pipe[callbacks.CallbackOutput](2)
	sw.Send(&model.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: "hello"}}, nil)
	sw.Send(&model.CallbackOutput{Message: &schema.Message{Role: schema.User, Content: "world"}}, nil)
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait()

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	// If ConcatMessages fails, the error should be recorded on the span
	assert.Equal(t, codes.Error, spans[0].Stub.Status.Code)
}

func TestStreamingCachedTokens(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	sr, sw := schema.Pipe[callbacks.CallbackOutput](1)
	sw.Send(&model.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "cached response",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     20,
				CompletionTokens: 5,
				TotalTokens:      25,
				PromptTokenDetails: schema.PromptTokenDetails{
					CachedTokens: 15,
				},
				CompletionTokensDetails: schema.CompletionTokensDetails{
					ReasoningTokens: 3,
				},
			},
		},
	}}, nil)
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait()

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	metrics := spans[0].Metrics()
	require.NotNil(t, metrics)
	assert.Equal(t, float64(20), metrics["prompt_tokens"])
	assert.Equal(t, float64(5), metrics["completion_tokens"])
	assert.Equal(t, float64(25), metrics["tokens"])
	assert.Equal(t, float64(15), metrics["prompt_cached_tokens"])
	assert.Equal(t, float64(3), metrics["completion_reasoning_tokens"])
}

func TestModelSpanKindClient(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("my-llm", "OpenAI")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "hello"},
	})

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	assert.Equal(t, oteltrace.SpanKindClient, spans[0].Stub.SpanKind,
		"model spans should be SpanKindClient (outbound call to LLM service)")
}

func TestOTelSemanticAttributes(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("my-llm", "OpenAI")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
		Config:   &model.Config{Model: "gpt-4o-mini"},
	}
	output := &model.CallbackOutput{
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: "hello",
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "stop",
			},
		},
		TokenUsage: &model.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	invokeChatModel(ctx, handler, info, input, output)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	// Check gen_ai.* OTel semantic convention attributes
	attrMap := make(map[string]any)
	for _, a := range span.Stub.Attributes {
		attrMap[string(a.Key)] = a.Value.AsInterface()
	}
	assert.Equal(t, "gpt-4o-mini", attrMap["gen_ai.request.model"])
	assert.Equal(t, "stop", attrMap["gen_ai.finish_reason"])
}

func TestToolSpanKindInternal(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("get_weather", "")
	input := &tool.CallbackInput{ArgumentsInJSON: `{}`}

	ctx2 := handler.OnStart(ctx, info, input)
	handler.OnEnd(ctx2, info, &tool.CallbackOutput{Response: "ok"})

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	assert.Equal(t, oteltrace.SpanKindInternal, spans[0].Stub.SpanKind,
		"tool spans should remain SpanKindInternal")
}

func TestStreamingTimeToFirstToken(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	info := makeRunInfo("stream-model", "")
	input := &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}

	ctx2 := handler.OnStart(ctx, info, input)

	sr, sw := schema.Pipe[callbacks.CallbackOutput](2)
	sw.Send(&model.CallbackOutput{Message: &schema.Message{
		Role: schema.Assistant, Content: "hello",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     5,
				CompletionTokens: 3,
				TotalTokens:      8,
			},
		},
	}}, nil)
	sw.Close()

	handler.OnEndWithStreamOutput(ctx2, info, sr)
	handler.Wait()

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	metrics := spans[0].Metrics()
	require.NotNil(t, metrics)
	ttft, ok := metrics["time_to_first_token"]
	require.True(t, ok, "metrics should contain time_to_first_token")
	assert.Greater(t, ttft, float64(0), "time_to_first_token should be positive")
}

func TestDefaultHandler(t *testing.T) {
	h1 := DefaultHandler()
	h2 := DefaultHandler()
	assert.Same(t, h1, h2, "DefaultHandler should return the same instance")
}

func TestSpanNameFallback(t *testing.T) {
	tests := []struct {
		name     string
		info     *callbacks.RunInfo
		expected string
	}{
		{"nil info", nil, "eino"},
		{"empty info", &callbacks.RunInfo{}, "eino"},
		{"name set", makeRunInfo("my-model", ""), "eino.my-model"},
		{"type set", makeRunInfo("", "OpenAI"), "eino.OpenAI"},
		{"name takes precedence", makeRunInfo("my-node", "OpenAI"), "eino.my-node"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, spanNameFromInfo(tt.info))
		})
	}
}
