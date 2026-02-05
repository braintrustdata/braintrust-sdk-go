// Package golden provides golden tests for Braintrust SDK integrations,
// mirroring the format of internal/golden in the braintrust-sdk repo (e.g. google_adk.py).
package golden

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceadk "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk"
)

const (
	appName        = "golden_test_app"
	userID         = "test-user"
	sessionIDBasic = "session-basic"
)

// skipGolden skips the test if -short or required API keys are missing.
func skipGolden(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping golden ADK test in short mode")
	}
	if os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY not set (required for golden ADK test)")
	}
	if os.Getenv("BRAINTRUST_API_KEY") == "" {
		t.Skip("BRAINTRUST_API_KEY not set (required for golden ADK test)")
	}
}

// fixturePath returns path to a fixture file; empty if file does not exist.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(filename), "fixtures")
	p := filepath.Join(fixturesDir, name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// setupGoldenTest initializes tracing, Braintrust, and a span; returns ctx and a cleanup function.
func setupGoldenTest(t *testing.T, spanName string) (ctx context.Context, cleanup func()) {
	t.Helper()
	skipGolden(t)
	ctx = context.Background()
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	_, err := braintrust.New(tp, braintrust.WithProject("golden-go-adk"), braintrust.WithBlockingLogin(true))
	if err != nil {
		t.Fatalf("braintrust.New: %v", err)
	}
	tracer := otel.Tracer("golden")
	ctx, span := tracer.Start(ctx, spanName)
	cleanup = func() {
		span.End()
		_ = tp.Shutdown(context.Background())
	}
	return ctx, cleanup
}

// newAgentAndRunner creates Braintrust callbacks, an LLM agent, in-memory session, and runner.
func newAgentAndRunner(ctx context.Context, t *testing.T, sessionID string, cfg llmagent.Config) *runner.Runner {
	t.Helper()

	llm, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{APIKey: os.Getenv("GOOGLE_API_KEY")})
	if err != nil {
		t.Fatalf("gemini.NewModel: %v", err)
	}
	cfg.Model = llm

	cb := traceadk.NewCallbacks()
	traceadk.AddLLMAgentCallbacks(&cfg, cb)
	a, err := llmagent.New(cfg)
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}

	svc := session.InMemoryService()
	_, err = svc.Create(ctx, &session.CreateRequest{AppName: appName, UserID: userID, SessionID: sessionID})
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}

	r, err := runner.New(runner.Config{AppName: appName, Agent: a, SessionService: svc})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	return r
}

// runAndCollectFinal runs the agent with the given message and returns all final response events.
func runAndCollectFinal(ctx context.Context, t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content, runCfg agent.RunConfig) []*session.Event {
	t.Helper()
	var responses []*session.Event
	for ev, err := range r.Run(ctx, userID, sessionID, msg, runCfg) { //nolint:revive // range over Seq2
		if err != nil {
			t.Fatalf("runner.Run: %v", err)
		}
		if ev.IsFinalResponse() {
			responses = append(responses, ev)
		}
	}
	return responses
}

// runAndCollectStreamedText runs the agent and concatenates all content parts (for streaming tests).
func runAndCollectStreamedText(ctx context.Context, t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content, runCfg agent.RunConfig) string {
	t.Helper()
	var fullText strings.Builder
	for ev, err := range r.Run(ctx, userID, sessionID, msg, runCfg) {
		if err != nil {
			t.Fatalf("runner.Run: %v", err)
		}
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
	for ev, err := range r.Run(ctx, userID, sessionID, msg, runCfg) {
		if err != nil {
			t.Fatalf("runner.Run: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

// assertFinalResponse asserts there is at least one final response with non-empty text and returns it.
func assertFinalResponse(t *testing.T, responses []*session.Event) string {
	t.Helper()
	if len(responses) == 0 {
		t.Fatal("expected at least one final response")
	}
	content := responses[0].Content
	if content == nil || len(content.Parts) == 0 {
		t.Fatal("expected response content with parts")
	}
	text := content.Parts[0].Text
	if text == "" {
		t.Fatal("expected non-empty text in response")
	}
	return text
}

// Basic completion with a simple agent, one user message, and collected final response.
func TestBasicCompletion(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_basic_completion")
	defer cleanup()

	r := newAgentAndRunner(ctx, t, sessionIDBasic, llmagent.Config{
		Name:        "basic_agent",
		Instruction: "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 100,
		},
	})

	userMsg := genai.NewContentFromText("What is the capital of France?", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sessionIDBasic, userMsg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)
}

// Two messages in same session, collect second response.
func TestMultiTurn(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_multi_turn")
	defer cleanup()

	sid := "session-multi-turn"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "conversation_agent",
		Instruction:           "You are a helpful assistant with good memory.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	// First message
	msg1 := genai.NewContentFromText("Hi, my name is Alice.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg1, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response 1: %s", text)

	// Second message
	msg2 := genai.NewContentFromText("What did I just tell you my name was?", genai.RoleUser)
	responses = runAndCollectFinal(ctx, t, r, sid, msg2, agent.RunConfig{})
	text = assertFinalResponse(t, responses)
	t.Logf("Response 2: %s", text)
}

// Pirate instruction, ask about weather.
func TestSystemPrompt(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_system_prompt")
	defer cleanup()

	sid := "session-pirate"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "pirate_agent",
		Instruction:           "You are a pirate. Always respond in pirate speak.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 150},
	})

	msg := genai.NewContentFromText("Tell me about the weather.", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)
}

// Collect all text from stream (Count from 1 to 10 slowly).
func TestStreaming(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_streaming")
	defer cleanup()

	sid := "session-streaming"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "counting_agent",
		Instruction:           "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 200},
	})

	msg := genai.NewContentFromText("Count from 1 to 10 slowly.", genai.RoleUser)
	runCfg := agent.RunConfig{StreamingMode: agent.StreamingMode("streaming")}
	fullText := runAndCollectStreamedText(ctx, t, r, sid, msg, runCfg)
	t.Logf("Streamed: %s", fullText)
	if fullText == "" {
		t.Fatal("expected some streamed text")
	}
}

// Send image + text, skip if fixture missing.
func TestImageInput(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	if path == "" {
		t.Skip("fixtures/test-image.png not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}

	ctx, cleanup := setupGoldenTest(t, "test_image_input")
	defer cleanup()

	sid := "session-vision"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// PDF + question, skip if fixture missing.
func TestDocumentInput(t *testing.T) {
	path := fixturePath(t, "test-document.pdf")
	if path == "" {
		t.Skip("fixtures/test-document.pdf not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}

	ctx, cleanup := setupGoldenTest(t, "test_document_input")
	defer cleanup()

	sid := "session-document"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// Three configs, unique session each.
func TestTemperatureVariations(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_temperature_variations")
	defer cleanup()

	configs := []struct {
		temp float32
		topP float32
	}{
		{0.0, 1.0},
		{1.0, 0.9},
		{0.7, 0.95},
	}
	for i, cfg := range configs {
		name := fmt.Sprintf("agent_temp_%v", cfg.temp)
		switch cfg.temp {
		case 0.0:
			name = "agent_temp_0_0"
		case 1.0:
			name = "agent_temp_1_0"
		}
		sid := fmt.Sprintf("session-temp-%v-%d", cfg.temp, i)
		r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
			Name:        name,
			Instruction: "You are a creative storyteller.",
			GenerateContentConfig: &genai.GenerateContentConfig{
				Temperature:     &cfg.temp,
				TopP:            &cfg.topP,
				MaxOutputTokens: 50,
			},
		})

		msg := genai.NewContentFromText("Say something creative.", genai.RoleUser)
		fullText := runAndCollectStreamedText(ctx, t, r, sid, msg, agent.RunConfig{})
		t.Logf("Config temp=%v top_p=%v: %s", cfg.temp, cfg.topP, fullText)
	}
}

// Stop_sequences in config.
func TestStopSequences(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_stop_sequences")
	defer cleanup()

	sid := "session-stop"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// Labels in generate_content_config.
func TestMetadata(t *testing.T) {
	t.Skip("labels parameter is not supported in Gemini API")

	ctx, cleanup := setupGoldenTest(t, "test_metadata")
	defer cleanup()

	sid := "session-metadata"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// Long prompt, ask how many 'fox'.
func TestLongContext(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_long_context")
	defer cleanup()

	sid := "session-long"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "analysis_agent",
		Instruction:           "You are a text analysis assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 100},
	})

	longText := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	msg := genai.NewContentFromText(fmt.Sprintf("Here is a long text:\n\n%s\n\nHow many times does the word 'fox' appear?", longText), genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)
}

// Text + image + text parts; skip if no image.
func TestMixedContent(t *testing.T) {
	path := fixturePath(t, "test-image.png")
	if path == "" {
		t.Skip("fixtures/test-image.png not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}

	ctx, cleanup := setupGoldenTest(t, "test_mixed_content")
	defer cleanup()

	sid := "session-mixed"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// Two messages (haiku then "Here is a haiku:"), collect second.
func TestPrefill(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_prefill")
	defer cleanup()

	sid := "session-prefill"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}

// Max_output_tokens=5.
func TestShortMaxTokens(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_short_max_tokens")
	defer cleanup()

	sid := "session-brief"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "brief_agent",
		Instruction:           "You are a helpful assistant.",
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 5},
	})

	msg := genai.NewContentFromText("What is AI?", genai.RoleUser)
	responses := runAndCollectFinal(ctx, t, r, sid, msg, agent.RunConfig{})
	text := assertFinalResponse(t, responses)
	t.Logf("Response: %s", text)
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

// Get_weather tool, ask Paris weather.
func TestToolUse(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_tool_use")
	defer cleanup()

	weatherTool, err := functiontool.New(
		functiontool.Config{
			Name:        "get_weather",
			Description: "Get the current weather for a location. Args: city_and_state (e.g. San Francisco, CA), unit (celsius or fahrenheit).",
		},
		getWeather,
	)
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}

	sid := "session-weather"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "weather_agent",
		Instruction:           "You are a helpful weather assistant. Use the get_weather tool to answer questions.",
		Tools:                 []tool.Tool{weatherTool},
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 500},
	})

	msg := genai.NewContentFromText("What is the weather like in Paris, France?", genai.RoleUser)

	// Collect ALL events to see function calls (they appear in non-final events)
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

	if !hasFunctionCall {
		t.Error("expected at least one function call")
	}
	if finalText == "" {
		t.Error("expected final response text")
	}
}

// calculateArgs and calculate implement the math tool for TestToolUseWithResult.
type calculateArgs struct {
	Operation string  `json:"operation" jsonschema:"add, subtract, multiply, divide"`
	A         float64 `json:"a" jsonschema:"First number"`
	B         float64 `json:"b" jsonschema:"Second number"`
}

func calculate(_ tool.Context, args calculateArgs) (float64, error) {
	switch args.Operation {
	case "add":
		return args.A + args.B, nil
	case "subtract":
		return args.A - args.B, nil
	case "multiply":
		return args.A * args.B, nil
	case "divide":
		if args.B == 0 {
			return 0, errors.New("division by zero")
		}
		return args.A / args.B, nil
	default:
		return 0, errors.New("invalid operation")
	}
}

// Calculate tool, 127*49, log first response.
func TestToolUseWithResult(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_tool_use_with_result")
	defer cleanup()

	calcTool, err := functiontool.New(
		functiontool.Config{
			Name:        "calculate",
			Description: "Perform a mathematical calculation. Operation: add, subtract, multiply, divide. a, b: numbers.",
		},
		calculate,
	)
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}

	sid := "session-calculator"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
		Name:                  "math_agent",
		Instruction:           "You are a helpful math assistant. Use the calculate tool to perform calculations.",
		Tools:                 []tool.Tool{calcTool},
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: 500},
	})

	msg := genai.NewContentFromText("What is 127 multiplied by 49?", genai.RoleUser)
	t.Log("First response:")
	for _, ev := range runAndCollectAll(ctx, t, r, sid, msg, agent.RunConfig{}) {
		if ev.IsFinalResponse() && ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.FunctionCall != nil {
					t.Logf("Tool called: %s Input: %v", p.FunctionCall.Name, p.FunctionCall.Args)
				}
			}
		}
	}
}

// Reasoning agent, two messages (pattern then follow-up).
// Uses ThinkingConfig in GenerateContentConfig when supported by the model.
func TestReasoning(t *testing.T) {
	ctx, cleanup := setupGoldenTest(t, "test_reasoning")
	defer cleanup()

	thinkingBudget := int32(1024)
	sid := "session-reasoning"
	r := newAgentAndRunner(ctx, t, sid, llmagent.Config{
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
}
