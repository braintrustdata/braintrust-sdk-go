// Auto-instrumentation example using Orchestrion.
//
// This example demonstrates automatic tracing injection for all supported LLM providers:
//   - OpenAI (openai-go official SDK)
//   - Anthropic
//   - sashabaranov/go-openai
//   - LangChainGo (OpenAI provider)
//   - ADK (with Gemini model)
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

	// 6. OpenAI embeddings - NO middleware added
	fmt.Println("6. OpenAI embeddings...")
	runOpenAIEmbeddings(ctx)

	// 7. sashabaranov embeddings - NO HTTPClient wrapping
	fmt.Println("7. sashabaranov embeddings...")
	runSashabaranovEmbeddings(ctx)

	// 8. Gemini embeddings - NO HTTPClient wrapping
	fmt.Println("8. Gemini embeddings...")
	runGeminiEmbeddings(ctx)

	fmt.Println("\n=== All providers tested ===")
	fmt.Println("If tracing worked, you should see LLM spans for each provider in Braintrust.")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}

func runOpenAI(ctx context.Context) {
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
	// NO middleware - Orchestrion injects it
	client := anthropic.NewClient()

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.ModelClaude3_5HaikuLatest,
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

func runOpenAIEmbeddings(ctx context.Context) {
	// NO middleware - Orchestrion injects it
	client := openai.NewClient()

	resp, err := client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("The quick brown fox jumps over the lazy dog"),
		},
		Model: openai.EmbeddingModelTextEmbedding3Small,
	})
	if err != nil {
		log.Printf("   OpenAI embeddings error: %v", err)
		return
	}
	fmt.Printf("   Response: %d-dim embedding\n", len(resp.Data[0].Embedding))
}

func runSashabaranovEmbeddings(ctx context.Context) {
	// NO HTTPClient wrapping - Orchestrion injects it
	config := sashabaranov.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	client := sashabaranov.NewClientWithConfig(config)

	resp, err := client.CreateEmbeddings(ctx, sashabaranov.EmbeddingRequest{
		Model: sashabaranov.SmallEmbedding3,
		Input: "The quick brown fox jumps over the lazy dog",
	})
	if err != nil {
		log.Printf("   sashabaranov embeddings error: %v", err)
		return
	}
	fmt.Printf("   Response: %d-dim embedding\n", len(resp.Data[0].Embedding))
}

func runGeminiEmbeddings(ctx context.Context) {
	// NO HTTPClient wrapping - Orchestrion injects it via genai.NewClient wrap.
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  os.Getenv("GOOGLE_API_KEY"),
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Printf("   Gemini embeddings error: %v", err)
		return
	}
	resp, err := client.Models.EmbedContent(
		ctx,
		"gemini-embedding-001",
		genai.Text("The quick brown fox jumps over the lazy dog"),
		nil,
	)
	if err != nil {
		log.Printf("   Gemini embeddings error: %v", err)
		return
	}
	fmt.Printf("   Response: %d-dim embedding\n", len(resp.Embeddings[0].Values))
}
