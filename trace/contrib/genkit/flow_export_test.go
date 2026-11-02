package genkit

import (
	"context"
	"strings"
	"testing"

	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

func TestGenkitFlowExportsInputAndOutput(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	session := auth.NewTestSession("test-key", "org-id", "org", "https://api.braintrust.dev", "https://www.braintrust.dev", "https://www.braintrust.dev", logger.Discard())
	require.NoError(t, bttrace.AddSpanProcessor(tp, session, bttrace.Config{Exporter: exporter, Logger: logger.Discard()}))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(context.Background()) })

	g := genkit.Init(ctx)
	flow := genkit.DefineFlow(g, "uppercase", func(_ context.Context, input string) (string, error) {
		return strings.ToUpper(input), nil
	})
	result, err := flow.Run(ctx, "hello")
	require.NoError(t, err)
	require.Equal(t, "HELLO", result)
	require.NoError(t, tp.ForceFlush(ctx))

	var flowAttrs map[string]string
	for _, span := range exporter.GetSpans() {
		attrs := make(map[string]string)
		for _, a := range span.Attributes {
			attrs[string(a.Key)] = a.Value.AsString()
		}
		if attrs["genkit:metadata:subtype"] == "flow" {
			flowAttrs = attrs
			break
		}
	}
	require.NotNil(t, flowAttrs, "expected a Genkit flow span; got: %+v", exporter.GetSpans())
	require.JSONEq(t, `"hello"`, flowAttrs["braintrust.input_json"])
	require.JSONEq(t, `"HELLO"`, flowAttrs["braintrust.output_json"])
}
