// Example demonstrating CloudWeGo Eino tracing with Braintrust.
//
// This example shows how to register the Braintrust tracing handler with
// the Eino callbacks system to capture LLM spans automatically.
//
// To run:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	export OPENAI_API_KEY="your-openai-key"
//	go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	traceeino "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino"
)

func main() {
	// Step 1: Initialize Braintrust tracing
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Step 2: Register the Braintrust handler globally
	// All Eino ChatModel calls will now be traced automatically.
	handler := traceeino.NewHandler()
	callbacks.AppendGlobalHandlers(handler)

	// Step 3: Create a root span
	tracer := otel.Tracer("eino-example")
	ctx, rootSpan := tracer.Start(context.Background(), "examples/cloudwego/eino/main.go")
	defer rootSpan.End()

	// Step 4: Create an OpenAI ChatModel from eino-ext
	m, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		Model:  "gpt-4o-mini",
		APIKey: os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	// Step 5: Use the model — all calls are automatically traced
	resp, err := m.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is the capital of France?"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", resp.Content)

	handler.Wait() // ensure streaming spans are flushed before exit

	fmt.Printf("\nView traces: %s\n", bt.Permalink(rootSpan))
}
