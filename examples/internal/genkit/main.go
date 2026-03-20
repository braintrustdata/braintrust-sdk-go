// Firebase Genkit integration example - demonstrates Braintrust tracing with Genkit's ModelMiddleware.
package main

import (
	"context"
	"fmt"
	"log"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracegenkit "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"
)

func main() {
	ctx := context.Background()

	// Initialize braintrust tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx)
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	tracer := otel.Tracer("genkit-examples")
	ctx, rootSpan := tracer.Start(ctx, "examples/internal/genkit/main.go")
	defer rootSpan.End()

	// Initialize Genkit with Google AI plugin
	g := genkit.Init(ctx,
		genkit.WithPlugins(&googlegenai.GoogleAI{}),
		genkit.WithDefaultModel("googleai/gemini-2.0-flash"),
	)

	// Create the Braintrust tracing middleware
	mw := tracegenkit.NewMiddleware(tracegenkit.WithTracerProvider(tp))

	// Example 1: Basic text generation
	fmt.Println("\n=== Example 1: Basic Text Generation ===")
	resp, err := genkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France? Answer in one sentence."),
		ai.WithMiddleware(mw),
	)
	if err != nil {
		log.Fatalf("Generate error: %v", err)
	}
	fmt.Printf("  %s\n", resp.Text())

	// Example 2: Generation with config
	fmt.Println("\n=== Example 2: With Config ===")
	resp, err = genkit.Generate(ctx, g,
		ai.WithPrompt("Explain quantum computing in two sentences."),
		ai.WithConfig(&ai.GenerationCommonConfig{
			Temperature:     0.7,
			MaxOutputTokens: 200,
		}),
		ai.WithMiddleware(mw),
	)
	if err != nil {
		log.Fatalf("Generate error: %v", err)
	}
	fmt.Printf("  %s\n", resp.Text())

	// Example 3: Streaming
	fmt.Println("\n=== Example 3: Streaming ===")
	fmt.Print("  ")
	for sv, err := range genkit.GenerateStream(ctx, g,
		ai.WithPrompt("Count from 1 to 5, one per line."),
		ai.WithMiddleware(mw),
	) {
		if err != nil {
			log.Fatalf("Stream error: %v", err)
		}
		if sv.Done {
			break
		}
		fmt.Print(sv.Chunk.Text())
	}
	fmt.Println()

	fmt.Println("\n=== Tracing Complete ===")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
