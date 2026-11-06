package trace

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestSpanOriginProvenance(t *testing.T) {
	for _, tt := range []struct {
		name            string
		scope           string
		version         string
		contextJSON     string
		instrumentation map[string]any
	}{
		{name: "scope and version", scope: "example.com/instrumentation", version: "1.2.3", instrumentation: map[string]any{"name": "example.com/instrumentation", "version": "1.2.3"}},
		{name: "empty scope", instrumentation: map[string]any{"name": "go.opentelemetry.io/otel/sdk/tracer"}},
		{name: "unknown scope version", scope: "my-app", instrumentation: map[string]any{"name": "my-app"}},
		{name: "explicit instrumentation name", scope: "my-app", version: "1.2.3", contextJSON: `{"caller_filename":"app.go","span_origin":{"instrumentation":{"name":"custom"}}}`, instrumentation: map[string]any{"name": "custom"}},
		{name: "explicit instrumentation version", scope: "my-app", contextJSON: `{"span_origin":{"instrumentation":{"version":"2.0.0"}}}`, instrumentation: map[string]any{"name": "my-app", "version": "2.0.0"}},
		{name: "invalid context", scope: "my-app", contextJSON: "{", instrumentation: map[string]any{"name": "my-app"}},
		{name: "non object context", scope: "my-app", contextJSON: "[]", instrumentation: map[string]any{"name": "my-app"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider()
			require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{Exporter: exporter, Logger: logger.Discard(), Environment: &SpanOriginEnvironment{Type: "ci", Name: "github_actions"}}))
			t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
			_, span := tp.Tracer(tt.scope, oteltrace.WithInstrumentationVersion(tt.version)).Start(t.Context(), "operation")
			// Context may be supplied after OnStart, and must still survive export.
			span.SetAttributes(attribute.String(contextJSONAttrKey, tt.contextJSON))
			span.End()
			require.NoError(t, tp.ForceFlush(t.Context()))
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			var captured map[string]any
			for _, attr := range spans[0].Attributes {
				if attr.Key == contextJSONAttrKey {
					require.NoError(t, json.Unmarshal([]byte(attr.Value.AsString()), &captured))
				}
			}
			require.NotNil(t, captured)
			origin := captured["span_origin"].(map[string]any)
			assert.Equal(t, "braintrust.sdk.go", origin["name"])
			assert.Equal(t, tt.instrumentation, origin["instrumentation"])
			assert.Equal(t, map[string]any{"type": "ci", "name": "github_actions"}, origin["environment"])
			if tt.name == "explicit instrumentation name" {
				assert.Equal(t, "app.go", captured["caller_filename"])
			}
		})
	}
}

func TestSpanOriginOmitsUnknownSDKVersion(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	_, span := tp.Tracer("test").Start(t.Context(), "operation")
	span.End()
	var captured map[string]any
	require.NoError(t, json.Unmarshal([]byte(spanOriginContextJSON(span.(sdktrace.ReadOnlySpan), nil)), &captured))
	origin := captured["span_origin"].(map[string]any)
	assert.NotEqual(t, "unknown", origin["version"])
	assert.NotEqual(t, "(devel)", origin["version"])
	assert.NotContains(t, origin, "environment")
}

func TestSpanOriginPreservesExplicitOrigin(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	_, span := tp.Tracer("test").Start(t.Context(), "operation", oteltrace.WithAttributes(attribute.String(contextJSONAttrKey,
		`{"caller_lineno":42,"span_origin":{"name":"custom.exporter","version":"3","environment":{"name":"custom"}}}`)))
	span.End()
	var captured map[string]any
	require.NoError(t, json.Unmarshal([]byte(spanOriginContextJSON(span.(sdktrace.ReadOnlySpan), &SpanOriginEnvironment{Type: "ci"})), &captured))
	origin := captured["span_origin"].(map[string]any)
	assert.Equal(t, float64(42), captured["caller_lineno"])
	assert.Equal(t, "custom.exporter", origin["name"])
	assert.Equal(t, "3", origin["version"])
	assert.Equal(t, map[string]any{"name": "custom"}, origin["environment"])
}

func TestSpanOriginDoesNotAffectAIFiltering(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{Exporter: exporter, Logger: logger.Discard(), FilterAISpans: true}))
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
	tracer := tp.Tracer("my-app")
	ctx, root := tracer.Start(t.Context(), "root")
	_, child := tracer.Start(ctx, "database-query")
	child.End()
	root.End()
	require.NoError(t, tp.ForceFlush(t.Context()))
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "root", spans[0].Name)
}

func TestSpanOriginEmptyEnvironmentDisablesDetection(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{Exporter: exporter, Logger: logger.Discard(), Environment: &SpanOriginEnvironment{}}))
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
	_, span := tp.Tracer("test").Start(t.Context(), "operation")
	span.End()
	require.NoError(t, tp.ForceFlush(t.Context()))
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	for _, attr := range spans[0].Attributes {
		if attr.Key == contextJSONAttrKey {
			var captured map[string]any
			require.NoError(t, json.Unmarshal([]byte(attr.Value.AsString()), &captured))
			assert.NotContains(t, captured["span_origin"], "environment")
			return
		}
	}
	t.Fatal("missing context provenance")
}
