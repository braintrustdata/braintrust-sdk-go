// Demonstrates that Gemini streaming calls (streamGenerateContent) are now
// fully instrumented. Before this fix, only non-streaming generateContent
// calls produced spans -- streaming calls silently passed through without
// any tracing.
//
// The example makes one non-streaming call and one streaming call, then
// inspects the captured spans to show that both produce complete trace data
// including output, token metrics, and time_to_first_token.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genai"

	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func main() {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		log.Fatal("GOOGLE_API_KEY is required")
	}

	// Set up an in-memory exporter so we can inspect spans locally.
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter),
	)
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	// Create Gemini client with tracing.
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: tracegenai.Client(tracegenai.WithTracerProvider(tp)),
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// --- Non-streaming call ---
	fmt.Println("=== Non-streaming call ===")
	resp, err := client.Models.GenerateContent(ctx, "gemini-2.0-flash",
		genai.Text("What is 2+2? Answer with just the number."), nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Response: %s\n", resp.Text())

	// --- Streaming call (was previously uninstrumented) ---
	fmt.Println("\n=== Streaming call ===")
	iter := client.Models.GenerateContentStream(ctx, "gemini-2.0-flash",
		genai.Text("Count from 1 to 3, one number per line."), nil)
	fmt.Print("Response: ")
	for chunk, err := range iter {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(chunk.Text())
	}
	fmt.Println()

	// --- Inspect spans ---
	fmt.Println("\n=== Captured spans ===")
	spans := exporter.GetSpans()
	fmt.Printf("Total spans: %d (before this fix, streaming calls produced 0 spans)\n\n", len(spans))

	for i, span := range spans {
		fmt.Printf("Span %d: %s\n", i+1, span.Name)

		metrics := jsonAttr(span, "braintrust.metrics")
		metadata := jsonAttr(span, "braintrust.metadata")

		fmt.Printf("  provider:           %v\n", metadata["provider"])
		fmt.Printf("  model:              %v\n", metadata["model"])
		fmt.Printf("  prompt_tokens:      %v\n", metrics["prompt_tokens"])
		fmt.Printf("  completion_tokens:  %v\n", metrics["completion_tokens"])
		fmt.Printf("  time_to_first_token: %v seconds\n", metrics["time_to_first_token"])

		output := jsonAttr(span, "braintrust.output_json")
		if candidates, ok := output["candidates"].([]any); ok && len(candidates) > 0 {
			if c, ok := candidates[0].(map[string]any); ok {
				if content, ok := c["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
						if p, ok := parts[0].(map[string]any); ok {
							text := fmt.Sprintf("%v", p["text"])
							if len(text) > 80 {
								text = text[:80] + "..."
							}
							fmt.Printf("  output text:        %q\n", text)
						}
					}
				}
			}
		}
		fmt.Println()
	}
}

func jsonAttr(span tracetest.SpanStub, key string) map[string]any {
	for _, attr := range span.Attributes {
		if string(attr.Key) == key {
			var m map[string]any
			if err := json.Unmarshal([]byte(attr.Value.AsString()), &m); err == nil {
				return m
			}
		}
	}
	return nil
}
