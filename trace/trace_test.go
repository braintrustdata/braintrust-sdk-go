package trace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Test helper: create a session for testing with proper auth info
func newTestSession() *auth.Session {
	return auth.NewTestSession(
		"test-api-key",
		"test-org-id",
		"test-org",
		"https://api.braintrust.dev",
		"https://www.braintrust.dev",
		"https://www.braintrust.dev",
		logger.Discard(),
	)
}

func TestSpanFilterFunc_WithAttributes(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	customFilter := func(span sdktrace.ReadOnlySpan) int {
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "importance" && attr.Value.AsString() == "high" {
				return 1
			}
			if string(attr.Key) == "noise" && attr.Value.AsBool() {
				return -1
			}
		}
		return 0
	}

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "filter-test",
		SpanFilterFuncs:  []SpanFilterFunc{customFilter},
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "important-operation", trace.WithAttributes(
		attribute.String("importance", "high"),
	))
	span1.End()

	_, span2 := tracer.Start(rootCtx, "noisy-operation", trace.WithAttributes(
		attribute.Bool("noise", true),
	))
	span2.End()

	_, span3 := tracer.Start(rootCtx, "normal-operation")
	span3.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 3)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	assert.Contains(spanNames, "root-operation")
	assert.Contains(spanNames, "important-operation")
	assert.Contains(spanNames, "normal-operation")
	assert.NotContains(spanNames, "noisy-operation")
}

func TestSpanProcessor_FilteringWithMultipleProcessors(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()

	// Register unfiltered processor first
	allSpansExporter := tracetest.NewInMemoryExporter()
	allSpansProcessor := sdktrace.NewSimpleSpanProcessor(allSpansExporter)
	tp.RegisterSpanProcessor(allSpansProcessor)

	// Register Braintrust processor with filtering
	braintrustExporter := tracetest.NewInMemoryExporter()

	dropNoisyFilter := func(span sdktrace.ReadOnlySpan) int {
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "noise" && attr.Value.AsBool() {
				return -1
			}
		}
		return 0
	}

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "multi-processor-test",
		SpanFilterFuncs:  []SpanFilterFunc{dropNoisyFilter},
		Exporter:         braintrustExporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "important-operation")
	span1.End()

	_, span2 := tracer.Start(rootCtx, "noisy-operation", trace.WithAttributes(
		attribute.Bool("noise", true),
	))
	span2.End()

	_, span3 := tracer.Start(rootCtx, "normal-operation")
	span3.End()

	_ = tp.ForceFlush(context.Background())

	// Verify all processor receives all spans
	allSpans := allSpansExporter.GetSpans()
	assert.Len(allSpans, 4)

	allSpanNames := make([]string, len(allSpans))
	for i, span := range allSpans {
		allSpanNames[i] = span.Name
	}
	assert.Contains(allSpanNames, "root-operation")
	assert.Contains(allSpanNames, "important-operation")
	assert.Contains(allSpanNames, "noisy-operation")
	assert.Contains(allSpanNames, "normal-operation")

	// Verify Braintrust processor filters out noisy spans
	braintrustSpans := braintrustExporter.GetSpans()
	assert.Len(braintrustSpans, 3)

	braintrustSpanNames := make([]string, len(braintrustSpans))
	for i, span := range braintrustSpans {
		braintrustSpanNames[i] = span.Name
	}
	assert.Contains(braintrustSpanNames, "root-operation")
	assert.Contains(braintrustSpanNames, "important-operation")
	assert.Contains(braintrustSpanNames, "normal-operation")
	assert.NotContains(braintrustSpanNames, "noisy-operation")
}

func TestAISpanFilterFunc_WithAIPrefixes(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "ai-filter-test",
		SpanFilterFuncs:  []SpanFilterFunc{aiSpanFilterFunc},
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "openai-call", trace.WithAttributes(
		attribute.String("gen_ai.system", "openai"),
		attribute.String("gen_ai.request.model", "gpt-4"),
	))
	span1.End()

	_, span2 := tracer.Start(rootCtx, "braintrust-log", trace.WithAttributes(
		attribute.String("braintrust.log.id", "12345"),
	))
	span2.End()

	_, span3 := tracer.Start(rootCtx, "llm-call", trace.WithAttributes(
		attribute.String("llm.request.type", "completion"),
	))
	span3.End()

	_, span4 := tracer.Start(rootCtx, "ai-service", trace.WithAttributes(
		attribute.String("ai.model.name", "claude"),
	))
	span4.End()

	_, span5 := tracer.Start(rootCtx, "traceloop-span", trace.WithAttributes(
		attribute.String("traceloop.span.kind", "llm"),
	))
	span5.End()

	_, span6 := tracer.Start(rootCtx, "database-query", trace.WithAttributes(
		attribute.String("db.statement", "SELECT * FROM users"),
	))
	span6.End()

	_, span7 := tracer.Start(rootCtx, "http-request")
	span7.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 6)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	assert.Contains(spanNames, "root-operation")
	assert.Contains(spanNames, "openai-call")
	assert.Contains(spanNames, "braintrust-log")
	assert.Contains(spanNames, "llm-call")
	assert.Contains(spanNames, "ai-service")
	assert.Contains(spanNames, "traceloop-span")
	assert.NotContains(spanNames, "database-query")
	assert.NotContains(spanNames, "http-request")
}

func TestAISpanFilterFunc_WithSpanNames(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "ai-name-filter-test",
		SpanFilterFuncs:  []SpanFilterFunc{aiSpanFilterFunc},
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "gen_ai.completion")
	span1.End()

	_, span2 := tracer.Start(rootCtx, "braintrust.experiment.log")
	span2.End()

	_, span3 := tracer.Start(rootCtx, "llm.chat_completion")
	span3.End()

	_, span4 := tracer.Start(rootCtx, "user.login")
	span4.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 4)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	assert.Contains(spanNames, "root-operation")
	assert.Contains(spanNames, "gen_ai.completion")
	assert.Contains(spanNames, "braintrust.experiment.log")
	assert.Contains(spanNames, "llm.chat_completion")
	assert.NotContains(spanNames, "user.login")
}

func TestWithFilterAISpans_Option(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "ai-option-test",
		FilterAISpans:    true,
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "gen_ai.completion", trace.WithAttributes(
		attribute.String("gen_ai.request.model", "gpt-4"),
	))
	span1.End()

	_, span2 := tracer.Start(rootCtx, "normal-http-call", trace.WithAttributes(
		attribute.String("llm.provider", "openai"),
	))
	span2.End()

	_, span3 := tracer.Start(rootCtx, "database-query", trace.WithAttributes(
		attribute.String("db.statement", "SELECT * FROM users"),
	))
	span3.End()

	_, span4 := tracer.Start(rootCtx, "http.request")
	span4.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 3)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	assert.Contains(spanNames, "root-operation")
	assert.Contains(spanNames, "gen_ai.completion")
	assert.Contains(spanNames, "normal-http-call")
	assert.NotContains(spanNames, "database-query")
	assert.NotContains(spanNames, "http.request")
}

func TestWithFilterAISpans_CombinedWithCustomFilters(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	customFilter := func(span sdktrace.ReadOnlySpan) int {
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "importance" {
				switch attr.Value.AsString() {
				case "high":
					return 1
				case "low":
					return -1
				}
			}
		}
		return 0
	}

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "combined-filter-test",
		FilterAISpans:    true,
		SpanFilterFuncs:  []SpanFilterFunc{customFilter},
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	rootCtx, rootSpan := tracer.Start(context.Background(), "root-operation")
	rootSpan.End()

	_, span1 := tracer.Start(rootCtx, "gen_ai.completion")
	span1.End()

	_, span2 := tracer.Start(rootCtx, "critical-operation", trace.WithAttributes(
		attribute.String("importance", "high"),
	))
	span2.End()

	_, span3 := tracer.Start(rootCtx, "routine-operation")
	span3.End()

	// Test priority: custom filter overrides AI filter
	// This span has AI attributes but custom filter should drop it (custom filter has priority)
	_, span4 := tracer.Start(rootCtx, "gen_ai.bad_completion", trace.WithAttributes(
		attribute.String("gen_ai.request.model", "gpt-4"),
		attribute.String("importance", "low"),
	))
	span4.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 3)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	assert.Contains(spanNames, "root-operation")
	assert.Contains(spanNames, "gen_ai.completion")
	assert.Contains(spanNames, "critical-operation")
	assert.NotContains(spanNames, "routine-operation")
	assert.NotContains(spanNames, "gen_ai.bad_completion")
}

func TestAISpanFilterFunc_KeepsRootSpans(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "root-spans-test",
		FilterAISpans:    true,
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	// Root span without AI attributes - should still be kept
	_, rootSpan1 := tracer.Start(context.Background(), "http-server-request")
	rootSpan1.End()

	// Root span with AI name - should be kept
	_, rootSpan2 := tracer.Start(context.Background(), "gen_ai.root_completion")
	rootSpan2.End()

	// Root with children - root should be kept, children filtered
	rootCtx, rootSpan3 := tracer.Start(context.Background(), "parent-operation")

	_, childAI := tracer.Start(rootCtx, "llm.child_completion")
	childAI.End()

	_, childNonAI := tracer.Start(rootCtx, "database-query")
	childNonAI.End()

	rootSpan3.End()

	_ = tp.ForceFlush(context.Background())
	spans := exporter.GetSpans()

	assert.Len(spans, 4)

	spanNames := make([]string, len(spans))
	for i, span := range spans {
		spanNames[i] = span.Name
	}

	// All root spans should be kept
	assert.Contains(spanNames, "http-server-request")
	assert.Contains(spanNames, "gen_ai.root_completion")
	assert.Contains(spanNames, "parent-operation")
	// AI child should be kept
	assert.Contains(spanNames, "llm.child_completion")
	// Non-AI child should be filtered out
	assert.NotContains(spanNames, "database-query")
}

func TestPermalink_ReadWriteSpan(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "test-project",
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	// Create a live span (ReadWriteSpan)
	_, span := tracer.Start(context.Background(), "test-operation")

	// Test Permalink with live span (before ending)
	link, err := Permalink(span)
	assert.NoError(err)
	assert.NotEmpty(link)
	assert.Contains(link, "https://www.braintrust.dev")
	assert.Contains(link, "test-project")
	assert.Contains(link, span.SpanContext().SpanID().String())
	assert.Contains(link, span.SpanContext().TraceID().String())

	span.End()
}

func TestPermalink_EndedSpan(t *testing.T) {
	assert := assert.New(t)

	tp := sdktrace.NewTracerProvider()
	exporter := tracetest.NewInMemoryExporter()

	session := newTestSession()
	cfg := Config{
		DefaultProjectID: "test-project",
		Exporter:         exporter,
		Logger:           logger.Discard(),
	}

	err := AddSpanProcessor(tp, session, cfg)
	assert.NoError(err)

	tracer := tp.Tracer("test")

	// Create a span
	_, span := tracer.Start(context.Background(), "test-operation")
	spanContext := span.SpanContext()

	// Get the permalink BEFORE ending - this is the normal use case
	// Once a span ends, IsRecording() returns false and it's treated as noop
	linkBefore, err := Permalink(span)
	assert.NoError(err)
	assert.NotEmpty(linkBefore)
	assert.Contains(linkBefore, "https://www.braintrust.dev")
	assert.Contains(linkBefore, "test-project")
	assert.Contains(linkBefore, spanContext.SpanID().String())
	assert.Contains(linkBefore, spanContext.TraceID().String())

	span.End()

	// Test Permalink with ended span
	// After ending, IsRecording() returns false, so it gets the noop fallback
	linkAfter, err := Permalink(span)
	assert.NoError(err)
	assert.NotEmpty(linkAfter)
	// Ended spans are treated as noop and get fallback URL
	assert.Contains(linkAfter, "https://www.braintrust.dev")
	assert.Contains(linkAfter, "noop-span")
}

func TestPermalink_NoopSpan(t *testing.T) {
	assert := assert.New(t)

	// Use noop tracer provider to create a noop span
	noopTP := noop.NewTracerProvider()
	noopTracer := noopTP.Tracer("test")
	_, noopSpan := noopTracer.Start(context.Background(), "noop-span")

	// Test Permalink with noop span
	// Noop spans don't record, so Permalink should return fallback URL
	link, err := Permalink(noopSpan)
	assert.NoError(err)
	assert.NotEmpty(link)

	// Should contain fallback URL with noop indicator
	assert.Contains(link, "https://www.braintrust.dev")
	assert.Contains(link, "noop-span")

	noopSpan.End()
}
