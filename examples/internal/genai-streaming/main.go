// Demonstrates that Gemini streaming calls (streamGenerateContent) are now
// fully instrumented. Before this fix, only non-streaming generateContent
// calls produced spans -- streaming calls silently passed through without
// any tracing.
//
// The example makes non-streaming, streaming thinking, and streaming image
// calls, then inspects the captured spans to show complete trace data. Streamed
// thought and media parts are accumulated without the SSE parser truncating them.
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

	// --- Streaming thinking call ---
	fmt.Println("\n=== Streaming thinking call ===")
	thinkingBudget := int32(256)
	iter := client.Models.GenerateContentStream(ctx, "gemini-2.5-flash",
		genai.Text("Explain briefly why the sky appears blue."),
		&genai.GenerateContentConfig{
			ThinkingConfig: &genai.ThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  &thinkingBudget,
			},
		})
	fmt.Print("Response: ")
	for chunk, err := range iter {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(chunk.Text())
	}
	fmt.Println()

	// --- Streaming image call (the SSE data line is typically larger than 64 KiB) ---
	fmt.Println("\n=== Streaming image call ===")
	imageIter := client.Models.GenerateContentStream(ctx, "gemini-2.5-flash-image",
		genai.Text("Generate a simple blue square icon."),
		&genai.GenerateContentConfig{ResponseModalities: []string{"TEXT", "IMAGE"}})
	for chunk, err := range imageIter {
		if err != nil {
			log.Fatal(err)
		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil {
					fmt.Printf("Received %d image bytes\n", len(part.InlineData.Data))
				}
			}
		}
	}

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
		if ttft, ok := metrics["time_to_first_token"]; ok {
			fmt.Printf("  time_to_first_token: %v seconds\n", ttft)
		}

		output := jsonAttr(span, "braintrust.output_json")
		if candidates, ok := output["candidates"].([]any); ok && len(candidates) > 0 {
			if c, ok := candidates[0].(map[string]any); ok {
				if content, ok := c["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						for _, rawPart := range parts {
							part, ok := rawPart.(map[string]any)
							if !ok {
								continue
							}
							if inlineData, ok := part["inlineData"].(map[string]any); ok {
								if data, ok := inlineData["data"].(string); ok {
									fmt.Printf("  output image:       %d base64 characters\n", len(data))
								}
								continue
							}
							text, ok := part["text"].(string)
							if !ok {
								continue
							}
							if len(text) > 80 {
								text = text[:80] + "..."
							}
							label := "output text"
							if thought, _ := part["thought"].(bool); thought {
								label = "thought"
							}
							fmt.Printf("  %-19s %q\n", label+":", text)
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
