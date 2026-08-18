package main

import (
	"context"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/openai/openai-go"
	openaioption "github.com/openai/openai-go/option"
	openaiv2 "github.com/openai/openai-go/v2"
	openaiv2option "github.com/openai/openai-go/v2/option"
	sashabaranovopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
	langchainopenai "github.com/tmc/langchaingo/llms/openai"
	"go.opentelemetry.io/otel"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// TestOpenAI verifies that orchestrion auto-injects the Braintrust middleware
// for the official OpenAI Go SDK. This test creates an OpenAI client WITHOUT
// manually adding middleware. If orchestrion is working, it will inject
// the middleware at compile time, and spans will be created.
func TestOpenAI(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create OpenAI client WITHOUT middleware - orchestrion should inject it
	client := openai.NewClient(
		openaioption.WithAPIKey("dummy-key-for-vcr"),
		openaioption.WithHTTPClient(httpClient),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)

	// Make a chat completion request
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hello"),
		},
	})
	require.NoError(t, err)

	// Verify spans were created - this proves middleware was injected
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not inject middleware for OpenAI")

	t.Logf("SUCCESS: %d span(s) created for OpenAI", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	// Verify the span is from OpenAI middleware
	found := false
	for _, span := range spans {
		if span.Name() == "Chat Completion" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected Chat Completion span")
}

// TestOpenAIV2 verifies that orchestrion auto-injects the Braintrust middleware
// for the official OpenAI Go SDK v2. This test creates an OpenAI v2 client WITHOUT
// manually adding middleware. If orchestrion is working, it will inject
// the middleware at compile time, and spans will be created.
func TestOpenAIV2(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create OpenAI v2 client WITHOUT middleware - orchestrion should inject it
	client := openaiv2.NewClient(
		openaiv2option.WithAPIKey("dummy-key-for-vcr"),
		openaiv2option.WithHTTPClient(httpClient),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)

	// Make a chat completion request
	_, err := client.Chat.Completions.New(context.Background(), openaiv2.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openaiv2.ChatCompletionMessageParamUnion{
			openaiv2.UserMessage("Say hello"),
		},
	})
	require.NoError(t, err)

	// Verify spans were created - this proves middleware was injected
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not inject middleware for OpenAI v2")

	t.Logf("SUCCESS: %d span(s) created for OpenAI v2", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	// Verify the span is from OpenAI middleware
	found := false
	for _, span := range spans {
		if span.Name() == "Chat Completion" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected Chat Completion span")
}

// TestAnthropic verifies that orchestrion auto-injects the Braintrust middleware
// for the Anthropic Go SDK. This test creates an Anthropic client WITHOUT
// manually adding middleware. If orchestrion is working, it will inject
// the middleware at compile time, and spans will be created.
func TestAnthropic(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create Anthropic client WITHOUT middleware - orchestrion should inject it
	client := anthropic.NewClient(
		anthropicoption.WithAPIKey("dummy-key-for-vcr"),
		anthropicoption.WithHTTPClient(httpClient),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)

	// Make a messages request
	_, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say hello")),
		},
	})
	require.NoError(t, err)

	// Verify spans were created - this proves middleware was injected
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not inject middleware for Anthropic")

	t.Logf("SUCCESS: %d span(s) created for Anthropic", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	// Verify the span is from Anthropic middleware
	found := false
	for _, span := range spans {
		if span.Name() == "anthropic.messages.create" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected anthropic.messages.create span")
}

// TestSashabaranovOpenAI verifies that orchestrion auto-injects the Braintrust
// tracing for the sashabaranov/go-openai library. This test creates a client
// config WITHOUT manually wrapping the HTTPClient. If orchestrion is working,
// it will wrap the HTTPClient at compile time, and spans will be created.
func TestSashabaranovOpenAI(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create config with VCR HTTPClient - orchestrion should wrap it
	config := sashabaranovopenai.DefaultConfig("dummy-key-for-vcr")
	config.HTTPClient = httpClient
	// NOTE: No traceopenai.WrapClient here! Orchestrion should inject it.

	client := sashabaranovopenai.NewClientWithConfig(config)

	// Make a chat completion request
	_, err := client.CreateChatCompletion(context.Background(), sashabaranovopenai.ChatCompletionRequest{
		Model: sashabaranovopenai.GPT4oMini,
		Messages: []sashabaranovopenai.ChatCompletionMessage{
			{
				Role:    sashabaranovopenai.ChatMessageRoleUser,
				Content: "Say hello",
			},
		},
	})
	require.NoError(t, err)

	// Verify spans were created - this proves HTTPClient was wrapped
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not wrap HTTPClient for sashabaranov/go-openai")

	t.Logf("SUCCESS: %d span(s) created for sashabaranov/go-openai", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	// Verify the span is from our tracing
	found := false
	for _, span := range spans {
		if span.Name() == "Chat Completion" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected Chat Completion span")
}

// TestGenAI verifies that orchestrion auto-injects the Braintrust
// tracing for the Google GenAI library. This test creates a client
// config WITHOUT manually wrapping the HTTPClient. If orchestrion is working,
// it will wrap the HTTPClient at compile time, and spans will be created.
func TestGenAI(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create GenAI client with VCR HTTPClient - orchestrion should wrap it
	// NOTE: No tracegenai.WrapClient here! Orchestrion should inject it.
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: httpClient,
		APIKey:     "dummy-key-for-vcr",
		Backend:    genai.BackendGeminiAPI,
	})
	require.NoError(t, err)

	// Make a generateContent request
	resp, err := client.Models.GenerateContent(
		context.Background(),
		"gemini-2.0-flash-exp",
		genai.Text("Say hello"),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify spans were created - this proves HTTPClient was wrapped
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not wrap HTTPClient for GenAI")

	t.Logf("SUCCESS: %d span(s) created for GenAI", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	// Verify the span is from our tracing
	found := false
	for _, span := range spans {
		if span.Name() == "generate_content" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected generate_content span")
}

// TestLangChainGo verifies that orchestrion auto-injects the Braintrust callback
// for LangChainGo's OpenAI client. This test creates the client WITHOUT manually
// adding a callback. If orchestrion is working, it will inject the callback at
// compile time and spans will be created.
func TestLangChainGo(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	// Create LangChainGo OpenAI client WITHOUT callback - orchestrion should inject it.
	llm, err := langchainopenai.New(
		langchainopenai.WithToken("dummy-key-for-vcr"),
		langchainopenai.WithModel("gpt-4o-mini"),
		langchainopenai.WithHTTPClient(httpClient),
		// NOTE: No WithCallback here! Orchestrion should inject it.
	)
	require.NoError(t, err)

	resp, err := llm.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Say hello"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify spans were created - this proves the callback was injected.
	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not inject callback for LangChainGo")

	t.Logf("SUCCESS: %d span(s) created for LangChainGo", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	found := false
	for _, span := range spans {
		if span.Name() == "langchain.llm.generate_content" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected langchain.llm.generate_content span")
}

// TestGenkit verifies that orchestrion auto-injects the Braintrust middleware
// into genkit.Generate calls. The test initializes Genkit with the compat_oai
// OpenAI plugin but does NOT pass ai.WithMiddleware — if orchestrion is
// working, it appends tracegenkit.NewMiddleware() as a GenerateOption at
// compile time and a genkit.generate span is emitted.
func TestGenkit(t *testing.T) {
	exporter := setupOtel(t)

	httpClient := vcr.NewHTTPClient(t)

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(&compatopenai.OpenAI{
			APIKey: "dummy-key-for-vcr",
			Opts: []openaioption.RequestOption{
				openaioption.WithHTTPClient(httpClient),
			},
		}),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)

	_, err := genkit.Generate(ctx, g,
		ai.WithPrompt("Say hello"),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)
	require.NoError(t, err)

	spans := exporter.Flush()
	require.NotEmpty(t, spans, "No spans created - orchestrion did not inject middleware for Genkit")

	t.Logf("SUCCESS: %d span(s) created for Genkit", len(spans))
	for _, span := range spans {
		t.Logf("  - %s", span.Name())
	}

	found := false
	for _, span := range spans {
		if span.Name() == "genkit.generate" {
			found = true
			break
		}
	}
	require.True(t, found, "Expected genkit.generate span")
}

// TestGenkitTool verifies that orchestrion replaces Genkit tool definitions so
// the actual handler execution is traced without a manual wrapper.
func TestGenkitTool(t *testing.T) {
	exporter := setupOtel(t)
	g := genkit.Init(context.Background())
	type input struct {
		Value string `json:"value"`
	}
	tool := genkit.DefineTool(g, "echo_tool", "Echo a value",
		func(_ *ai.ToolContext, in input) (string, error) {
			return in.Value, nil
		},
	)

	output, err := tool.RunRaw(context.Background(), map[string]any{"value": "hello"})
	require.NoError(t, err)
	require.Equal(t, "hello", output)

	found := false
	for _, span := range exporter.Flush() {
		if span.Name() == "echo_tool" && span.HasAttr("braintrust.span_attributes") {
			span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "tool"})
			found = true
			break
		}
	}
	require.True(t, found, "orchestrion did not replace genkit.DefineTool")
}

// TestMultipleIntegrations verifies that multiple Braintrust contrib integrations
// can be enabled together with orchestrion and all produce spans in the same trace.
func TestMultipleIntegrations(t *testing.T) {
	exporter := setupOtel(t)
	httpClient := vcr.NewHTTPClient(t)

	ctx, rootSpan := otel.Tracer("orchestrion-test").Start(context.Background(), "multi-integration-root")

	openAIClient := openai.NewClient(
		openaioption.WithAPIKey("dummy-key-for-vcr"),
		openaioption.WithHTTPClient(httpClient),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)
	_, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hello"),
		},
	})
	require.NoError(t, err)

	anthropicClient := anthropic.NewClient(
		anthropicoption.WithAPIKey("dummy-key-for-vcr"),
		anthropicoption.WithHTTPClient(httpClient),
		// NOTE: No WithMiddleware here! Orchestrion should inject it.
	)
	_, err = anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say hello")),
		},
	})
	require.NoError(t, err)

	genAIClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		HTTPClient: httpClient,
		APIKey:     "dummy-key-for-vcr",
		Backend:    genai.BackendGeminiAPI,
	})
	require.NoError(t, err)
	resp, err := genAIClient.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Say hello"),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	langChainClient, err := langchainopenai.New(
		langchainopenai.WithToken("dummy-key-for-vcr"),
		langchainopenai.WithModel("gpt-4o-mini"),
		langchainopenai.WithHTTPClient(httpClient),
		// NOTE: No WithCallback here! Orchestrion should inject it.
	)
	require.NoError(t, err)
	resp2, err := langChainClient.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Say hello"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp2)

	rootSpan.End()

	spans := exporter.Flush()
	require.Len(t, spans, 5, "expected root span plus one span from each integration")

	var root oteltest.Span
	foundNames := map[string]bool{}
	for _, span := range spans {
		t.Logf("SPAN: %s", span.Name())
		if span.Name() == "multi-integration-root" {
			root = span
			continue
		}
		foundNames[span.Name()] = true
	}

	require.Equal(t, "multi-integration-root", root.Name())
	for _, name := range []string{"Chat Completion", "anthropic.messages.create", "generate_content", "langchain.llm.generate_content"} {
		require.True(t, foundNames[name], "missing expected span %q", name)
	}

	for _, span := range spans {
		if span.Name() == "multi-integration-root" {
			continue
		}
		require.Equal(t, root.Stub.SpanContext.TraceID(), span.Stub.SpanContext.TraceID(), "span %q should share the root trace", span.Name())
		require.Equal(t, root.Stub.SpanContext.SpanID(), span.Stub.Parent.SpanID(), "span %q should be a child of the root span", span.Name())
	}
}

// setupOtel sets up OpenTelemetry with an in-memory exporter for testing
func setupOtel(t *testing.T) *oteltest.Exporter {
	t.Helper()

	tp, exporter := oteltest.Setup(t)
	originalTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(originalTP)
	})

	return exporter
}
