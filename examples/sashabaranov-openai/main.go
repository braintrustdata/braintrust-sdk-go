// This example demonstrates OpenAI tracing with Braintrust using the sashabaranov/go-openai library.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai"
)

func main() {
	// Set up OpenTelemetry tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	// Initialize Braintrust
	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Get API key from environment
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable not set")
	}

	// Create traced HTTP client
	httpClient := traceopenai.Client()

	// Create OpenAI client with traced HTTP client
	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = httpClient
	client := openai.NewClientWithConfig(config)

	// Get a tracer instance from the global TracerProvider
	tracer := otel.Tracer("sashabaranov-openai-example")

	// Create a parent span to wrap the OpenAI call
	ctx, span := tracer.Start(context.Background(), "examples/sashabaranov-openai/main.go")
	defer span.End()

	// Example 1: Simple chat completion
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "What is the capital of France?",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Response: %s\n", resp.Choices[0].Message.Content)

	// Example 2: Streaming chat completion
	stream, err := client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Count from 1 to 5",
			},
		},
		StreamOptions: &openai.StreamOptions{
			IncludeUsage: true, // Include token usage in streaming responses
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = stream.Close()
	}()

	fmt.Print("Streaming response: ")
	for {
		response, err := stream.Recv()
		if err != nil {
			break
		}
		if len(response.Choices) > 0 {
			fmt.Print(response.Choices[0].Delta.Content)
		}
	}
	fmt.Println()

	fmt.Printf("\nView trace: %s\n", bt.Permalink(span))
}
