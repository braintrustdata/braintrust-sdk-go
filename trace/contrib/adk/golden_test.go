package adk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"
	"google.golang.org/genai"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
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

// newGoldenRunner creates a VCR-wrapped Gemini agent and runner.
func newGoldenRunner(ctx context.Context, t *testing.T, tp oteltrace.TracerProvider, sessionID string, cfg llmagent.Config) *runner.Runner {
	t.Helper()

	httpClient := vcr.NewHTTPClient(t)
	apiKey := goldenAPIKey(t)

	llm, err := gemini.NewModel(ctx, goldenModelID, &genai.ClientConfig{
		HTTPClient: httpClient,
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
	})
	require.NoError(t, err)
	cfg.Model = llm

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

// fixturePath returns path to a fixture file; empty if file does not exist.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(filename), "testdata", "fixtures", name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
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
func assertFinalResponse(t *testing.T, responses []*session.Event) string {
	t.Helper()
	require.NotEmpty(t, responses, "expected at least one final response")
	content := responses[0].Content
	require.NotNil(t, content, "expected response content")
	require.NotEmpty(t, content.Parts, "expected response content with parts")
	text := content.Parts[0].Text
	require.NotEmpty(t, text, "expected non-empty text in response")
	return text
}

// assertSpansExist verifies that at least one span was produced.
func assertSpansExist(t *testing.T, exporter *oteltest.Exporter) []oteltest.Span {
	t.Helper()
	spans := exporter.Flush()
	assert.NotEmpty(t, spans, "expected spans to be created")
	return spans
}

// assertGoldenExchange verifies the model request and response recorded in the trace.
// Checking the complete contents also covers conversation history and binary parts.
func assertGoldenExchange(t *testing.T, exporter *oteltest.Exporter, contents []*genai.Content, output *genai.Content, finishReason genai.FinishReason) map[string]any {
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
	span.AssertAttrEquals("gen_ai.finish_reason", string(finishReason))
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	input, ok := span.Input().(map[string]any)
	require.True(t, ok, "expected serialized model request")
	response, ok := span.Output().(map[string]any)
	require.True(t, ok, "expected serialized model response")
	assert.Equal(t, goldenJSON(t, contents), input["Contents"], "complete conversation should be captured")
	assert.Equal(t, goldenJSON(t, output), response["Content"], "model output should match the runner response")
	assert.Equal(t, string(finishReason), response["FinishReason"])
	config, ok := input["Config"].(map[string]any)
	require.True(t, ok, "expected serialized generation config")
	return config
}

func goldenJSON(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var result any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func TestBasicCompletion(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	r := newGoldenRunner(ctx, t, tp, "session-basic", llmagent.Config{
		Name:        "basic_agent",
		Instruction: "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 100,
		},
	})

	userMsg := genai.NewContentFromText("What is the capital of France?", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, "session-basic", userMsg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{userMsg}, responses[0].Content, genai.FinishReasonStop)
	assert.Equal(t, float64(100), config["maxOutputTokens"])
	assert.Equal(t, goldenJSON(t, genai.NewContentFromText("You are a helpful assistant.", genai.RoleUser)), config["systemInstruction"])
}

func TestMultiTurn(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-multi-turn"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "conversation_agent",
		Instruction:           "You are a helpful assistant with good memory.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	msg1 := genai.NewContentFromText("Hi, my name is Alice.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg1, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response 1: %s", text)
	firstOutput := responses[0].Content
	assertGoldenExchange(t, exporter, []*genai.Content{msg1}, firstOutput, genai.FinishReasonStop)

	msg2 := genai.NewContentFromText("What did I just tell you my name was?", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	text = assertFinalResponse(t, responses)
	t.Logf("Response 2: %s", text)

	assertGoldenExchange(t, exporter, []*genai.Content{msg1, firstOutput, msg2}, responses[0].Content, genai.FinishReasonStop)
}

func TestSystemPrompt(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-pirate"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "pirate_agent",
		Instruction:           "You are a pirate. Always respond in pirate speak.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 150},
	})

	msg := genai.NewContentFromText("Tell me about the weather.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{msg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(150), config["maxOutputTokens"])
	assert.Equal(t, goldenJSON(t, genai.NewContentFromText("You are a pirate. Always respond in pirate speak.", genai.RoleUser)), config["systemInstruction"])
}

func TestImageInput(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	require.NotEmpty(t, path, "testdata/fixtures/test-image.png must exist")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-vision"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "vision_agent",
		Instruction:           "You are a helpful vision assistant that can analyze images.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 150},
	})

	userMsg := genai.NewContentFromParts([]*genai.Part{
		{InlineData: &genai.Blob{Data: data, MIMEType: "image/png"}},
		{Text: "What color is this image?"},
	}, genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, userMsg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{userMsg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(150), config["maxOutputTokens"])
}

func TestDocumentInput(t *testing.T) {
	path := fixturePath(t, "test-document.pdf")
	require.NotEmpty(t, path, "testdata/fixtures/test-document.pdf must exist")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-document"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "doc_agent",
		Instruction:           "You are a document analysis assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 150},
	})

	userMsg := genai.NewContentFromParts([]*genai.Part{
		{InlineData: &genai.Blob{Data: data, MIMEType: "application/pdf"}},
		{Text: "What is in this document?"},
	}, genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, userMsg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{userMsg}, responses[0].Content, genai.FinishReasonStop)
	assert.Equal(t, float64(150), config["maxOutputTokens"])
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
		},
	})

	msg := genai.NewContentFromText("Say something creative.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	assertFinalResponse(t, responses)
	config := assertGoldenExchange(t, exporter, []*genai.Content{msg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(0), config["temperature"], "explicit zero temperature should survive cleanup")
	assert.Equal(t, float64(1), config["topP"])
	assert.Equal(t, float64(50), config["maxOutputTokens"])
}

func TestStopSequences(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-stop"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:        "story_agent",
		Instruction: "You are a creative writer.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 500,
			StopSequences:   []string{"END", "\n\n"},
		},
	})

	msg := genai.NewContentFromText("Write a short story about a robot.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{msg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(500), config["maxOutputTokens"])
	assert.Equal(t, []any{"END", "\n\n"}, config["stopSequences"])
}

func TestMixedContent(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	require.NotEmpty(t, path, "testdata/fixtures/test-image.png must exist")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-mixed"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "vision_agent",
		Instruction:           "You are a helpful vision assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	userMsg := genai.NewContentFromParts([]*genai.Part{
		{Text: "First, look at this image:"},
		{InlineData: &genai.Blob{Data: data, MIMEType: "image/png"}},
		{Text: "Now describe what you see and explain why it matters."},
	}, genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, userMsg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{userMsg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(200), config["maxOutputTokens"])
}

func TestShortMaxTokens(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-brief"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "brief_agent",
		Instruction:           "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 5},
	})

	msg := genai.NewContentFromText("What is AI?", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	config := assertGoldenExchange(t, exporter, []*genai.Content{msg}, responses[0].Content, genai.FinishReasonMaxTokens)
	assert.Equal(t, float64(5), config["maxOutputTokens"])
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
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	spans := assertSpansExist(t, exporter)
	var modelSpan *oteltest.Span
	for i := range spans {
		if spans[i].Name() == "call_llm" {
			modelSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, modelSpan)

	metrics := modelSpan.Metrics()
	assert.Equal(t, float64(157), metrics["prompt_tokens"])
	assert.Equal(t, float64(295), metrics["completion_tokens"])
	assert.Equal(t, float64(239), metrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(452), metrics["tokens"])
	assert.Equal(t, metrics["tokens"], metrics["prompt_tokens"]+metrics["completion_tokens"])
	assert.NotContains(t, metrics, "tool_use_prompt_token_count")
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
	text := assertFinalResponse(t, responses)
	t.Logf("First response: %s", text)

	msg2 := genai.NewContentFromText("Using the pattern you discovered, what would be the 10th term? And can you find the sum of the first 10 terms?", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	text = assertFinalResponse(t, responses)
	t.Logf("Follow-up response: %s", text)

	spans := assertSpansExist(t, exporter)
	var modelSpans []*oteltest.Span
	for i := range spans {
		if spans[i].Name() == "call_llm" {
			modelSpans = append(modelSpans, &spans[i])
		}
	}
	require.Len(t, modelSpans, 2)

	firstMetrics := modelSpans[0].Metrics()
	assert.Equal(t, float64(47), firstMetrics["prompt_tokens"])
	assert.Equal(t, float64(1608), firstMetrics["completion_tokens"])
	assert.Equal(t, float64(765), firstMetrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(1655), firstMetrics["tokens"])
	assert.Equal(t, firstMetrics["tokens"], firstMetrics["prompt_tokens"]+firstMetrics["completion_tokens"])
	assert.NotContains(t, firstMetrics, "tool_use_prompt_token_count")
	assert.False(t, modelSpans[0].HasAttr("gen_ai.usage.prompt_tokens"))

	secondMetrics := modelSpans[1].Metrics()
	assert.Equal(t, float64(1475), secondMetrics["prompt_tokens"])
	assert.Equal(t, float64(1627), secondMetrics["completion_tokens"])
	assert.Equal(t, float64(900), secondMetrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(3102), secondMetrics["tokens"])
	assert.Equal(t, secondMetrics["tokens"], secondMetrics["prompt_tokens"]+secondMetrics["completion_tokens"])
}
