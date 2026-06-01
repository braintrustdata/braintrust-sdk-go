package main

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
	ctx := context.Background()

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx) //nolint:errcheck
	otel.SetTracerProvider(tp)

	// This intentionally omits WithAPIKey. The SDK reads BRAINTRUST_API_KEY
	// from the environment, or from the nearest .braintrust.json fallback.
	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-braintrust-json-example"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	tracer := bt.Tracer("braintrust-json-example")
	_, span := tracer.Start(ctx, "braintrust-json-api-key-discovery")
	span.End()

	if err := tp.ForceFlush(ctx); err != nil {
		log.Fatal(err)
	}
}
