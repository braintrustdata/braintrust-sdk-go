package adk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"
	"google.golang.org/genai"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

const (
	goldenAppName = "golden_test_app"
	goldenUserID  = "test-user"
	goldenModelID = "gemini-2.5-flash"
)

// setupGoldenTest sets up VCR-based tracing for golden tests.
// Returns ctx, tracer provider, and exporter for span assertions.
func setupGoldenTest(t *testing.T) (context.Context, oteltrace.TracerProvider, *oteltest.Exporter) {
	t.Helper()
	t.Parallel()
	ctx := context.Background()
	tp, exporter := oteltest.Setup(t)
	return ctx, tp, exporter
}

// goldenAPIKey returns the Google API key, using a dummy in replay mode.
func goldenAPIKey(t *testing.T) string {
	t.Helper()
	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("GOOGLE_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-google-key-for-replay"
	}
	return apiKey
}

// newTestModel creates a Gemini model backed by the test's real VCR recording.
func newTestModel(ctx context.Context, t *testing.T, modelID string, opts ...tracegenai.Option) model.LLM {
	t.Helper()
	httpClient := vcr.NewHTTPClient(t)
	if len(opts) > 0 {
		httpClient = tracegenai.WrapClient(httpClient, opts...)
	}
	llm, err := gemini.NewModel(ctx, modelID, &genai.ClientConfig{
		HTTPClient: httpClient,
		APIKey:     goldenAPIKey(t),
		Backend:    genai.BackendGeminiAPI,
	})
	require.NoError(t, err)
	return llm
}

// newGoldenRunner creates a traced agent and runner, using Gemini by default.
func newGoldenRunner(ctx context.Context, t *testing.T, tp oteltrace.TracerProvider, sessionID string, cfg llmagent.Config) *runner.Runner {
	t.Helper()
	if cfg.Model == nil {
		cfg.Model = newTestModel(ctx, t, goldenModelID)
	}
	cb := NewCallbacks(WithTracerProvider(tp))
	AddLLMAgentCallbacks(&cfg, WithCallback(cb))
	a, err := llmagent.New(cfg)
	require.NoError(t, err)

	svc := session.InMemoryService()
	_, err = svc.Create(ctx, &session.CreateRequest{AppName: goldenAppName, UserID: goldenUserID, SessionID: sessionID})
	require.NoError(t, err)

	r, err := runner.New(runner.Config{AppName: goldenAppName, Agent: a, SessionService: svc})
	require.NoError(t, err)
	return r
}

// runAndCollectFinal runs the agent and returns all final response events.
func runAndCollectFinal(ctx context.Context, t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content, runCfg agent.RunConfig) []*session.Event {
	t.Helper()
	var responses []*session.Event
	for ev, err := range r.Run(ctx, goldenUserID, sessionID, msg, runCfg) { //nolint:revive // range over Seq2
		require.NoError(t, err)
		if ev.IsFinalResponse() {
			responses = append(responses, ev)
		}
	}
	return responses
}

// assertFinalResponse checks the final response contains text.
func assertFinalResponse(t *testing.T, responses []*session.Event) {
	t.Helper()
	require.NotEmpty(t, responses, "expected at least one final response")
	content := responses[0].Content
	require.NotNil(t, content, "expected response content")
	require.NotEmpty(t, content.Parts, "expected response content with parts")
	require.NotEmpty(t, content.Parts[0].Text, "expected non-empty text in response")
}

// assertGoldenExchange verifies the model request and response recorded in the trace.
// Checking the complete contents also covers conversation history and binary parts.
func assertGoldenExchange(t *testing.T, exporter *oteltest.Exporter, contents []*genai.Content, result *model.LLMResponse) (*oteltest.Span, map[string]any) {
	t.Helper()
	spans := exporter.Flush()
	var modelSpans []oteltest.Span
	for _, span := range spans {
		if span.Name() == "call_llm" {
			modelSpans = append(modelSpans, span)
		}
	}
	require.Len(t, modelSpans, 1, "expected one model span per exchange")
	span := &modelSpans[0]
	span.AssertAttrEquals("gen_ai.request.model", goldenModelID)
	span.AssertAttrEquals("gen_ai.finish_reason", string(result.FinishReason))
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	input, ok := span.Input().(map[string]any)
	require.True(t, ok, "expected serialized model request")
	response, ok := span.Output().(map[string]any)
	require.True(t, ok, "expected serialized model response")
	assert.Equal(t, goldenJSON(t, contents), input["Contents"], "complete conversation should be captured")
	assert.Equal(t, goldenJSON(t, result.Content), response["Content"], "model output should match the runner response")
	assert.Equal(t, string(result.FinishReason), response["FinishReason"])
	// Token counts vary on every live run; verify the trace against that response.
	usage := result.UsageMetadata
	require.NotNil(t, usage)
	metrics := span.Metrics()
	assert.Equal(t, float64(usage.PromptTokenCount+usage.ToolUsePromptTokenCount), metrics["prompt_tokens"])
	assert.Equal(t, float64(usage.CandidatesTokenCount+usage.ThoughtsTokenCount), metrics["completion_tokens"])
	assert.Equal(t, float64(usage.TotalTokenCount), metrics["tokens"])
	assert.Equal(t, float64(usage.ThoughtsTokenCount), metrics["completion_reasoning_tokens"])
	config, ok := input["Config"].(map[string]any)
	require.True(t, ok, "expected serialized generation config")
	return span, config
}

func goldenJSON(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var result any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func TestMultimodalInput(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string
		mimeType    string
		prefix      string
		prompt      string
		instruction string
		maxTokens   int32
	}{
		{
			name:        "document",
			fixture:     "test-document.pdf",
			mimeType:    "application/pdf",
			prompt:      "What is in this document?",
			instruction: "You are a document analysis assistant.",
			maxTokens:   150,
		},
		{
			name:        "mixed_content",
			fixture:     "test-image.png",
			mimeType:    "image/png",
			prefix:      "First, look at this image:",
			prompt:      "Now describe what you see and explain why it matters.",
			instruction: "You are a helpful vision assistant.",
			maxTokens:   200,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, tp, exporter := setupGoldenTest(t)
			data, err := os.ReadFile(filepath.Join("testdata", "fixtures", tc.fixture))
			require.NoError(t, err)

			r := newGoldenRunner(ctx, t, tp, tc.name, llmagent.Config{
				Name:                  "multimodal_agent",
				Instruction:           tc.instruction,
				GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: tc.maxTokens},
			})
			var parts []*genai.Part
			if tc.prefix != "" {
				parts = append(parts, &genai.Part{Text: tc.prefix})
			}
			parts = append(parts,
				&genai.Part{InlineData: &genai.Blob{Data: data, MIMEType: tc.mimeType}},
				&genai.Part{Text: tc.prompt},
			)
			userMsg := genai.NewContentFromParts(parts, genai.RoleUser)
			responses := runAndCollectFinal(ctx, t, r, tc.name, userMsg, agent.RunConfig{})
			assertFinalResponse(t, responses)
			_, config := assertGoldenExchange(t, exporter, []*genai.Content{userMsg}, &responses[0].LLMResponse)
			assert.Equal(t, float64(tc.maxTokens), config["maxOutputTokens"])
		})
	}
}

func TestGenerationConfig(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)
	temp := float32(0)
	topP := float32(1)
	sid := "session-temp-temp_0_0"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:        "agent_temp_0_0",
		Instruction: "You are a creative storyteller.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			Temperature:     &temp,
			TopP:            &topP,
			MaxOutputTokens: 50,
			StopSequences:   []string{"END", "\n\n"},
		},
	})

	msg := genai.NewContentFromText("Say something creative.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	// A tight budget can be exhausted by reasoning before any text is emitted.
	require.Len(t, responses, 1)
	_, config := assertGoldenExchange(t, exporter, []*genai.Content{msg}, &responses[0].LLMResponse)
	assert.Equal(t, float64(0), config["temperature"], "explicit zero temperature should survive cleanup")
	assert.Equal(t, float64(1), config["topP"])
	assert.Equal(t, float64(50), config["maxOutputTokens"])
	assert.Equal(t, []any{"END", "\n\n"}, config["stopSequences"])
}

func TestGoogleSearchUsage(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-google-search"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "search_agent",
		Instruction:           "Use Google Search to answer the user's question.",
		Tools:                 []tool.Tool{geminitool.GoogleSearch{}},
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 500},
	})

	msg := genai.NewContentFromText("Search the web for the current stable Go release and name its major and minor version.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	assertFinalResponse(t, responses)

	assertGoldenExchange(t, exporter, []*genai.Content{msg}, &responses[0].LLMResponse)
}

func TestReasoning(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	thinkingBudget := int32(1024)
	sid := "session-reasoning"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:        "reasoning_agent",
		Instruction: "You are a mathematical reasoning assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 2048,
			ThinkingConfig:  &genai.ThinkingConfig{IncludeThoughts: true, ThinkingBudget: &thinkingBudget},
		},
	})

	msg1 := genai.NewContentFromText("Look at this sequence: 2, 6, 12, 20, 30. What is the pattern and what would be the formula for the nth term?", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg1, agent.RunConfig{})
	assertFinalResponse(t, responses)
	firstOutput := responses[0].Content
	firstSpan, config := assertGoldenExchange(t, exporter, []*genai.Content{msg1}, &responses[0].LLMResponse)
	assert.Equal(t, float64(2048), config["maxOutputTokens"])
	assert.Equal(t, goldenJSON(t, genai.NewContentFromText("You are a mathematical reasoning assistant.", genai.RoleUser)), config["systemInstruction"])
	assert.Equal(t, map[string]any{"includeThoughts": true, "thinkingBudget": float64(1024)}, config["thinkingConfig"])

	msg2 := genai.NewContentFromText("Using the pattern you discovered, what would be the 10th term? And can you find the sum of the first 10 terms?", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	assertFinalResponse(t, responses)
	assertGoldenExchange(t, exporter, []*genai.Content{msg1, firstOutput, msg2}, &responses[0].LLMResponse)

	assert.False(t, firstSpan.HasAttr("gen_ai.usage.prompt_tokens"))
}
