// Package main demonstrates distributed tracing using W3C baggage propagation
// and Braintrust span export (similar to span.export() in the JS/Python SDK).
//
// This example shows how trace context propagates across service boundaries
// via W3C baggage. A parent span encodes context to headers, and a child span
// extracts it (simulated without an actual HTTP server).
//
// Alternatively, you can use trace.Export(span) to get a serialized string and
// trace.ContextWithExportedSpan(ctx, exported) on the remote side to attach
// children to that span (e.g. when passing the parent in a message or custom header).
//
// To run this example:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	go run main.go
package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

func main() {
	// Setup tracing
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	// Set global tracer provider
	otel.SetTracerProvider(tp)

	// Enable W3C baggage propagation globally (required for distributed tracing)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create Braintrust client with default project
	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Failed to initialize Braintrust: %v", err)
	}

	tracer := otel.Tracer("examples/distributed-tracing")
	ctx := context.Background()

	// Create parent span
	ctx, parentSpan := tracer.Start(ctx, "examples/distributed-tracing/main.go")
	defer parentSpan.End()

	// Encode context to headers (simulates HTTP request)
	headers := make(map[string]string)
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))

	// Call remote service (simulates crossing service boundary)
	simulateHTTPRequest(headers)

	// Alternative: pass exported span string (like JS/Python span.export())
	if exported, err := bttrace.Export(parentSpan); err == nil {
		simulateHTTPRequestWithExportedSpan(exported)
	}

	// Flush all spans
	if err := tp.ForceFlush(context.Background()); err != nil {
		log.Printf("Failed to flush spans: %v", err)
	}

	fmt.Printf("\nView span: %s\n", bt.Permalink(parentSpan))
}

// simulateHTTPRequest simulates a remote service receiving an HTTP request
func simulateHTTPRequest(headers map[string]string) {
	tracer := otel.Tracer("examples/distributed-tracing")

	// Extract context from headers (simulates HTTP handler)
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(headers))

	// Create child span - inherits trace context from parent
	_, span := tracer.Start(ctx, "remote-service.handle-request")
	defer span.End()
}

// simulateHTTPRequestWithExportedSpan simulates a remote service that receives
// an exported span string (e.g. from a message or custom header) and creates
// a child span. This is similar to using span.export() in the JS/Python SDK.
func simulateHTTPRequestWithExportedSpan(exported string) {
	tracer := otel.Tracer("examples/distributed-tracing")

	ctx, err := bttrace.ContextWithExportedSpan(context.Background(), exported)
	if err != nil {
		log.Printf("ContextWithExportedSpan: %v", err)
		return
	}

	_, span := tracer.Start(ctx, "remote-service.handle-request-export")
	defer span.End()
}
