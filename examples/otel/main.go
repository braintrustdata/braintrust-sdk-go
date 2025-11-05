// This example demonstrates adding Braintrust tracing to an existing OpenTelemetry setup.
// It shows how to use braintrust.New() to add Braintrust to an app that already has
// other exporters (console exporter in this case).

package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
	log.Println("🔧 Setting up existing OpenTelemetry infrastructure...")

	// Create a tracer provider with an existing processor / exporter
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	otel.SetTracerProvider(tp)

	// Add Braintrust tracing with blocking login to ensure permalinks work
	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Braintrust: %v", err)
	}

	log.Println("✅ Braintrust tracing enabled successfully")

	tracer := otel.Tracer("otel-enable-demo")
	_, span := tracer.Start(context.Background(), "examples/otel/main.go")
	span.End()

	// Print permalink so user can view the trace in the UI
	fmt.Printf("\n🔗 View trace: %s\n", bt.Permalink(span))
}
