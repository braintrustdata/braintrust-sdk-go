package genkit

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

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

func openaiAPIKey(t *testing.T) string {
	t.Helper()
	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-openai-key-for-replay"
	}
	return apiKey
}

func newOpenAIPlugin(t *testing.T) *compatopenai.OpenAI {
	t.Helper()
	return &compatopenai.OpenAI{
		APIKey: openaiAPIKey(t),
		Opts: []option.RequestOption{
			option.WithHTTPClient(vcr.NewHTTPClient(t)),
		},
	}
}

func newGoogleAIPlugin(t *testing.T) *googlegenai.GoogleAI {
	t.Helper()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if vcr.GetVCRMode() != vcr.ModeReplay && apiKey == "" {
		t.Fatal("GEMINI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-gemini-key-for-replay"
	}

	client := vcr.NewHTTPClient(t)
	previousTransport := http.DefaultTransport
	http.DefaultTransport = client.Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	return &googlegenai.GoogleAI{APIKey: apiKey}
}

func TestAgentStateIsScopedToGenerateInvocation(t *testing.T) {
	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer("agent-state-test")
	rootCtx, root := tracer.Start(context.Background(), "root")
	defer root.End()
	firstCtx, firstParent := tracer.Start(rootCtx, "first-generate")
	secondCtx, secondParent := tracer.Start(rootCtx, "second-generate")

	cfg := &middlewareConfig{
		tracer: tracer,
		agents: make(map[string]*agentState),
	}
	initial := &ai.ModelRequest{
		Messages: []*ai.Message{ai.NewUserTextMessage("weather?")},
		Tools:    []*ai.ToolDefinition{{Name: "get_weather"}},
	}
	firstKey, firstState := prepareAgent(firstCtx, cfg, initial)
	secondKey, secondState := prepareAgent(secondCtx, cfg, initial)
	require.NotEqual(t, firstKey, secondKey)
	require.NotSame(t, firstState, secondState)

	updateAgentPending(firstState, []*ai.ToolRequest{{Ref: "call_1", Name: "get_weather"}})
	continuation := &ai.ModelRequest{
		Tools: initial.Tools,
		Messages: []*ai.Message{
			ai.NewUserTextMessage("weather?"),
			{Role: ai.RoleModel, Content: []*ai.Part{ai.NewToolRequestPart(&ai.ToolRequest{
				Ref: "call_1", Name: "get_weather", Input: map[string]any{"city": "Paris"},
			})}},
			{Role: ai.RoleTool, Content: []*ai.Part{ai.NewToolResponsePart(&ai.ToolResponse{
				Ref: "call_1", Name: "get_weather", Output: "sunny",
			})}},
		},
	}
	nestedCtx, nestedParent := tracer.Start(firstCtx, "nested-generate")
	continuationKey, continuationState := prepareAgent(nestedCtx, cfg, continuation)
	nestedParent.End()
	require.Equal(t, firstKey, continuationKey)
	require.Same(t, firstState, continuationState)

	firstParent.End()
	secondParent.End()
	require.Eventually(t, func() bool {
		cfg.agentMu.Lock()
		defer cfg.agentMu.Unlock()
		return len(cfg.agents) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestFinishAgentErrorFlushesMetrics(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	_, span := tp.Tracer("agent-error-test").Start(context.Background(), "agent")
	cfg := &middlewareConfig{agents: make(map[string]*agentState)}
	agent := &agentState{
		span:    span,
		metrics: make(map[string]float64),
		done:    make(chan struct{}),
	}
	cfg.agents["agent"] = agent
	addAgentMetrics(agent, map[string]float64{
		"prompt_tokens":     10,
		"completion_tokens": 5,
		"tokens":            15,
	})

	finishAgentError(cfg, "agent", agent, errors.New("model failed"), map[string]float64{
		"prompt_tokens":     4,
		"completion_tokens": 1,
		"tokens":            5,
	})
	span.End()

	exported := exporter.FlushOne()
	assert.Equal(t, map[string]float64{
		"prompt_tokens":     14,
		"completion_tokens": 6,
		"tokens":            20,
	}, exported.Metrics())
	assert.Empty(t, cfg.agents)
}

func TestEnableStreamUsageClonesPointerConfig(t *testing.T) {
	original := &openaigo.ChatCompletionNewParams{
		MaxCompletionTokens: openaigo.Int(32),
	}
	req := &ai.ModelRequest{Config: original}

	enableStreamUsage(req)

	cloned, ok := req.Config.(*openaigo.ChatCompletionNewParams)
	require.True(t, ok)
	require.NotSame(t, original, cloned)
	assert.False(t, original.StreamOptions.IncludeUsage.Valid())
	assert.True(t, cloned.StreamOptions.IncludeUsage.Or(false))
}

func TestExtractMetrics(t *testing.T) {
	tests := []struct {
		name     string
		usage    *ai.GenerationUsage
		expected map[string]float64
	}{
		{
			name: "all standard fields",
			usage: &ai.GenerationUsage{
				InputTokens:         10,
				OutputTokens:        20,
				TotalTokens:         30,
				CachedContentTokens: 5,
				ThoughtsTokens:      8,
				Custom: map[string]float64{
					"acceptedPredictionTokens": 4,
					"audioTokens":              3,
				},
			},
			expected: map[string]float64{
				"prompt_tokens":               10,
				"completion_tokens":           20,
				"tokens":                      30,
				"prompt_cached_tokens":        5,
				"completion_reasoning_tokens": 8,
				"completion_audio_tokens":     3,
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
			name:  "prompt tokens only",
			usage: &ai.GenerationUsage{InputTokens: 10},
			expected: map[string]float64{
				"prompt_tokens": 10,
				"tokens":        10,
			},
		},
		{
			name:  "completion tokens only",
			usage: &ai.GenerationUsage{OutputTokens: 20},
			expected: map[string]float64{
				"completion_tokens": 20,
				"tokens":            20,
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

func TestExtractGoogleMetrics(t *testing.T) {
	resp := &ai.ModelResponse{
		Usage: &ai.GenerationUsage{
			InputTokens:         10,
			OutputTokens:        20,
			TotalTokens:         39,
			CachedContentTokens: 4,
			ThoughtsTokens:      7,
		},
		Custom: map[string]any{
			"usageMetadata": map[string]any{
				"promptTokenCount":        10,
				"candidatesTokenCount":    20,
				"thoughtsTokenCount":      7,
				"toolUsePromptTokenCount": 2,
				"totalTokenCount":         39,
				"cachedContentTokenCount": 4,
			},
		},
	}

	assert.Equal(t, map[string]float64{
		"prompt_tokens":               12,
		"completion_tokens":           27,
		"completion_reasoning_tokens": 7,
		"tokens":                      39,
		"prompt_cached_tokens":        4,
	}, extractMetrics(resp, 0))
}

func TestInferProviderGoogle(t *testing.T) {
	assert.Equal(t, "google", inferProvider(nil, "googleai/gemini-2.5-flash"))
	assert.Equal(t, "google", inferProvider(nil, "gemini-2.5-flash"))
}

func TestResponseMetadataGoogleModelVersion(t *testing.T) {
	metadata := responseMetadata(&middlewareConfig{}, &ai.ModelResponse{Custom: map[string]any{
		"modelVersion":  "gemini-2.5-flash-001",
		"usageMetadata": map[string]any{},
	}})

	assert.Equal(t, "gemini-2.5-flash-001", metadata["model"])
	assert.Equal(t, "google", metadata["provider"])
}

func TestGoogleGenerate(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newGoogleAIPlugin(t)),
		genkit.WithDefaultModel("googleai/gemini-2.5-flash"),
	)
	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France? Answer in one word."),
		ai.WithMiddleware(NewMiddleware(WithModel("gemini-2.5-flash"))),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assertLLMSpan(t, span, "google")
	assert.Equal(t, "google", span.Metadata()["provider"])
	assert.Equal(t, "gemini-2.5-flash", span.Metadata()["model"])
	metrics := span.Metrics()
	assert.Equal(t, metrics["prompt_tokens"]+metrics["completion_tokens"], metrics["tokens"])
}

func TestFinishReason(t *testing.T) {
	tests := []struct {
		name     string
		response *ai.ModelResponse
		expected string
	}{
		{name: "stop", response: &ai.ModelResponse{FinishReason: ai.FinishReasonStop}, expected: "stop"},
		{name: "length", response: &ai.ModelResponse{FinishReason: ai.FinishReasonLength}, expected: "length"},
		{name: "blocked", response: &ai.ModelResponse{FinishReason: ai.FinishReasonBlocked}, expected: "content_filter"},
		{name: "interrupted", response: &ai.ModelResponse{FinishReason: ai.FinishReasonInterrupted}, expected: "interrupted"},
		{name: "other", response: &ai.ModelResponse{FinishReason: ai.FinishReasonOther}, expected: "other"},
		{name: "unknown", response: &ai.ModelResponse{FinishReason: ai.FinishReasonUnknown}, expected: "unknown"},
		{name: "provider specific", response: &ai.ModelResponse{FinishReason: ai.FinishReason("safety_recitation")}, expected: "safety_recitation"},
		{name: "empty", response: &ai.ModelResponse{}, expected: "stop"},
		{
			name: "tool call takes precedence",
			response: &ai.ModelResponse{Message: &ai.Message{Content: []*ai.Part{
				ai.NewToolRequestPart(&ai.ToolRequest{Name: "weather"}),
			}}},
			expected: "tool_calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, finishReason(tt.response))
		})
	}
}

func TestCanonicalMessagePayloads(t *testing.T) {
	req := &ai.ModelRequest{Messages: []*ai.Message{
		ai.NewUserTextMessage("weather?"),
		{Role: ai.RoleModel, Content: []*ai.Part{ai.NewToolRequestPart(&ai.ToolRequest{
			Ref: "call_123", Name: "get_weather", Input: map[string]any{"city": "Paris"},
		})}},
		{Role: ai.RoleTool, Content: []*ai.Part{ai.NewToolResponsePart(&ai.ToolResponse{
			Ref: "call_123", Name: "get_weather", Output: map[string]any{"temperature": 18},
		})}},
	}}

	assert.Equal(t, []map[string]any{
		{"role": "user", "content": "weather?"},
		{
			"role": "assistant", "content": nil,
			"tool_calls": []map[string]any{{
				"id": "call_123", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`},
			}},
		},
		{"role": "tool", "tool_call_id": "call_123", "content": `{"temperature":18}`},
	}, cleanupForInput(req))

	resp := &ai.ModelResponse{
		Message:      req.Messages[1],
		FinishReason: ai.FinishReasonStop,
	}
	assert.Equal(t, []map[string]any{{
		"index": float64(0), "finish_reason": "tool_calls",
		"message": map[string]any{
			"role": "assistant", "content": nil,
			"tool_calls": []map[string]any{{
				"id": "call_123", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`},
			}},
		},
	}}, cleanupForOutput(resp))
}

func TestStreaming(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	var chunks []string
	for sv, err := range genkit.GenerateStream(ctx, g,
		ai.WithPrompt("Count from 1 to 3, one number per line."),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			MaxCompletionTokens: openaigo.Int(32),
		}),
		ai.WithMiddleware(NewMiddleware()),
	) {
		require.NoError(t, err)
		if sv.Done {
			break
		}
		chunks = append(chunks, sv.Chunk.Text())
	}
	assert.NotEmpty(t, chunks, "should have received streaming chunks")

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assertLLMSpan(t, span, "openai")

	metrics := span.Metrics()
	assert.Greater(t, metrics["time_to_first_token"], float64(0), "streaming should capture TTFT")
}

func TestStreamingEarlyTermination(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)
	stopErr := errors.New("stop consuming stream")
	chunks := 0
	_, err := genkit.Generate(ctx, g,
		ai.WithPrompt("Count slowly from 1 to 20."),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			MaxCompletionTokens: openaigo.Int(64),
		}),
		ai.WithStreaming(func(_ context.Context, _ *ai.ModelResponseChunk) error {
			chunks++
			return stopErr
		}),
		ai.WithMiddleware(NewMiddleware()),
	)
	require.ErrorIs(t, err, stopErr)
	require.Equal(t, 1, chunks)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.False(t, span.HasAttr("braintrust.output_json"))
	assert.Greater(t, span.Metrics()["time_to_first_token"], float64(0))
}

func TestToolUse(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	type WeatherInput struct {
		City string `json:"city"`
	}
	weatherTool := DefineTool(g, "get_weather",
		"Get the current weather for a city",
		func(ctx *ai.ToolContext, input WeatherInput) (string, error) {
			time.Sleep(25 * time.Millisecond)
			return "18C, partly cloudy", nil
		},
	)

	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the weather in Paris? Use the get_weather tool."),
		ai.WithTools(weatherTool),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			MaxCompletionTokens: openaigo.Int(64),
		}),
		ai.WithMiddleware(NewMiddleware()),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	spans := exporter.Flush()
	var llmSpans []oteltest.Span
	var taskSpans []oteltest.Span
	var toolSpans []oteltest.Span
	for _, s := range spans {
		if !s.HasAttr("braintrust.span_attributes") {
			continue
		}
		attrs := s.Attr("braintrust.span_attributes").String()
		switch attrs {
		case `{"type":"llm"}`:
			llmSpans = append(llmSpans, s)
		case `{"name":"genkit.generate","type":"task"}`:
			taskSpans = append(taskSpans, s)
		case `{"type":"tool"}`:
			toolSpans = append(toolSpans, s)
		}
	}
	require.Len(t, llmSpans, 2, "tool loop should trace each model call")
	require.Len(t, taskSpans, 1, "tool loop should have one task parent")
	require.Len(t, toolSpans, 1, "tool execution should have one tool span")

	for i := range llmSpans {
		llmSpans[i].AssertChildOf(&taskSpans[0])
	}
	toolSpans[0].AssertChildOf(&taskSpans[0])

	firstSpan := llmSpans[0]
	metadata := firstSpan.Metadata()
	assert.Equal(t, "auto", metadata["tool_choice"])
	assert.Equal(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get the current weather for a city",
			"parameters": map[string]any{
				"additionalProperties": false,
				"properties":           map[string]any{"city": map[string]any{"type": "string"}},
				"required":             []any{"city"},
				"type":                 "object",
			},
		},
	}}, metadata["tools"])

	firstOutput := firstSpan.Output().([]any)[0].(map[string]any)
	assert.Equal(t, "tool_calls", firstOutput["finish_reason"])
	toolCall := firstOutput["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	assert.NotEmpty(t, toolCall["id"])
	assert.Equal(t, "get_weather", toolCall["function"].(map[string]any)["name"])

	toolSpans[0].AssertNameIs("get_weather")
	assert.Equal(t, map[string]any{"city": "Paris"}, toolSpans[0].Input())
	assert.Equal(t, "18C, partly cloudy", toolSpans[0].Output())
	assert.GreaterOrEqual(t, toolSpans[0].Stub.EndTime.Sub(toolSpans[0].Stub.StartTime), 25*time.Millisecond)
	assert.NotEmpty(t, taskSpans[0].Output())
	assert.Equal(t, float64(173), taskSpans[0].Metrics()["tokens"])
}

func TestWrapToolFuncStartsChildSpan(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	ctx, parentSpan := tp.Tracer("application").Start(context.Background(), "application")
	wrapped := WrapToolFunc("wrapped_tool", func(_ *ai.ToolContext, input string) (string, error) {
		return input + " output", nil
	})

	output, err := wrapped(&ai.ToolContext{Context: ctx}, "test")
	require.NoError(t, err)
	assert.Equal(t, "test output", output)
	parentSpan.End()

	spans := exporter.Flush()
	parent := requireSpanNamed(t, spans, "application")
	child := requireSpanNamed(t, spans, "wrapped_tool")
	child.AssertChildOf(&parent)
	assert.False(t, parent.HasAttr("braintrust.span_attributes"))
	assert.Equal(t, "test", child.Input())
	assert.Equal(t, "test output", child.Output())
}

func TestToolErrorIsTracedDuringExecution(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	type ToolInput struct {
		Value string `json:"value"`
	}
	wantErr := errors.New("tool failed")
	g := genkit.Init(context.Background())
	tool := DefineTool(g, "failing_tool", "Always fails",
		func(*ai.ToolContext, ToolInput) (string, error) {
			time.Sleep(25 * time.Millisecond)
			return "", wantErr
		},
	)

	_, err := tool.RunRaw(context.Background(), map[string]any{"value": "test"})
	require.ErrorIs(t, err, wantErr)

	span := requireSpanNamed(t, exporter.Flush(), "failing_tool")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "tool"})
	assert.Equal(t, map[string]any{"value": "test"}, span.Input())
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.GreaterOrEqual(t, span.Stub.EndTime.Sub(span.Stub.StartTime), 25*time.Millisecond)
}

func TestErrorHandling(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
	)

	_, err := genkit.Generate(ctx, g,
		ai.WithPrompt("hello"),
		ai.WithModelName("openai/nonexistent-model-12345"),
		ai.WithMiddleware(NewMiddleware(
			WithProvider("openai"),
			WithModel("nonexistent-model-12345"),
		)),
	)
	require.Error(t, err)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
	assert.Equal(t, "openai", span.Metadata()["provider"])
	assert.Equal(t, "nonexistent-model-12345", span.Metadata()["model"])
	assert.Equal(t, codes.Error, span.Status().Code)
}

func TestSequentialCalls(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	for i := 0; i < 2; i++ {
		resp, err := genkit.Generate(ctx, g,
			ai.WithPrompt("Say hi."),
			ai.WithConfig(&openaigo.ChatCompletionNewParams{
				MaxCompletionTokens: openaigo.Int(8),
			}),
			ai.WithMiddleware(NewMiddleware()),
		)
		require.NoError(t, err)
		require.NotNil(t, resp)
	}

	var llmSpans []oteltest.Span
	for _, s := range exporter.Flush() {
		if s.Name() == "genkit.generate" {
			llmSpans = append(llmSpans, s)
		}
	}
	assert.Len(t, llmSpans, 2, "sequential calls should produce separate spans")
}

func TestGenerateDefaultModel(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France? Answer in one word."),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			MaxCompletionTokens: openaigo.Int(16),
		}),
		ai.WithMiddleware(NewMiddleware()),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assertLLMSpan(t, span, "openai")
}

func TestGenerateWithModelName(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
	)

	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France? Answer in one word."),
		ai.WithModelName("openai/gpt-4o-mini"),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			MaxCompletionTokens: openaigo.Int(16),
		}),
		ai.WithMiddleware(NewMiddleware()),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assertLLMSpan(t, span, "openai")
}

func TestMiddlewareIntegration(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(newOpenAIPlugin(t)),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France? Answer in one word."),
		ai.WithConfig(&openaigo.ChatCompletionNewParams{
			Model:               "gpt-4o-mini",
			Temperature:         openaigo.Float(0.2),
			MaxCompletionTokens: openaigo.Int(32),
		}),
		ai.WithMiddleware(NewMiddleware()),
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	assertLLMSpan(t, span, "openai")

	metadata := span.Metadata()
	assert.Equal(t, 0.2, metadata["temperature"])
	assert.Equal(t, float64(32), metadata["max_tokens"])
	assert.Len(t, metadata, 4, "metadata capture must stay allowlisted")
	assert.NotContains(t, metadata, "system")
	assert.NotContains(t, metadata, "latency_ms")
	assert.NotContains(t, metadata, "system_fingerprint")
}

// assertLLMSpan checks the structural invariants every LLM span must satisfy:
// span_attributes, input as OpenAI-format messages array, output with role/content/finish_reason,
// model/provider metadata, and token metrics.
func assertLLMSpan(t *testing.T, span oteltest.Span, expectedProvider string) {
	t.Helper()

	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	// Input must be an array of messages (OpenAI format)
	messages, ok := span.Input().([]any)
	require.True(t, ok, "input should be a messages array")
	assert.NotEmpty(t, messages, "input messages should not be empty")

	// Output must be an array of OpenAI Chat Completions choices.
	output, ok := span.Output().([]any)
	require.True(t, ok, "output should be a choices array")
	require.Len(t, output, 1)
	choice, ok := output[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), choice["index"])
	assert.NotEmpty(t, choice["finish_reason"])
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", message["role"])
	assert.NotNil(t, message["content"])

	metadata := span.Metadata()
	assert.NotEmpty(t, metadata["model"], "metadata.model should be set")
	assert.Equal(t, expectedProvider, metadata["provider"])

	// Token metrics must be present
	metrics := span.Metrics()
	assert.Greater(t, metrics["prompt_tokens"], float64(0), "prompt_tokens should be > 0")
	assert.Greater(t, metrics["completion_tokens"], float64(0), "completion_tokens should be > 0")
	assert.Greater(t, metrics["tokens"], float64(0), "tokens should be > 0")
}
