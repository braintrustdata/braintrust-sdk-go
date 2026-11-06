package main

import (
	"context"
	"log"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
	ctx := context.Background()
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx) //nolint:errcheck

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-span-origin-example"),
		// Explicit environment values take precedence over ambient detection.
		braintrust.WithEnvironment("local", "development"),
	)
	if err != nil {
		log.Fatal(err)
	}

	// The exported context.span_origin records braintrust.sdk.go and, when
	// available, its version. Instrumentation comes from this OTel scope.
	tracer := bt.Tracer("my-app", oteltrace.WithInstrumentationVersion("1.0.0"))
	_, span := tracer.Start(ctx, "span-origin-example")
	// Caller context and explicit provenance overrides are preserved at export.
	span.SetAttributes(attribute.String("braintrust.context_json", `{"caller_functionname":"main"}`))
	span.End()
	if err := tp.ForceFlush(ctx); err != nil {
		log.Fatal(err)
	}
}
