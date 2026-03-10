// Package main demonstrates linking distributed traces using span export.
//
// Service A starts a root span and exports it (like span.export() in JS/Python).
// The exported string is passed to Service B (e.g. via message queue or HTTP header).
// Service B uses ContextWithExportedSpan to create a child span in the same trace.
//
// To run:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	go run examples/export-span/main.go
package main

import (
	"context"
	"fmt"
	"log"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

func main() {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Failed to initialize Braintrust: %v", err)
	}

	tracer := bt.Tracer("export-span-example")

	// --- Service A: create root span and export it ---
	_, rootSpan := tracer.Start(context.Background(), "service-a.request")
	exported := bt.Export(rootSpan)
	if exported == "" {
		log.Fatal("Export failed")
	}
	defer rootSpan.End()

	// Simulate sending the exported string to Service B (e.g. in a message or header)
	// In a real setup: publish to a queue, put in HTTP header "X-Braintrust-Span", etc.
	runServiceB(tracer, exported)

	if err := tp.ForceFlush(context.Background()); err != nil {
		log.Printf("Flush: %v", err)
	}

	fmt.Printf("\nView trace: %s\n", bt.Permalink(rootSpan))
}

// runServiceB simulates a separate service that receives the exported span
// and creates a child span in the same trace.
func runServiceB(tracer oteltrace.Tracer, exported string) {
	ctx, err := bttrace.ContextWithExportedSpan(context.Background(), exported)
	if err != nil {
		log.Fatalf("ContextWithExportedSpan: %v", err)
	}

	_, childSpan := tracer.Start(ctx, "service-b.process")
	childSpan.End()
}
