// Auto-instrumentation example using Orchestrion.
//
// This example demonstrates automatic tracing injection for all supported LLM providers:
//   - OpenAI (openai-go official SDK)
//   - Anthropic
//   - sashabaranov/go-openai
//   - LangChainGo (OpenAI provider)
//   - ADK (with Gemini model)
//   - Google GenAI (direct client)
//   - CloudWeGo Eino
//   - Firebase Genkit
//
// Note: NO manual middleware or callbacks are added to any client.
// When built with `orchestrion go build`, tracing middleware is injected at compile time.
//
// To run:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	export OPENAI_API_KEY="your-openai-key"
//	export ANTHROPIC_API_KEY="your-anthropic-key"
//	export GOOGLE_API_KEY="your-google-key"
//	orchestrion go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"github.com/openai/openai-go"
	sashabaranov "github.com/sashabaranov/go-openai"
	"github.com/tmc/langchaingo/llms"
	langchainopenai "github.com/tmc/langchaingo/llms/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
	// Validate environment
	for _, key := range []string{"BRAINTRUST_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY"} {
		if os.Getenv(key) == "" {
			log.Fatalf("Missing required environment variable: %s", key)
		}
	}

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Create a root span
	tracer := otel.Tracer("autoinstrumentation-example")
	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/autoinstrumentation/main.go")
	defer rootSpan.End()

	fmt.Println("=== Orchestrion Auto-Instrumentation Example ===")
	fmt.Println()

	// 1. OpenAI (openai-go) - NO middleware added
	fmt.Println("1. OpenAI (openai-go)...")
	runOpenAI(ctx)

	// 2. Anthropic - NO middleware added
	fmt.Println("2. Anthropic...")
	runAnthropic(ctx)

	// 3. sashabaranov/go-openai - NO HTTPClient wrapping
	fmt.Println("3. sashabaranov/go-openai...")
	runSashabaranov(ctx)

	// 4. LangChainGo - NO callback added
	fmt.Println("4. LangChainGo (OpenAI)...")
	runLangChainGo(ctx)

	// 5. ADK - NO callbacks added
	fmt.Println("5. ADK (with Gemini model)...")
	runADK(ctx)

	// 6. Google GenAI (direct) - NO HTTPClient wrapping
	fmt.Println("6. Google GenAI (direct)...")
	runGenai(ctx)

	// 7. CloudWeGo Eino - NO callback handler added
	fmt.Println("7. CloudWeGo Eino...")
	runEino(ctx)

	// 8. Firebase Genkit - NO middleware added
	fmt.Println("8. Firebase Genkit...")
	runGenkit(ctx)

	fmt.Println("\n=== All providers tested ===")
	fmt.Println("If tracing worked, you should see LLM spans for each provider in Braintrust.")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}

func runOpenAI(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "openai")
	defer span.End()

	// NO middleware - Orchestrion injects it
	client := openai.NewClient()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say 'Hello from OpenAI' in exactly those words."),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		log.Printf("   OpenAI error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Choices[0].Message.Content)
}

func runAnthropic(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "anthropic")
	defer span.End()

	// NO middleware - Orchestrion injects it
	client := anthropic.NewClient()

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.ModelClaudeHaiku4_5,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say 'Hello from Anthropic' in exactly those words.")),
		},
		MaxTokens: 100,
	})
	if err != nil {
		log.Printf("   Anthropic error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", message.Content[0].Text)
}

func runSashabaranov(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "sashabaranov")
	defer span.End()

	// NO HTTPClient wrapping - Orchestrion injects it
	config := sashabaranov.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	client := sashabaranov.NewClientWithConfig(config)

	resp, err := client.CreateChatCompletion(ctx, sashabaranov.ChatCompletionRequest{
		Model: sashabaranov.GPT4oMini,
		Messages: []sashabaranov.ChatCompletionMessage{
			{Role: sashabaranov.ChatMessageRoleUser, Content: "Say 'Hello from sashabaranov' in exactly those words."},
		},
	})
	if err != nil {
		log.Printf("   sashabaranov error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Choices[0].Message.Content)
}

func runLangChainGo(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "langchaingo")
	defer span.End()

	// NO callback - Orchestrion injects it
	llm, err := langchainopenai.New()
	if err != nil {
		log.Printf("   LangChainGo error: %v", err)
		return
	}

	resp, err := llm.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Say 'Hello from LangChainGo' in exactly those words."),
	})
	if err != nil {
		log.Printf("   LangChainGo error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Choices[0].Content)
}

func runADK(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "adk")
	defer span.End()

	// NO callbacks - Orchestrion injects them
	model, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Printf("   ADK error: %v", err)
		return
	}

	assistant, err := llmagent.New(llmagent.Config{
		Name:        "adk_agent",
		Model:       model,
		Instruction: "You are a helpful assistant.",
	})
	if err != nil {
		log.Printf("   ADK error: %v", err)
		return
	}

	// Create session service and initialize the session
	svc := session.InMemoryService()
	_, err = svc.Create(ctx, &session.CreateRequest{
		AppName:   "adk-example",
		UserID:    "user",
		SessionID: "session",
	})
	if err != nil {
		log.Printf("   ADK error: %v", err)
		return
	}

	runner, err := runner.New(runner.Config{
		AppName:        "adk-example",
		Agent:          assistant,
		SessionService: svc,
	})
	if err != nil {
		log.Printf("   ADK error: %v", err)
		return
	}

	msg := genai.NewContentFromText("Say 'Hello from ADK' in exactly those words.", genai.RoleUser)
	for ev, err := range runner.Run(ctx, "user", "session", msg, agent.RunConfig{}) {
		if err != nil {
			log.Printf("   ADK error: %v", err)
			return
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					fmt.Printf("   Response: %s\n", p.Text)
				}
			}
		}
	}
}

func runGenai(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "genai")
	defer span.End()

	// NO HTTPClient wrapping - Orchestrion injects it
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Printf("   GenAI error: %v", err)
		return
	}

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash",
		genai.Text("Say 'Hello from GenAI' in exactly those words."), nil)
	if err != nil {
		log.Printf("   GenAI error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Text())
}

func runEino(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "eino")
	defer span.End()

	// NO handler added - Orchestrion injects it
	callbacks.AppendGlobalHandlers()

	model, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		Model:  "gpt-4o-mini",
		APIKey: os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		log.Printf("   Eino error: %v", err)
		return
	}

	resp, err := model.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "Say 'Hello from Eino' in exactly those words."},
	})
	if err != nil {
		log.Printf("   Eino error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Content)
}

func runGenkit(ctx context.Context) {
	ctx, span := otel.Tracer("autoinstrumentation-example").Start(ctx, "genkit")
	defer span.End()

	g := genkit.Init(ctx,
		genkit.WithPlugins(&googlegenai.GoogleAI{}),
		genkit.WithDefaultModel("googleai/gemini-2.5-flash"),
	)

	// NO middleware - Orchestrion injects it
	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("Say 'Hello from Genkit' in exactly those words."))
	if err != nil {
		log.Printf("   Genkit error: %v", err)
		return
	}
	fmt.Printf("   Response: %s\n", resp.Text())
}
