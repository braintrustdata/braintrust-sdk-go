// Auto-instrumentation example using Orchestrion.
//
// This example demonstrates automatic tracing injection for all supported LLM providers:
//   - OpenAI (openai-go official SDK)
//   - Anthropic
//   - sashabaranov/go-openai
//   - LangChainGo (OpenAI provider)
//
// Note: NO manual middleware or callbacks are added to any client.
// When built with `orchestrion go build`, tracing middleware is injected at compile time.
//
// To run:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	export OPENAI_API_KEY="your-openai-key"
//	export ANTHROPIC_API_KEY="your-anthropic-key"
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

	"github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
	// Validate environment
	for _, key := range []string{"BRAINTRUST_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
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
