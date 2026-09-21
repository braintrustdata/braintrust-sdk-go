package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/config"
)

// Embedding the export snapshot preserves every field except Attributes.
// No live application span or provider response is changed.
type redactedSpan struct {
	sdktrace.ReadOnlySpan
	attrs []attribute.KeyValue
}

func (s redactedSpan) Attributes() []attribute.KeyValue { return s.attrs }

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tp := sdktrace.NewTracerProvider()
	client, err := braintrust.New(tp,
		braintrust.WithProject("span-customizers-example"),
		braintrust.WithBlockingLogin(true),
		braintrust.WithSpanCustomizers(config.SpanCustomizer{
			OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
				attrs := make([]attribute.KeyValue, 0, len(span.Attributes())+1)
				for _, attr := range span.Attributes() {
					switch attr.Key {
					case "braintrust.input_json":
						attrs = append(attrs, attribute.String("braintrust.input_json", `"[redacted]"`))
					case "braintrust.output_json":
						// Omit the output attribute entirely.
					default:
						attrs = append(attrs, attr)
					}
				}
				attrs = append(attrs, attribute.Bool("redacted", true))
				return redactedSpan{ReadOnlySpan: span, attrs: attrs}, nil
			},
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	_, span := client.Tracer("span-customizers-example").Start(ctx, "redacted-operation")
	span.SetAttributes(
		attribute.String("braintrust.input_json", `{"email":"private@example.com"}`),
		attribute.String("braintrust.output_json", `"private response"`),
	)
	link := client.Permalink(span)
	span.End()
	if err := tp.ForceFlush(ctx); err != nil {
		log.Fatal(err)
	}
	if err := tp.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Exported redacted span:", link)
}
