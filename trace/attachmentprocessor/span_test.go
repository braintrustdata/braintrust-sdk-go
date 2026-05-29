package attachmentprocessor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// makeReadOnlySpan creates a real ReadOnlySpan with the given attributes for testing.
func makeReadOnlySpan(t *testing.T, attrs ...attribute.KeyValue) sdktrace.ReadOnlySpan {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	_, span := tp.Tracer("test").Start(context.Background(), "test-span")
	span.SetAttributes(attrs...)
	span.End()

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 span, got %d", len(stubs))
	}
	return stubs[0].Snapshot()
}

func TestNewTransformedSpan_OverridesExistingKey(t *testing.T) {
	orig := makeReadOnlySpan(t,
		attribute.String("braintrust.input_json", "original"),
		attribute.String("other.attr", "keep-me"),
	)

	transformed := NewTransformedSpan(orig, map[attribute.Key]string{
		"braintrust.input_json": "replaced",
	})

	attrs := transformed.Attributes()
	got := make(map[string]string)
	for _, a := range attrs {
		got[string(a.Key)] = a.Value.AsString()
	}

	assert.Equal(t, "replaced", got["braintrust.input_json"])
	assert.Equal(t, "keep-me", got["other.attr"])
	assert.Len(t, attrs, 2)
}

func TestNewTransformedSpan_AppendsNewKey(t *testing.T) {
	orig := makeReadOnlySpan(t,
		attribute.String("existing", "value"),
	)

	transformed := NewTransformedSpan(orig, map[attribute.Key]string{
		"new.key": "new-value",
	})

	attrs := transformed.Attributes()
	got := make(map[string]string)
	for _, a := range attrs {
		got[string(a.Key)] = a.Value.AsString()
	}

	// The new key should be appended, not silently dropped.
	assert.Equal(t, "value", got["existing"])
	assert.Equal(t, "new-value", got["new.key"])
	assert.Len(t, attrs, 2)
}

func TestNewTransformedSpan_MixedOverrideAndAppend(t *testing.T) {
	orig := makeReadOnlySpan(t,
		attribute.String("braintrust.input_json", "orig-in"),
		attribute.String("other", "preserved"),
	)

	transformed := NewTransformedSpan(orig, map[attribute.Key]string{
		"braintrust.input_json":  "new-in",
		"braintrust.output_json": "new-out", // not on original
	})

	got := make(map[string]string)
	for _, a := range transformed.Attributes() {
		got[string(a.Key)] = a.Value.AsString()
	}

	assert.Equal(t, "new-in", got["braintrust.input_json"])
	assert.Equal(t, "new-out", got["braintrust.output_json"])
	assert.Equal(t, "preserved", got["other"])
	assert.Len(t, transformed.Attributes(), 3)
}

func TestNewTransformedSpan_PreservesDelegateMethods(t *testing.T) {
	orig := makeReadOnlySpan(t, attribute.String("k", "v"))
	transformed := NewTransformedSpan(orig, map[attribute.Key]string{})

	assert.Equal(t, orig.Name(), transformed.Name())
	assert.Equal(t, orig.SpanContext(), transformed.SpanContext())
	assert.Equal(t, orig.StartTime(), transformed.StartTime())
	assert.Equal(t, orig.EndTime(), transformed.EndTime())
}
