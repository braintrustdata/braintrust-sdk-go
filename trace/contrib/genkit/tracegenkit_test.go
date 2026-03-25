package genkit

import (
	"context"
	"os"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

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
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
	assert.True(t, span.HasAttr("braintrust.input_json"))
	assert.True(t, span.HasAttr("braintrust.output_json"))
	assert.True(t, span.HasAttr("braintrust.metadata"))

	metrics := span.Metrics()
	assert.Greater(t, metrics["time_to_first_token"], float64(0), "streaming should capture TTFT")
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
	weatherTool := genkit.DefineTool(g, "get_weather",
		"Get the current weather for a city",
		func(ctx *ai.ToolContext, input WeatherInput) (string, error) {
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

	// Genkit auto-executes tools, so we may see multiple LLM spans (tool call + final response).
	spans := exporter.Flush()
	var llmSpans []oteltest.Span
	for _, s := range spans {
		if s.Name() == "genkit.generate" {
			llmSpans = append(llmSpans, s)
		}
	}
	require.GreaterOrEqual(t, len(llmSpans), 1, "should have at least one LLM span")

	// First span should have tool_choice in metadata
	firstSpan := llmSpans[0]
	firstSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
	metadata := firstSpan.Metadata()
	assert.NotEmpty(t, metadata["tools"], "metadata.tools should be set")
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
		ai.WithMiddleware(NewMiddleware()),
	)
	require.Error(t, err)

	span := requireSpanNamed(t, exporter.Flush(), "genkit.generate")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
	assert.True(t, span.HasAttr("braintrust.metadata"))
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
	assertLLMSpan(t, span)
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
	assertLLMSpan(t, span)
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
	assertLLMSpan(t, span)

	metadata := span.Metadata()
	assert.Equal(t, 0.2, metadata["temperature"])
	assert.Equal(t, float64(32), metadata["max_output_tokens"])
}

// assertLLMSpan checks the structural invariants every LLM span must satisfy:
// span_attributes, input as OpenAI-format messages array, output with role/content/finish_reason,
// model/provider metadata, and token metrics.
func assertLLMSpan(t *testing.T, span oteltest.Span) {
	t.Helper()

	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	// Input must be an array of messages (OpenAI format)
	messages, ok := span.Input().([]any)
	require.True(t, ok, "input should be a messages array")
	assert.NotEmpty(t, messages, "input messages should not be empty")

	// Output must have role, content, and finish_reason (OpenAI format)
	output, ok := span.Output().(map[string]any)
	require.True(t, ok, "output should be a map")
	assert.NotEmpty(t, output["role"], "output.role should be set")
	assert.NotNil(t, output["content"], "output.content should be set")
	assert.NotEmpty(t, output["finish_reason"], "output.finish_reason should be set")

	// Metadata must have model
	metadata := span.Metadata()
	assert.NotEmpty(t, metadata["model"], "metadata.model should be set")

	// Token metrics must be present
	metrics := span.Metrics()
	assert.Greater(t, metrics["prompt_tokens"], float64(0), "prompt_tokens should be > 0")
	assert.Greater(t, metrics["completion_tokens"], float64(0), "completion_tokens should be > 0")
	assert.Greater(t, metrics["tokens"], float64(0), "tokens should be > 0")
}
