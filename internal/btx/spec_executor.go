package btx

import (
	"context"
	"net/http"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// executeSpec runs all requests in a spec under a parent OTel span and returns
// the trace ID (hex string). The provider-specific executor performs the
// actual SDK calls using the VCR-wrapped HTTP client.
func executeSpec(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client, executor ExecuteFunc) (string, error) {
	tracer := tp.Tracer("btx")
	ctx, rootSpan := tracer.Start(ctx, spec.Name)
	defer rootSpan.End()

	traceID := rootSpan.SpanContext().TraceID().String()
	return traceID, executor(ctx, spec, tp, httpClient)
}
