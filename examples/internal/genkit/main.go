// Firebase Genkit integration example - demonstrates Braintrust tracing with Genkit's ModelMiddleware.
// Covers: basic generation, multi-turn conversation, tool use, streaming, and config options.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracegenkit "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"
)

type WeatherInput struct {
	City string `json:"city"`
}

func main() {
	ctx := context.Background()

	// Initialize braintrust tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx) //nolint:errcheck
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

	mw := tracegenkit.NewMiddleware(
		tracegenkit.WithProvider("google"),
		tracegenkit.WithModel("gemini-2.0-flash"),
	)

	// Define a tool for the model to use
	weatherTool := tracegenkit.DefineTool(g, "get_weather",
		"Get the current weather for a city",
		func(ctx *ai.ToolContext, input WeatherInput) (string, error) {
			weather := map[string]string{
				"paris":    "18C, partly cloudy",
				"tokyo":    "24C, sunny",
				"new york": "12C, rainy",
			}
			if w, ok := weather[input.City]; ok {
				return fmt.Sprintf("Weather in %s: %s", input.City, w), nil
			}
			return fmt.Sprintf("Weather in %s: 20C, clear skies", input.City), nil
		},
	)

	// --- Example 1: Basic text generation ---
	func() {
		ctx, span := tracer.Start(ctx, "basic-text-generation")
		defer span.End()

		fmt.Println("\n=== Example 1: Basic Text Generation ===")
		resp, err := genkit.Generate(ctx, g,
			ai.WithPrompt("What is the capital of France? Answer in one sentence."),
			ai.WithMiddleware(mw),
		)
		if err != nil {
			log.Fatalf("Generate error: %v", err)
		}
		fmt.Printf("  %s\n", resp.Text())
	}()

	// --- Example 2: System prompt + multi-turn conversation ---
	func() {
		ctx, span := tracer.Start(ctx, "multi-turn-conversation")
		defer span.End()

		fmt.Println("\n=== Example 2: Multi-turn Conversation ===")
		resp, err := genkit.Generate(ctx, g,
			ai.WithMessages(
				ai.NewSystemTextMessage("You are a helpful geography tutor. Keep answers brief."),
				ai.NewUserTextMessage("What is the largest country by area?"),
				ai.NewModelTextMessage("Russia is the largest country by area."),
				ai.NewUserTextMessage("And the second largest?"),
			),
			ai.WithMiddleware(mw),
		)
		if err != nil {
			log.Fatalf("Generate error: %v", err)
		}
		fmt.Printf("  %s\n", resp.Text())
	}()

	// --- Example 3: Tool use ---
	func() {
		ctx, span := tracer.Start(ctx, "tool-use")
		defer span.End()

		fmt.Println("\n=== Example 3: Tool Use ===")
		resp, err := genkit.Generate(ctx, g,
			ai.WithPrompt("What's the weather like in Paris and Tokyo? Use the weather tool for each city."),
			ai.WithTools(weatherTool),
			ai.WithMiddleware(mw),
		)
		if err != nil {
			log.Fatalf("Generate error: %v", err)
		}
		fmt.Printf("  %s\n", resp.Text())
	}()

	// --- Example 4: Generation with config options ---
	func() {
		ctx, span := tracer.Start(ctx, "config-options")
		defer span.End()

		fmt.Println("\n=== Example 4: Config Options ===")
		resp, err := genkit.Generate(ctx, g,
			ai.WithPrompt("Write a haiku about programming."),
			ai.WithConfig(&genai.GenerateContentConfig{
				Temperature:     genai.Ptr[float32](0.9),
				MaxOutputTokens: 100,
				TopP:            genai.Ptr[float32](0.95),
			}),
			ai.WithMiddleware(mw),
		)
		if err != nil {
			log.Fatalf("Generate error: %v", err)
		}
		fmt.Printf("  %s\n", resp.Text())
	}()

	// --- Example 5: Streaming ---
	func() {
		ctx, span := tracer.Start(ctx, "streaming")
		defer span.End()

		fmt.Println("\n=== Example 5: Streaming ===")
		fmt.Print("  ")
		for sv, err := range genkit.GenerateStream(ctx, g,
			ai.WithPrompt("Count from 1 to 5, one number per line."),
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
	}()

	// --- Example 6: Structured output (JSON) ---
	func() {
		ctx, span := tracer.Start(ctx, "structured-output")
		defer span.End()
		_ = ctx

		fmt.Println("\n=== Example 6: Structured Output ===")
		resp, err := genkit.Generate(ctx, g,
			ai.WithPrompt("List 3 programming languages and their year of creation. Return JSON with an array of {name, year} objects."),
			ai.WithOutputFormat(ai.OutputFormatJSON),
			ai.WithMiddleware(mw),
		)
		if err != nil {
			log.Fatalf("Generate error: %v", err)
		}
		var parsed any
		if err := json.Unmarshal([]byte(resp.Text()), &parsed); err == nil {
			pretty, _ := json.MarshalIndent(parsed, "  ", "  ")
			fmt.Printf("  %s\n", pretty)
		} else {
			fmt.Printf("  %s\n", resp.Text())
		}
	}()

	// --- Example 7: Embeddings (WrapEmbedder) ---
	// Genkit has no middleware hook for embedders, so embedders are traced by
	// wrapping them with tracegenkit.WrapEmbedder. The wrapper delegates to
	// the underlying embedder and emits a Braintrust llm span per Embed call.
	func() {
		ctx, span := tracer.Start(ctx, "embeddings")
		defer span.End()

		fmt.Println("\n=== Example 7: Embeddings ===")
		embedder := tracegenkit.WrapEmbedder(
			genkit.LookupEmbedder(g, "googleai/gemini-embedding-001"),
			tracegenkit.WithEmbedderModel("gemini-embedding-001"),
			tracegenkit.WithEmbedderProvider("google"),
		)

		resp, err := genkit.Embed(ctx, g,
			ai.WithEmbedder(embedder),
			ai.WithTextDocs(
				"Braintrust is an evaluation platform for LLM apps.",
				"Genkit is an AI framework from Firebase.",
			),
		)
		if err != nil {
			log.Fatalf("Embed error: %v", err)
		}
		fmt.Printf("  %d embeddings, %d dims each\n",
			len(resp.Embeddings), len(resp.Embeddings[0].Embedding))
	}()

	fmt.Println("\n=== Tracing Complete ===")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
