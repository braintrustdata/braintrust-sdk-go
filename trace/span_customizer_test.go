package trace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// These tests exercise local exporter policy, not the Braintrust API. OTel's
// in-memory exporter lets us assert exactly what leaves the customization boundary.
func customizerTestSpan(name string) sdktrace.ReadOnlySpan {
	return tracetest.SpanStub{
		Name: name,
		SpanContext: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: oteltrace.TraceID{1}, SpanID: oteltrace.SpanID{2},
		}),
		Parent: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: oteltrace.TraceID{1}, SpanID: oteltrace.SpanID{3}, Remote: true,
		}),
		Attributes: []attribute.KeyValue{attribute.String("braintrust.input_json", `"secret"`)},
		Events:     []sdktrace.Event{{Name: "event", Attributes: []attribute.KeyValue{attribute.String("secret", "event")}}},
		Links:      []sdktrace.Link{{Attributes: []attribute.KeyValue{attribute.String("secret", "link")}}},
	}.Snapshot()
}

type customizerAttributes struct {
	sdktrace.ReadOnlySpan
	attrs []attribute.KeyValue
}

func (s customizerAttributes) Attributes() []attribute.KeyValue { return s.attrs }

func TestSpanCustomizersOrderedReplacementAndIsolation(t *testing.T) {
	original := customizerTestSpan("ordered")
	sink := tracetest.NewInMemoryExporter()
	customizers := []config.SpanCustomizer{
		{}, // Optional hooks do not interrupt the chain.
		{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			span.Attributes()[0] = attribute.String("braintrust.input_json", `"redacted"`)
			span.Events()[0].Attributes[0] = attribute.String("secret", "redacted")
			span.Links()[0].Attributes[0] = attribute.String("secret", "redacted")
			return span, nil
		}},
		{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			require.Equal(t, `"redacted"`, span.Attributes()[0].Value.AsString())
			// Replacement removes input entirely and changes the destination.
			return customizerAttributes{span, []attribute.KeyValue{
				attribute.String(ParentOtelAttrKey, "project_name:redacted"),
				attribute.Bool("customized", true),
			}}, nil
		}},
	}
	exporter := newCustomizingExporter(sink, customizers, logger.Discard())
	customizers[1].OnSpanExport = func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
		return nil, errors.New("configuration changed after setup")
	}
	batch := []sdktrace.ReadOnlySpan{original}
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	exported := sink.GetSpans()
	require.Len(t, exported, 1)
	assert.Equal(t, []attribute.KeyValue{
		attribute.String(ParentOtelAttrKey, "project_name:redacted"),
		attribute.Bool("customized", true),
	}, exported[0].Attributes)
	assert.Equal(t, original, batch[0])
	assert.Equal(t, `"secret"`, original.Attributes()[0].Value.AsString())
	assert.Equal(t, "event", original.Events()[0].Attributes[0].Value.AsString())
	assert.Equal(t, "link", original.Links()[0].Attributes[0].Value.AsString())
}

func TestSpanCustomizersFailEntireBatch(t *testing.T) {
	failure := errors.New("redaction failed")
	for _, tc := range []struct {
		name string
		hook func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error)
	}{
		{"error", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { return nil, failure }},
		{"nil", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { return nil, nil }},
		{"typed nil", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			var span *customizerAttributes
			return span, nil
		}},
		{"panic", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { panic("redaction failed") }},
		{"nil panic", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { panic(nil) }},
		{"identity", func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			stub := tracetest.SpanStubFromReadOnlySpan(span)
			stub.SpanContext = stub.SpanContext.WithSpanID(oteltrace.SpanID{9})
			return stub.Snapshot(), nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := tracetest.NewInMemoryExporter()
			laterCalls := 0
			exporter := newCustomizingExporter(sink, []config.SpanCustomizer{
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					span.Attributes()[0] = attribute.String("braintrust.input_json", `"redacted"`)
					if span.Name() == "second" {
						return tc.hook(span)
					}
					return span, nil
				}},
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					laterCalls++
					return span, nil
				}},
			}, logger.Discard())
			batch := []sdktrace.ReadOnlySpan{customizerTestSpan("first"), customizerTestSpan("second")}
			err := exporter.ExportSpans(context.Background(), batch)
			require.Error(t, err)
			if tc.name == "error" {
				assert.ErrorIs(t, err, failure)
			}
			assert.Empty(t, sink.GetSpans())
			assert.Equal(t, 1, laterCalls)
			for _, span := range batch {
				assert.Equal(t, `"secret"`, span.Attributes()[0].Value.AsString())
			}
		})
	}
}

func TestSpanCustomizersProtectIdentityAfterEachHook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		root   bool
		change func(*tracetest.SpanStub)
	}{
		{name: "trace", change: func(s *tracetest.SpanStub) { s.SpanContext = s.SpanContext.WithTraceID(oteltrace.TraceID{9}) }},
		{name: "span", change: func(s *tracetest.SpanStub) { s.SpanContext = s.SpanContext.WithSpanID(oteltrace.SpanID{9}) }},
		{name: "parent trace", change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithTraceID(oteltrace.TraceID{9}) }},
		{name: "parent span", change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithSpanID(oteltrace.SpanID{9}) }},
		{name: "remove parent", change: func(s *tracetest.SpanStub) { s.Parent = oteltrace.SpanContext{} }},
		{name: "add root parent", root: true, change: func(s *tracetest.SpanStub) { s.Parent = s.SpanContext }},
		{name: "invalid parent trace", root: true, change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithTraceID(oteltrace.TraceID{9}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := tracetest.SpanStubFromReadOnlySpan(customizerTestSpan("identity"))
			if tc.root {
				stub.Parent = oteltrace.SpanContext{}
			}
			original := stub.Snapshot()
			sink := tracetest.NewInMemoryExporter()
			laterCalled := false
			exporter := newCustomizingExporter(sink, []config.SpanCustomizer{
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					changed := tracetest.SpanStubFromReadOnlySpan(span)
					tc.change(&changed)
					return changed.Snapshot(), nil
				}},
				{OnSpanExport: func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					laterCalled = true
					return original, nil // Restoring IDs later must not bypass validation.
				}},
			}, logger.Discard())
			require.Error(t, exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{original}))
			assert.False(t, laterCalled)
			assert.Empty(t, sink.GetSpans())
		})
	}
}

func TestSpanCustomizersAllowContextMetadataAndRerunOnResubmission(t *testing.T) {
	sink := tracetest.NewInMemoryExporter()
	calls := 0
	exporter := newCustomizingExporter(sink, []config.SpanCustomizer{{
		OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			calls++
			stub := tracetest.SpanStubFromReadOnlySpan(span)
			stub.Parent = stub.Parent.WithRemote(false)
			stub.SpanContext = stub.SpanContext.WithTraceFlags(oteltrace.FlagsSampled)
			return stub.Snapshot(), nil
		},
	}}, logger.Discard())
	batch := []sdktrace.ReadOnlySpan{customizerTestSpan("retry")}
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	assert.Equal(t, 2, calls)
	require.Len(t, sink.GetSpans(), 2)
	assert.False(t, sink.GetSpans()[0].Parent.IsRemote())
	assert.True(t, sink.GetSpans()[0].SpanContext.IsSampled())
}

func TestSpanCustomizersProcessorPreservesOtherConsumers(t *testing.T) {
	ctx := context.Background()
	sink := tracetest.NewInMemoryExporter()
	originals := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(ctx)) })
	require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{
		Exporter: sink, Logger: logger.Discard(),
		SpanCustomizers: []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			return customizerAttributes{span, []attribute.KeyValue{attribute.String("exported", "redacted")}}, nil
		}}},
	}))
	tp.RegisterSpanProcessor(originals)
	_, span := tp.Tracer("application").Start(ctx, "manual")
	span.SetAttributes(attribute.String("application", "original"))
	span.End()
	require.NoError(t, tp.ForceFlush(ctx))
	require.Len(t, sink.GetSpans(), 1)
	assert.Equal(t, []attribute.KeyValue{attribute.String("exported", "redacted")}, sink.GetSpans()[0].Attributes)
	require.Len(t, originals.Ended(), 1)
	assert.Contains(t, originals.Ended()[0].Attributes(), attribute.String("application", "original"))
}
