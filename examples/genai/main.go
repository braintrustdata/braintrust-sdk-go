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

func geminiAPIKey() string {
	if apiKey := os.Getenv("GOOGLE_API_KEY"); apiKey != "" {
		return apiKey
	}
	return os.Getenv("GEMINI_API_KEY")
}

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

	// Create Gemini client with Braintrust tracing
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: tracegenai.Client(), // Add tracing via custom HTTP client
		APIKey:     geminiAPIKey(),
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

	// Make a simple generateContent request with Gemini thinking enabled.
	thinkingBudget := int32(256)
	resp, err := client.Models.GenerateContent(
		ctx,
		"gemini-2.5-flash",
		genai.Text("Look at this sequence: 2, 6, 12, 20, 30. What is the next number? Answer briefly."),
		&genai.GenerateContentConfig{
			ThinkingConfig: &genai.ThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  &thinkingBudget,
				ThinkingLevel:   genai.ThinkingLevelMedium,
			},
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", resp.Text())
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
