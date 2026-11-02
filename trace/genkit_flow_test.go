package trace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNormalizeGenkitFlow(t *testing.T) {
	tests := []struct {
		name, scope           string
		attributes            []attribute.KeyValue
		wantInput, wantOutput string
	}{
		{"flow", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:input", `{"n":1}`), attribute.String("genkit:output", `null`)}, `{"n":1}`, `null`},
		{"existing values win", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:input", `"source"`), attribute.String("braintrust.input_json", `"explicit"`)}, `"explicit"`, ""},
		{"invalid and absent", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:input", `not-json`)}, "", ""},
		{"failed flow has input only", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:input", `"request"`)}, `"request"`, ""},
		{"non-string input ignored", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.Int("genkit:input", 1)}, "", ""},
		{"existing output wins", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:output", `"source"`), attribute.String("braintrust.output_json", `"explicit"`)}, "", `"explicit"`},
		{"model is unchanged", "genkit-tracer", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "model"), attribute.String("genkit:input", `"secret"`)}, "", ""},
		{"other tracer is unchanged", "other", []attribute.KeyValue{attribute.String("genkit:type", "action"), attribute.String("genkit:metadata:subtype", "flow"), attribute.String("genkit:input", `"secret"`)}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			_, span := tp.Tracer(tc.scope).Start(context.Background(), "test")
			span.SetAttributes(tc.attributes...)
			span.End()
			original := exporter.GetSpans()[0].Snapshot()
			mapped := normalizeGenkitFlow(original)
			attrs := make(map[attribute.Key]string)
			for _, a := range mapped.Attributes() {
				attrs[a.Key] = a.Value.AsString()
			}
			require.Equal(t, tc.wantInput, attrs["braintrust.input_json"])
			require.Equal(t, tc.wantOutput, attrs["braintrust.output_json"])
			if tc.wantInput != `"explicit"` {
				for _, a := range original.Attributes() {
					require.NotEqual(t, attribute.Key("braintrust.input_json"), a.Key)
				}
			}
			require.NoError(t, tp.Shutdown(context.Background()))
		})
	}
}
