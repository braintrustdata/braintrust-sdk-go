// Example demonstrating basic LangChainGo tracing with Braintrust
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracelangchaingo "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo"
)

func main() {
	// Step 1: Initialize Braintrust tracing with blocking login
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

	// Step 2: Create the Braintrust callback handler
	// Optionally provide model and provider information for richer traces
	handler := tracelangchaingo.NewHandlerWithOptions(tracelangchaingo.HandlerOptions{
		Model:          "gpt-4o-mini",
		Provider:       "openai",
		TracerProvider: tp,
	})

	// Step 3: Create LangChainGo LLM with the callback handler
	llm, err := openai.New(openai.WithCallback(handler))
	if err != nil {
		log.Fatal(err)
	}

	// Step 4: Create a root span to group all operations
	tracer := otel.Tracer("langchaingo-example")
	ctx, rootSpan := tracer.Start(context.Background(), "examples/langchaingo/main.go")

	// Simple completion
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "What is the capital of France?"),
	}
	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Fatal(err)
	}
	if len(resp.Choices) > 0 {
		fmt.Printf("Answer: %s\n\n", resp.Choices[0].Content)
	}

	// Multi-turn conversation
	conversation := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hi, my name is Alice."),
	}
	resp, err = llm.GenerateContent(ctx, conversation)
	if err != nil {
		log.Fatal(err)
	}
	if len(resp.Choices) > 0 {
		firstResponse := resp.Choices[0].Content
		fmt.Printf("AI: %s\n", firstResponse)

		// Continue conversation
		conversation = append(conversation,
			llms.TextParts(llms.ChatMessageTypeAI, firstResponse),
			llms.TextParts(llms.ChatMessageTypeHuman, "What did I tell you my name was?"),
		)
		resp, err = llm.GenerateContent(ctx, conversation)
		if err != nil {
			log.Fatal(err)
		}
		if len(resp.Choices) > 0 {
			fmt.Printf("AI: %s\n\n", resp.Choices[0].Content)
		}
	}

	// End the root span
	rootSpan.End()

	// Print the permalink to view traces
	fmt.Printf("\nView traces: %s\n", bt.Permalink(rootSpan))
}
