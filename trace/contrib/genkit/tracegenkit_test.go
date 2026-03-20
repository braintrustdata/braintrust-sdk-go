package genkit

import (
	"context"
	"errors"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// mockModelFunc creates a ModelFunc that returns the given response.
func mockModelFunc(resp *ai.ModelResponse, err error) ai.ModelFunc {
	return func(_ context.Context, _ *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		return resp, err
	}
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

	// Verify span name and kind
	span.AssertNameIs("genkit.generate")
	assert.Equal(t, oteltrace.SpanKindClient, span.Stub.SpanKind)

	// Verify braintrust attributes
	assert.True(t, span.HasAttr("braintrust.span_attributes"))
	assert.True(t, span.HasAttr("braintrust.input_json"))
	assert.True(t, span.HasAttr("braintrust.output_json"))
	assert.True(t, span.HasAttr("braintrust.metrics"))
	assert.True(t, span.HasAttr("braintrust.metadata"))

	// Verify span_attributes
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	// Verify metrics
	metrics := span.Metrics()
	assert.Equal(t, float64(10), metrics["prompt_tokens"])
	assert.Equal(t, float64(1), metrics["completion_tokens"])
	assert.Equal(t, float64(11), metrics["tokens"])

	// Verify metadata
	metadata := span.Metadata()
	assert.Equal(t, "genkit", metadata["provider"])
	assert.Equal(t, 0.7, metadata["temperature"])
	assert.Equal(t, float64(100), metadata["maxOutputTokens"])
	assert.Equal(t, 0.9, metadata["topP"])
	assert.Equal(t, float64(40), metadata["topK"])

	// Verify input contains messages
	input := span.Input()
	inputMap, ok := input.(map[string]any)
	require.True(t, ok)
	messages, ok := inputMap["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 1)

	// Verify output has message
	output := span.Output()
	outputMap, ok := output.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, outputMap, "message")
	assert.Equal(t, "stop", outputMap["finishReason"])
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
			// Simulate streaming chunks
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

	// Verify input has tools
	input := span.Input()
	inputMap := input.(map[string]any)
	tools, ok := inputMap["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 1)

	// Verify output has tool request
	output := span.Output()
	outputMap := output.(map[string]any)
	assert.Contains(t, outputMap, "message")
}

func TestParseUsageTokens(t *testing.T) {
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
			},
			expected: map[string]float64{
				"prompt_tokens":              10,
				"completion_tokens":          20,
				"tokens":                     30,
				"prompt_cached_tokens":       5,
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

	// A model that returns nil response with no error (unusual but possible)
	wrapped := mw(mockModelFunc(nil, nil))
	result, err := wrapped(context.Background(), &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("test")},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, result)

	span := exporter.FlushOne()
	span.AssertNameIs("genkit.generate")
	// Should still have input set
	assert.True(t, span.HasAttr("braintrust.input_json"))
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

	// Model that calls streaming callback then returns an error from it
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

	// Callback that returns an error
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
