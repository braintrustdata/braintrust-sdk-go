// This example demonstrates basic OpenAI tracing with Braintrust.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {
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

	client := openai.NewClient(
		option.WithMiddleware(traceopenai.NewMiddleware()),
	)

	// Get a tracer instance from the global TracerProvider
	tracer := otel.Tracer("openai-example")

	// Create a parent span to wrap the OpenAI call
	ctx, span := tracer.Start(context.Background(), "examples/openai/main.go")
	defer span.End()

	// Make a simple chat completion request
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", resp.Choices[0].Message.Content)
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
