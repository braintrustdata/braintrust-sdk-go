// This example demonstrates basic Google Gemini tracing with Braintrust.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func main() {
	fmt.Println("Braintrust Google Gemini Basic Example")

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

	// Create Gemini client with Braintrust tracing
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: tracegenai.Client(), // Add tracing via custom HTTP client
		APIKey:     os.Getenv("GOOGLE_API_KEY"),
		Backend:    genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Get a tracer instance
	tracer := otel.Tracer("genai-example")

	// Create a parent span to wrap the Gemini call
	ctx, span := tracer.Start(context.Background(), "examples/genai/main.go")
	defer span.End()

	// Make a simple generateContent request
	resp, err := client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("What is the capital of France?"),
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", resp.Text())
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
