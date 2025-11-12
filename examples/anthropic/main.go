// This example demonstrates basic Anthropic tracing with Braintrust.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
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

	client := anthropic.NewClient(
		option.WithMiddleware(traceanthropic.NewMiddleware()),
	)

	// Get a tracer instance from the global TracerProvider
	tracer := otel.Tracer("anthropic-example")

	// Create a parent span to wrap the Anthropic call
	ctx, span := tracer.Start(context.Background(), "examples/anthropic/main.go")
	defer span.End()

	// Make a simple message request
	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.ModelClaude3_7SonnetLatest,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is the capital of France?")),
		},
		MaxTokens: 1024,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", message.Content[0].Text)
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
