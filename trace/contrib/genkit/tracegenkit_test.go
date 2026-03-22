package genkit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// mockModelFunc creates a ModelFunc that returns the given response.
func mockModelFunc(resp *ai.ModelResponse, err error) ai.ModelFunc {
	return func(_ context.Context, _ *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		return resp, err
	}
}

func requireSpanNamed(t *testing.T, spans []oteltest.Span, name string) oteltest.Span {
	t.Helper()

	var matches []oteltest.Span
	for _, span := range spans {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}

	require.Len(t, matches, 1)
	return matches[0]
}

func TestUnitMiddleware(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	req := &ai.ModelRequest{
		Messages: []*ai.Message{
			ai.NewUserTextMessage("What is 2+2?"),
		},
		Config: &ai.GenerationCommonConfig{
			Temperature:     0.7,
			MaxOutputTokens: 100,
			TopP:            0.9,
			TopK:            40,
		},
	}

	resp := &ai.ModelResponse{
		Message:      ai.NewModelTextMessage("4"),
		FinishReason: ai.FinishReasonStop,
		Usage: &ai.GenerationUsage{
			InputTokens:  10,
			OutputTokens: 1,
			TotalTokens:  11,
		},
	}

	wrapped := mw(mockModelFunc(resp, nil))
	result, err := wrapped(context.Background(), req, nil)
	require.NoError(t, err)
	assert.Equal(t, "4", result.Text())

	span := exporter.FlushOne()

	span.AssertNameIs("genkit.generate")
	assert.Equal(t, oteltrace.SpanKindClient, span.Stub.SpanKind)
	assert.True(t, span.HasAttr("braintrust.span_attributes"))
	assert.True(t, span.HasAttr("braintrust.input_json"))
	assert.True(t, span.HasAttr("braintrust.output_json"))
	assert.True(t, span.HasAttr("braintrust.metrics"))
	assert.True(t, span.HasAttr("braintrust.metadata"))

	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	metrics := span.Metrics()
	assert.Equal(t, float64(10), metrics["prompt_tokens"])
	assert.Equal(t, float64(1), metrics["completion_tokens"])
	assert.Equal(t, float64(11), metrics["tokens"])

	metadata := span.Metadata()
	assert.NotContains(t, metadata, "provider")
	assert.Equal(t, 0.7, metadata["temperature"])
	assert.Equal(t, float64(100), metadata["max_output_tokens"])
	assert.Equal(t, 0.9, metadata["top_p"])
	assert.Equal(t, float64(40), metadata["top_k"])

	input := span.Input()
	inputMap, ok := input.(map[string]any)
	require.True(t, ok)
	messages, ok := inputMap["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 1)

	output := span.Output()
	outputMap, ok := output.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, outputMap, "message")
	assert.Equal(t, "stop", outputMap["finishReason"])
}

func TestMetadataExtraction(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	req := &ai.ModelRequest{
		Config: &openaigo.ChatCompletionNewParams{
			Model:               "gpt-4o-mini",
			Temperature:         openaigo.Float(0.2),
			TopP:                openaigo.Float(0.8),
			MaxCompletionTokens: openaigo.Int(32),
		},
		Messages: []*ai.Message{
			ai.NewSystemTextMessage("You are concise."),
			ai.NewUserTextMessage("Return JSON"),
		},
		Output: &ai.ModelOutputConfig{
			Format:      "json",
			ContentType: "application/json",
			Constrained: true,
			Schema: map[string]any{
				"type": "object",
			},
		},
		ToolChoice: ai.ToolChoiceRequired,
		Tools: []*ai.ToolDefinition{
			{
				Name:        "lookup_weather",
				Description: "Looks up weather",
				InputSchema: map[string]any{
					"type": "object",
				},
			},
		},
	}

	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage(`{"city":"Paris"}`),
		Custom: map[string]any{
			"model":             "gpt-4o-mini",
			"id":                "resp_123",
			"systemFingerprint": "fp_123",
		},
	}

	wrapped := mw(mockModelFunc(resp, nil))
	_, err := wrapped(context.Background(), req, nil)
	require.NoError(t, err)

	span := exporter.FlushOne()
	metadata := span.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "gpt-4o-mini", metadata["model"])
	assert.Equal(t, "You are concise.", metadata["system"])
	assert.Equal(t, "json", metadata["response_format"])
	assert.Equal(t, "application/json", metadata["content_type"])
	assert.Equal(t, true, metadata["output_constrained"])
	assert.Equal(t, "required", metadata["tool_choice"])
	assert.Equal(t, 0.2, metadata["temperature"])
	assert.Equal(t, 0.8, metadata["top_p"])
	assert.Equal(t, float64(32), metadata["max_output_tokens"])
	assert.Equal(t, "resp_123", metadata["id"])
	assert.Equal(t, "fp_123", metadata["system_fingerprint"])

	input := span.Input().(map[string]any)
	assert.Contains(t, input, "config")
	assert.Contains(t, input, "tools")
	assert.Contains(t, input, "output")
}

func TestErrorHandling(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))
	modelErr := errors.New("model failed")

	wrapped := mw(mockModelFunc(nil, modelErr))
	_, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("hello")},
	}, nil)
	require.Error(t, err)
	assert.Equal(t, modelErr, err)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.generate")
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "model failed", span.Status().Description)
	assert.True(t, span.HasAttr("braintrust.metadata"))
}

func TestStreaming(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	resp := &ai.ModelResponse{
		Message:      ai.NewModelTextMessage("Hello world"),
		FinishReason: ai.FinishReasonStop,
		Usage: &ai.GenerationUsage{
			InputTokens:  5,
			OutputTokens: 2,
			TotalTokens:  7,
		},
	}

	streamingModel := func(_ context.Context, _ *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if cb != nil {
			_ = cb(context.Background(), &ai.ModelResponseChunk{
				Content: []*ai.Part{ai.NewTextPart("Hello ")},
			})
			_ = cb(context.Background(), &ai.ModelResponseChunk{
				Content: []*ai.Part{ai.NewTextPart("world")},
			})
		}
		return resp, nil
	}

	var chunks []string
	streamCB := func(_ context.Context, chunk *ai.ModelResponseChunk) error {
		chunks = append(chunks, chunk.Text())
		return nil
	}

	wrapped := mw(streamingModel)
	result, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("hi")},
	}, streamCB)
	require.NoError(t, err)
	assert.Equal(t, "Hello world", result.Text())
	assert.Equal(t, []string{"Hello ", "world"}, chunks)

	span := exporter.FlushOne()
	metrics := span.Metrics()
	assert.Greater(t, metrics["time_to_first_token"], float64(0))
	assert.Equal(t, float64(5), metrics["prompt_tokens"])
}

func TestToolUse(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	req := &ai.ModelRequest{
		Messages: []*ai.Message{
			ai.NewUserTextMessage("What's the weather?"),
		},
		Tools: []*ai.ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather for a location",
			},
		},
	}

	resp := &ai.ModelResponse{
		Message: &ai.Message{
			Role: ai.RoleModel,
			Content: []*ai.Part{
				ai.NewToolRequestPart(&ai.ToolRequest{
					Name:  "get_weather",
					Input: map[string]any{"location": "Tokyo"},
				}),
			},
		},
		FinishReason: ai.FinishReasonStop,
		Usage: &ai.GenerationUsage{
			InputTokens:  20,
			OutputTokens: 10,
			TotalTokens:  30,
		},
	}

	wrapped := mw(mockModelFunc(resp, nil))
	result, err := wrapped(context.Background(), req, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	span := exporter.FlushOne()
	input := span.Input()
	inputMap := input.(map[string]any)
	tools, ok := inputMap["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 1)

	output := span.Output()
	outputMap := output.(map[string]any)
	assert.Contains(t, outputMap, "message")

	metadata := span.Metadata()
	assert.Equal(t, "auto", metadata["tool_choice"])
}

func TestExtractMetrics(t *testing.T) {
	tests := []struct {
		name     string
		usage    *ai.GenerationUsage
		expected map[string]float64
	}{
		{
			name: "all fields",
			usage: &ai.GenerationUsage{
				InputTokens:         10,
				OutputTokens:        20,
				TotalTokens:         30,
				CachedContentTokens: 5,
				ThoughtsTokens:      8,
				Custom: map[string]float64{
					"acceptedPredictionTokens": 4,
				},
			},
			expected: map[string]float64{
				"accepted_prediction_tokens":  4,
				"prompt_tokens":               10,
				"completion_tokens":           20,
				"tokens":                      30,
				"prompt_cached_tokens":        5,
				"completion_reasoning_tokens": 8,
			},
		},
		{
			name: "partial fields",
			usage: &ai.GenerationUsage{
				InputTokens:  10,
				OutputTokens: 20,
			},
			expected: map[string]float64{
				"prompt_tokens":     10,
				"completion_tokens": 20,
				"tokens":            30,
			},
		},
		{
			name:     "nil usage",
			usage:    nil,
			expected: map[string]float64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &ai.ModelResponse{Usage: tt.usage}
			metrics := extractMetrics(resp, 0)
			assert.Equal(t, tt.expected, metrics)
		})
	}
}

func TestCachedTokens(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	resp := &ai.ModelResponse{
		Message:      ai.NewModelTextMessage("cached response"),
		FinishReason: ai.FinishReasonStop,
		Usage: &ai.GenerationUsage{
			InputTokens:         100,
			OutputTokens:        50,
			TotalTokens:         150,
			CachedContentTokens: 80,
		},
	}

	wrapped := mw(mockModelFunc(resp, nil))
	_, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("test")},
	}, nil)
	require.NoError(t, err)

	span := exporter.FlushOne()
	metrics := span.Metrics()
	assert.Equal(t, float64(80), metrics["prompt_cached_tokens"])
}

func TestNilResponse(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	wrapped := mw(mockModelFunc(nil, nil))
	result, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("test")},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, result)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.generate")
	assert.True(t, span.HasAttr("braintrust.input_json"))
	assert.True(t, span.HasAttr("braintrust.metadata"))
	assert.False(t, span.HasAttr("braintrust.output_json"))
}

func TestCleanupJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected any
	}{
		{
			name: "removes empty values",
			input: map[string]any{
				"name":      "test",
				"empty_str": "",
				"nil_value": nil,
				"empty_map": map[string]any{},
				"empty_arr": []any{},
			},
			expected: map[string]any{
				"name": "test",
			},
		},
		{
			name: "keeps non-empty values",
			input: map[string]any{
				"name":  "test",
				"count": float64(5),
				"flag":  false,
			},
			expected: map[string]any{
				"name":  "test",
				"count": float64(5),
				"flag":  false,
			},
		},
		{
			name: "nested cleanup",
			input: map[string]any{
				"outer": map[string]any{
					"inner": "value",
					"empty": "",
				},
			},
			expected: map[string]any{
				"outer": map[string]any{
					"inner": "value",
				},
			},
		},
		{
			name: "drops empty list values",
			input: map[string]any{
				"values": []any{"kept", ""},
			},
			expected: map[string]any{
				"values": []any{"kept"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanupJSON(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestReasoningTokens(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	resp := &ai.ModelResponse{
		Message:      ai.NewModelTextMessage("deep thought"),
		FinishReason: ai.FinishReasonStop,
		Usage: &ai.GenerationUsage{
			InputTokens:    50,
			OutputTokens:   30,
			TotalTokens:    80,
			ThoughtsTokens: 20,
		},
	}

	wrapped := mw(mockModelFunc(resp, nil))
	_, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("think hard")},
	}, nil)
	require.NoError(t, err)

	span := exporter.FlushOne()
	metrics := span.Metrics()
	assert.Equal(t, float64(20), metrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(50), metrics["prompt_tokens"])
	assert.Equal(t, float64(30), metrics["completion_tokens"])
}

func TestStreamingCallbackError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	cbErr := errors.New("callback failed")

	streamingModel := func(_ context.Context, _ *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if cb != nil {
			if err := cb(context.Background(), &ai.ModelResponseChunk{
				Content: []*ai.Part{ai.NewTextPart("chunk")},
			}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	failingCB := func(_ context.Context, _ *ai.ModelResponseChunk) error {
		return cbErr
	}

	wrapped := mw(streamingModel)
	_, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("test")},
	}, failingCB)

	require.Error(t, err)
	assert.Equal(t, cbErr, err)

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Status().Code)
}

func TestDuplicateSpanGuard(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	req := &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("nested")},
	}
	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("done"),
	}

	inner := mw(mockModelFunc(resp, nil))
	outer := mw(func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		return inner(ctx, req, cb)
	})

	result, err := outer(context.Background(), req, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	spans[0].AssertNameIs("genkit.generate")
}

func TestSequentialToolTurnsAreTraced(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	mw := NewMiddleware(WithTracerProvider(tp))

	var nextCtx context.Context
	callCount := 0
	wrapped := mw(func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		callCount++
		nextCtx = ctx
		return &ai.ModelResponse{
			Message: ai.NewModelTextMessage("turn complete"),
			Usage: &ai.GenerationUsage{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		}, nil
	})

	_, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("first turn")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, nextCtx)

	_, err = wrapped(nextCtx, &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("second turn")},
	}, nil)
	require.NoError(t, err)

	spans := exporter.Flush()
	require.Len(t, spans, 2)
	assert.Equal(t, 2, callCount)
}

func TestGeneratePropagatesDefaultModelMetadata(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx, genkit.WithDefaultModel("googleai/gemini-2.0-flash"))
	genkit.DefineModel(g, "googleai/gemini-2.0-flash", nil,
		func(_ context.Context, _ *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return &ai.ModelResponse{
				Message: ai.NewModelTextMessage("hi"),
			}, nil
		},
	)

	resp, err := Generate(ctx, g,
		ai.WithPrompt("hello"),
		ai.WithConfig(&struct {
			Temperature float64 `json:"temperature"`
		}{
			Temperature: 0.3,
		}),
		ai.WithMiddleware(func(next ai.ModelFunc) ai.ModelFunc {
			return func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return next(ctx, req, cb)
			}
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hi", resp.Text())

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	metadata := span.Metadata()
	assert.Equal(t, "gemini", metadata["provider"])
	assert.Equal(t, "gemini-2.0-flash", metadata["model"])
	assert.Equal(t, 0.3, metadata["temperature"])
}

func TestGeneratePropagatesWithModelNameMetadata(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx)
	genkit.DefineModel(g, "anthropic/claude-3-5-sonnet", nil,
		func(_ context.Context, _ *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return &ai.ModelResponse{
				Message: ai.NewModelTextMessage("ok"),
			}, nil
		},
	)

	resp, err := Generate(ctx, g,
		ai.WithPrompt("hello"),
		ai.WithModelName("anthropic/claude-3-5-sonnet"),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "ok", resp.Text())

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	metadata := span.Metadata()
	assert.Equal(t, "anthropic", metadata["provider"])
	assert.Equal(t, "claude-3-5-sonnet", metadata["model"])
}

func TestMiddlewareIntegration(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	mode := vcr.GetVCRMode()
	if mode != vcr.ModeReplay {
		server := startFixedOpenAIServer(t)
		t.Cleanup(func() {
			_ = server.Close()
		})
	}

	ctx := context.Background()
	httpClient := vcr.NewHTTPClient(t)
	plugin := &compatopenai.OpenAI{
		APIKey: "dummy-openai-key",
		Opts: []option.RequestOption{
			option.WithBaseURL("http://127.0.0.1:38087/v1"),
			option.WithHTTPClient(httpClient),
		},
	}

	g := genkit.Init(ctx,
		genkit.WithPlugins(plugin),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	resp, err := Generate(ctx, g,
		ai.WithPrompt("What is the capital of France?"),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			Model:               "gpt-4o-mini",
			Temperature:         openaigo.Float(0.2),
			MaxCompletionTokens: openaigo.Int(32),
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "Paris", resp.Text())

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	span.AssertNameIs("genkit.generate")

	input := span.Input().(map[string]any)
	messages := input["messages"].([]any)
	firstMessage := messages[0].(map[string]any)
	content := firstMessage["content"].([]any)
	assert.Equal(t, "What is the capital of France?", content[0].(map[string]any)["text"])

	output := span.Output().(map[string]any)
	message := output["message"].(map[string]any)
	outContent := message["content"].([]any)
	assert.Equal(t, "Paris", outContent[0].(map[string]any)["text"])

	metadata := span.Metadata()
	assert.Equal(t, "openai", metadata["provider"])
	assert.Equal(t, "gpt-4o-mini", metadata["model"])
	assert.Equal(t, 0.2, metadata["temperature"])
	assert.Equal(t, float64(32), metadata["max_output_tokens"])

	metrics := span.Metrics()
	assert.Equal(t, 11.0, metrics["prompt_tokens"])
	assert.Equal(t, 3.0, metrics["completion_tokens"])
	assert.Equal(t, 14.0, metrics["tokens"])
}

func startFixedOpenAIServer(t *testing.T) *http.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:38087")
	require.NoError(t, err)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/chat/completions", r.URL.Path)
			assert.Equal(t, "POST", r.Method)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "chatcmpl_test_123",
  "object": "chat.completion",
  "created": 1742428800,
  "model": "gpt-4o-mini",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Paris"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 11,
    "completion_tokens": 3,
    "total_tokens": 14
  }
}`))
		}),
	}

	go func() {
		_ = server.Serve(listener)
	}()

	return server
}
