// Package main demonstrates manual attachment usage in Braintrust traces.
//
// This example shows how to manually create and log attachments using the
// attachment package. Most users won't need to do this manually, as the
// instrumentation middleware (traceopenai, traceanthropic) automatically
// handles attachment conversion.
//
// To run this example:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	go run main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/trace/attachment"
)

func main() {
	// Create a tracer provider and add Braintrust tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Failed to initialize Braintrust: %v", err)
	}

	tracer := otel.Tracer("attachments-example")
	ctx := context.Background()

	// Create a parent span to wrap all examples
	ctx, span := tracer.Start(ctx, "examples/attachments/main.go")

	// Example 1: Create attachment from local file
	fmt.Println("Example 1: FromFile")
	exampleFromFile(ctx, tracer)

	// Example 2: Create attachment from io.Reader
	fmt.Println("\nExample 2: FromReader")
	exampleFromReader(ctx, tracer)

	// Example 3: Create attachment from bytes
	fmt.Println("\nExample 3: FromBytes")
	exampleFromBytes(ctx, tracer)

	// Example 4: Create attachment from URL
	fmt.Println("\nExample 4: FromURL")
	exampleFromURL(ctx, tracer)

	// Example 5: Use attachment in manual span logging
	fmt.Println("\nExample 5: Manual span with attachment")
	exampleManualSpan(ctx, tracer)

	fmt.Println("\n✓ All examples completed successfully!")

	// End the main span
	span.End()

	// Force flush all spans to ensure they're sent to Braintrust
	if err := tp.ForceFlush(context.Background()); err != nil {
		log.Printf("Failed to flush tracer provider: %v", err)
	}

	// Get a link to the span in Braintrust
	fmt.Printf("\n🔗 View in Braintrust: %s\n", bt.Permalink(span))
}

func exampleFromFile(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "attachment.from_file")
	defer span.End()

	// Create a temporary test image
	tmpFile, err := createTestImage()
	if err != nil {
		log.Printf("Failed to create test image: %v", err)
		return
	}
	defer func() {
		_ = os.Remove(tmpFile)
	}()

	// Create attachment from file
	att, err := attachment.FromFile(attachment.ImagePNG, tmpFile)
	if err != nil {
		log.Printf("Failed to create attachment from file: %v", err)
		return
	}

	fmt.Printf("  Created attachment from file\n")

	// Log as JSON attribute
	err = logAttachment(span, att)
	if err != nil {
		log.Printf("Failed to log attachment: %v", err)
	}
}

func exampleFromReader(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "attachment.from_reader")
	defer span.End()

	// Create a temporary test image
	tmpFile, err := createTestImage()
	if err != nil {
		log.Printf("Failed to create test image: %v", err)
		return
	}
	defer func() {
		_ = os.Remove(tmpFile)
	}()

	// Open file as a reader
	file, err := os.Open(tmpFile)
	if err != nil {
		log.Printf("Failed to open file: %v", err)
		return
	}
	defer func() {
		_ = file.Close()
	}()

	// Create attachment from reader
	att := attachment.FromReader(attachment.ImagePNG, file)

	fmt.Printf("  Created attachment from reader\n")

	err = logAttachment(span, att)
	if err != nil {
		log.Printf("Failed to log attachment: %v", err)
	}
}

func exampleFromBytes(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "attachment.from_bytes")
	defer span.End()

	// Create test image data (1x1 PNG)
	imageBytes := getTestImageBytes()

	// Create attachment from bytes
	att := attachment.FromBytes(attachment.ImagePNG, imageBytes)

	fmt.Printf("  Created attachment from %d bytes\n", len(imageBytes))

	err := logAttachment(span, att)
	if err != nil {
		log.Printf("Failed to log attachment: %v", err)
	}
}

func exampleFromURL(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "attachment.from_url")
	defer span.End()

	// Fetch image from URL
	// Using Braintrust's GitHub avatar as a public test image
	// The content type is automatically derived from the HTTP response headers
	url := "https://avatars.githubusercontent.com/u/109710255?s=200&v=4"

	att, err := attachment.FromURL(url)
	if err != nil {
		log.Printf("Failed to fetch URL: %v", err)
		return
	}

	fmt.Printf("  Fetched attachment from URL\n")

	err = logAttachment(span, att)
	if err != nil {
		log.Printf("Failed to log attachment: %v", err)
	}
}

func exampleManualSpan(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "vision.analyze_image")
	defer span.End()

	// Create attachment from test data
	imageBytes := getTestImageBytes()
	att := attachment.FromBytes(attachment.ImagePNG, imageBytes)

	// Get attachment in message format
	attMsg, err := att.Base64Message()
	if err != nil {
		log.Printf("Failed to get attachment message: %v", err)
		return
	}

	// Construct a message with text and image (similar to OpenAI/Anthropic format)
	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "What's in this image? Describe it in detail.",
				},
				attMsg, // Attachment message in correct format
			},
		},
	}

	// Log the messages as input
	messagesJSON, _ := json.Marshal(messages)
	span.SetAttributes(attribute.String("braintrust.input_json", string(messagesJSON)))

	// Simulate output
	output := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "This is a test image showing a simple geometric shape.",
		},
	}

	outputJSON, _ := json.Marshal(output)
	span.SetAttributes(attribute.String("braintrust.output_json", string(outputJSON)))

	// Add metadata
	metadata := map[string]interface{}{
		"model":    "vision-model",
		"provider": "custom",
	}
	metadataJSON, _ := json.Marshal(metadata)
	span.SetAttributes(attribute.String("braintrust.metadata", string(metadataJSON)))

	fmt.Printf("  Created manual span with attachment\n")
	fmt.Printf("  Span name: vision.analyze_image\n")
	fmt.Printf("  Input: text + image attachment\n")
	fmt.Printf("  Output: assistant response\n")
}

// Helper function to log attachment to span
func logAttachment(span oteltrace.Span, att *attachment.Attachment) error {
	// Get attachment in message format
	attMsg, err := att.Base64Message()
	if err != nil {
		return fmt.Errorf("failed to get attachment message: %w", err)
	}

	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Example with attachment",
				},
				attMsg,
			},
		},
	}

	messagesJSON, _ := json.Marshal(messages)
	span.SetAttributes(attribute.String("braintrust.input_json", string(messagesJSON)))
	return nil
}

// createTestImage creates a temporary 10x10 PNG image for testing
func createTestImage() (string, error) {
	tmpFile, err := os.CreateTemp("", "test-image-*.png")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	// Write the test PNG data
	_, err = tmpFile.Write(getTestImageBytes())
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

// getTestImageBytes returns a 10x10 red square PNG (91 bytes)
func getTestImageBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x0a, 0x00, 0x00, 0x00, 0x0a,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x02, 0x50, 0x58, 0xea, 0x00, 0x00, 0x00,
		0x12, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xf8, 0xcf, 0xc0, 0x80,
		0x07, 0x31, 0x8c, 0x4a, 0x63, 0x43, 0x00, 0xb7, 0xca, 0x63, 0x9d, 0xd6,
		0xd5, 0xef, 0x74, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
