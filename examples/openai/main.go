// This example demonstrates basic OpenAI tracing with Braintrust.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
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

	// Make a simple Responses API request
	resp, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Model: openai.ChatModelGPT4oMini,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("What is the capital of France?")},
	})
	if err != nil {
		log.Fatal(err)
	}
	switch resp.Status {
	case responses.ResponseStatusCompleted:
		fmt.Printf("Response: %s\n", resp.OutputText())
		fmt.Printf("View trace: %s\n", bt.Permalink(span))
	case responses.ResponseStatusIncomplete:
		fmt.Println("incomplete:", resp.IncompleteDetails.Reason)
		fmt.Printf("Response: %s\n", resp.OutputText())
		fmt.Printf("View trace: %s\n", bt.Permalink(span))
	case responses.ResponseStatusFailed:
		fmt.Println("failed:", resp.Error.Message)
		fmt.Printf("View trace: %s\n", bt.Permalink(span))
	default:
		fmt.Println("status:", resp.Status)
	}
}
