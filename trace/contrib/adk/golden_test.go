package adk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
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
	ctx := context.Background()
	tp, exporter := oteltest.Setup(t)
	otel.SetTracerProvider(tp)
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

// runAndCollectStreamedText runs the agent and concatenates all content text parts.
func runAndCollectStreamedText(ctx context.Context, t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content, runCfg agent.RunConfig) string {
	t.Helper()
	var fullText strings.Builder
	for ev, err := range r.Run(ctx, goldenUserID, sessionID, msg, runCfg) {
		require.NoError(t, err)
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					fullText.WriteString(p.Text)
				}
			}
		}
	}
	return fullText.String()
}

// runAndCollectAll runs the agent and returns all events.
func runAndCollectAll(ctx context.Context, t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content, runCfg agent.RunConfig) []*session.Event {
	t.Helper()
	var events []*session.Event
	for ev, err := range r.Run(ctx, goldenUserID, sessionID, msg, runCfg) {
		require.NoError(t, err)
		events = append(events, ev)
	}
	return events
}

// assertFinalResponse asserts there is at least one final response with non-empty text.
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

	assertSpansExist(t, exporter)
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

	msg2 := genai.NewContentFromText("What did I just tell you my name was?", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	text = assertFinalResponse(t, responses)
	t.Logf("Response 2: %s", text)

	assertSpansExist(t, exporter)
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

	assertSpansExist(t, exporter)
}

func TestStreaming(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-streaming"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "counting_agent",
		Instruction:           "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	msg := genai.NewContentFromText("Count from 1 to 10 slowly.", genai.RoleUser)
	runCfg := agent.RunConfig{StreamingMode: agent.StreamingMode("streaming")}
	fullText := runAndCollectStreamedText(ctx, t, r, sid, msg, runCfg)
	t.Logf("Streamed: %s", fullText)
	require.NotEmpty(t, fullText, "expected some streamed text")

	assertSpansExist(t, exporter)
}

func TestImageInput(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	if path == "" {
		t.Skip("testdata/fixtures/test-image.png not found")
	}
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

	assertSpansExist(t, exporter)
}

func TestDocumentInput(t *testing.T) {
	path := fixturePath(t, "test-document.pdf")
	if path == "" {
		t.Skip("testdata/fixtures/test-document.pdf not found")
	}
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

	assertSpansExist(t, exporter)
}

func TestTemperatureVariations(t *testing.T) {
	configs := []struct {
		name string
		temp float32
		topP float32
	}{
		{"temp_0_0", 0.0, 1.0},
		{"temp_1_0", 1.0, 0.9},
		{"temp_0_7", 0.7, 0.95},
	}
	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			ctx, tp, exporter := setupGoldenTest(t)
			temp := cfg.temp
			topP := cfg.topP

			sid := fmt.Sprintf("session-temp-%s", cfg.name)
			r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
				Name:        fmt.Sprintf("agent_%s", cfg.name),
				Instruction: "You are a creative storyteller.",
				GenerateContentConfig: &genai.GenerateContentConfig{
					Temperature:     &temp,
					TopP:            &topP,
					MaxOutputTokens: 50,
				},
			})

			msg := genai.NewContentFromText("Say something creative.", genai.RoleUser)
			fullText := runAndCollectStreamedText(ctx, t, r, sid, msg, agent.RunConfig{})
			t.Logf("Config temp=%v top_p=%v: %s", cfg.temp, cfg.topP, fullText)

			assertSpansExist(t, exporter)
		})
	}
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

	assertSpansExist(t, exporter)
}

func TestMetadata(t *testing.T) {
	t.Skip("labels parameter is not supported in Gemini API")

	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-metadata"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:        "basic_agent",
		Instruction: "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 100,
			Labels:          map[string]string{"user_id": "test_user_123", "environment": "testing", "feature": "metadata_test"},
		},
	})

	msg := genai.NewContentFromText("Hello!", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	assertSpansExist(t, exporter)
}

func TestLongContext(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-long"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "analysis_agent",
		Instruction:           "You are a text analysis assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 100},
	})

	longText := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	msg := genai.NewContentFromText(fmt.Sprintf("Here is a long text:\n\n%s\n\nHow many times does the word 'fox' appear?", longText), genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)

	assertSpansExist(t, exporter)
}

func TestMixedContent(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	if path == "" {
		t.Skip("testdata/fixtures/test-image.png not found")
	}
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

	assertSpansExist(t, exporter)
}

func TestPrefill(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	sid := "session-prefill"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "haiku_agent",
		Instruction:           "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	msg1 := genai.NewContentFromText("Write a haiku about coding.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg1, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response 1: %s", text)

	msg2 := genai.NewContentFromText("Here is a haiku:", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	text = assertFinalResponse(t, responses)
	t.Logf("Response 2: %s", text)

	assertSpansExist(t, exporter)
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

	assertSpansExist(t, exporter)
}

// getWeatherArgs and getWeather implement the weather tool for TestToolUse.
type getWeatherArgs struct {
	CityAndState string `json:"city_and_state" jsonschema:"The city and state, e.g. San Francisco, CA"`
	Unit         string `json:"unit" jsonschema:"The unit of temperature (celsius or fahrenheit). Default to celsius."`
}

func getWeather(_ tool.Context, args getWeatherArgs) (string, error) {
	unit := args.Unit
	if unit == "" {
		unit = "celsius"
	}
	return fmt.Sprintf("22 degrees %s and sunny in %s", unit, args.CityAndState), nil
}

func TestToolUse(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	weatherTool, err := functiontool.New(
		functiontool.Config{
			Name:        "get_weather",
			Description: "Get the current weather for a location. Args: city_and_state (e.g. San Francisco, CA), unit (celsius or fahrenheit).",
		},
		getWeather,
	)
	require.NoError(t, err)

	sid := "session-weather"
	r := newGoldenRunner(ctx, t, tp, sid, llmagent.Config{
		Name:                  "weather_agent",
		Instruction:           "You are a helpful weather assistant. Use the get_weather tool to answer questions.",
		Tools:                 []tool.Tool{weatherTool},
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 500},
	})

	msg := genai.NewContentFromText("What is the weather like in Paris, France?", genai.RoleUser)

	var hasFunctionCall bool
	var finalText string
	for _, ev := range runAndCollectAll(ctx, t, r, sid, msg, agent.RunConfig{}) {
		if ev.Content != nil {
			for i, p := range ev.Content.Parts {
				if p.FunctionCall != nil {
					hasFunctionCall = true
					t.Logf("Tool use block %d: %s %v", i, p.FunctionCall.Name, p.FunctionCall.Args)
				} else if p.Text != "" && ev.IsFinalResponse() {
					finalText = p.Text
					t.Logf("Final text: %s", p.Text)
				}
			}
		}
	}

	assert.True(t, hasFunctionCall, "expected at least one function call")
	assert.NotEmpty(t, finalText, "expected final response text")

	// Richer span assertions for tool use
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "expected spans to be created")

	var agentSpan, modelSpan, toolSpan *oteltest.Span
	for i := range spans {
		s := &spans[i]
		name := s.Name()
		switch name {
		case "agent_run [weather_agent]":
			agentSpan = s
		case "call_llm":
			if modelSpan == nil {
				modelSpan = s
			}
		case "tool [get_weather]":
			toolSpan = s
		}
	}

	require.NotNil(t, agentSpan, "agent_run span not found")
	assert.True(t, agentSpan.Status().Code == codes.Ok || agentSpan.Status().Code == codes.Unset)
	agentSpan.AssertAttrEquals("adk.agent.name", "weather_agent")

	require.NotNil(t, modelSpan, "call_llm span not found")
	assert.True(t, modelSpan.Status().Code == codes.Ok || modelSpan.Status().Code == codes.Unset)

	require.NotNil(t, toolSpan, "tool span not found")
	assert.True(t, toolSpan.Status().Code == codes.Ok || toolSpan.Status().Code == codes.Unset)
	toolSpan.AssertAttrEquals("tool.name", "get_weather")
	assert.True(t, toolSpan.HasAttr("tool.input"))
	assert.True(t, toolSpan.HasAttr("tool.output"))

	// Verify parent-child relationships
	assert.Equal(t, agentSpan.Stub.SpanContext.SpanID(), modelSpan.Stub.Parent.SpanID(),
		"model span should be child of agent span")
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

	assertSpansExist(t, exporter)
}
