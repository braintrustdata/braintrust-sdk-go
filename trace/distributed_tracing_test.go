package trace

// This file contains conformance tests for the Braintrust distributed tracing
// spec (docs/features/distributed-tracing.md).
//
// The Go SDK is an OpenTelemetry-based SDK. It propagates W3C Trace Context
// (traceparent/tracestate) and W3C Baggage using OTel's standard
// propagation.TraceContext{} and propagation.Baggage{} propagators, which the
// application registers as the global composite propagator (see
// examples/distributed-tracing/main.go). The Braintrust-specific piece is the
// braintrust.parent baggage entry, written by SetParent and read by GetParent /
// the span processor.
//
// These tests exercise that documented mechanism end-to-end against the
// behaviors required by the spec's "Test cases" section.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// braintrustPropagator returns the composite propagator the Go SDK documents
// for distributed tracing: W3C TraceContext + W3C Baggage. This is what carries
// traceparent/tracestate/baggage (including braintrust.parent) across a
// boundary.
func braintrustPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

// traceparentRe matches the W3C traceparent format:
// version-trace_id-parent_id-flags.
var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// newActiveSpanCtx starts a real (sampled) span on a fresh tracer provider and
// returns the context carrying that span. The returned shutdown flushes the tp.
func newActiveSpanCtx(t *testing.T, parent *Parent) (context.Context, oteltrace.Span) {
	t.Helper()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("conformance")
	ctx := context.Background()
	if parent != nil {
		ctx = SetParent(ctx, *parent)
	}
	ctx, span := tracer.Start(ctx, "active-span")
	t.Cleanup(func() { span.End() })
	return ctx, span
}

// ---------------------------------------------------------------------------
// Send: header injection
// ---------------------------------------------------------------------------

func TestDistributedTracing_Inject_TraceparentWellFormed(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeProjectID, ID: "12345"}
	ctx, span := newActiveSpanCtx(t, &parent)

	carrier := propagation.MapCarrier{}
	braintrustPropagator().Inject(ctx, carrier)

	tp := carrier.Get("traceparent")
	require.NotEmpty(t, tp, "traceparent must be present")
	assert.Regexp(traceparentRe, tp, "traceparent must be well-formed")

	// trace id and parent id are non-zero.
	sc := span.SpanContext()
	assert.True(sc.TraceID().IsValid(), "active span must have a valid trace id")
	parts := strings.Split(tp, "-")
	require.Len(t, parts, 4)
	assert.NotEqual("00000000000000000000000000000000", parts[1], "trace id must be non-zero")
	assert.NotEqual("0000000000000000", parts[2], "parent id must be non-zero")
}

func TestDistributedTracing_Inject_IdsMatchActiveSpan(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeProjectID, ID: "12345"}
	ctx, span := newActiveSpanCtx(t, &parent)

	carrier := propagation.MapCarrier{}
	braintrustPropagator().Inject(ctx, carrier)

	tp := carrier.Get("traceparent")
	parts := strings.Split(tp, "-")
	require.Len(t, parts, 4)

	sc := span.SpanContext()
	// The injected trace id equals the active span's trace id (root_span_id
	// analogue), and the parent id equals the active span's span id.
	assert.Equal(sc.TraceID().String(), parts[1])
	assert.Equal(sc.SpanID().String(), parts[2])
}

func TestDistributedTracing_Inject_BaggageContainsBraintrustParent(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeProjectID, ID: "12345"}
	ctx, _ := newActiveSpanCtx(t, &parent)

	carrier := propagation.MapCarrier{}
	braintrustPropagator().Inject(ctx, carrier)

	bag := carrier.Get("baggage")
	require.NotEmpty(t, bag, "baggage must be present when a Braintrust parent is known")
	assert.Contains(bag, ParentOtelAttrKey+"=project_id:12345")
}

func TestDistributedTracing_Inject_PreservesUnrelatedBaggage(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeProjectID, ID: "12345"}
	ctx, _ := newActiveSpanCtx(t, &parent)

	// Add a non-Braintrust baggage member already present on the context.
	userMember, err := baggage.NewMember("user.id", "abc-123")
	require.NoError(t, err)
	existing := baggage.FromContext(ctx)
	userBag, err := existing.SetMember(userMember)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, userBag)

	carrier := propagation.MapCarrier{}
	braintrustPropagator().Inject(ctx, carrier)

	bag := carrier.Get("baggage")
	require.NotEmpty(t, bag)
	// Both the unrelated key and the braintrust.parent key are present.
	assert.Contains(bag, "user.id=abc-123")
	assert.Contains(bag, ParentOtelAttrKey+"=project_id:12345")
}

func TestDistributedTracing_Inject_NoBraintrustParent(t *testing.T) {
	assert := assert.New(t)

	// No parent set on the context.
	ctx, _ := newActiveSpanCtx(t, nil)

	carrier := propagation.MapCarrier{}
	braintrustPropagator().Inject(ctx, carrier)

	// traceparent is still injected (the span uses W3C-shaped hex ids).
	assert.Regexp(traceparentRe, carrier.Get("traceparent"))

	// braintrust.parent is absent from baggage (not emitted empty).
	bag := carrier.Get("baggage")
	assert.NotContains(bag, ParentOtelAttrKey)
}

func TestDistributedTracing_Inject_HeaderNamesAreLowercaseAndOverwriteCaseVariants(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeProjectID, ID: "12345"}
	ctx, _ := newActiveSpanCtx(t, &parent)

	// The outbound carrier already carries title-cased case-variants that a
	// framework might have added.
	headers := map[string][]string{
		"Traceparent": {"stale-value"},
		"Baggage":     {"stale=value"},
	}
	carrier := propagation.HeaderCarrier(headers)
	braintrustPropagator().Inject(ctx, carrier)

	// HeaderCarrier is backed by net/http canonicalization, so lowercase keys
	// resolve to the canonical case-variant: the SDK ends up with a single
	// logical key, not two conflicting ones.
	assert.Regexp(traceparentRe, carrier.Get("traceparent"))
	assert.NotEqual("stale-value", carrier.Get("traceparent"))
	assert.Contains(carrier.Get("baggage"), ParentOtelAttrKey+"=project_id:12345")

	// Exactly one logical traceparent / baggage entry exists.
	assert.Len(headers["Traceparent"], 1)
	assert.Len(headers["Baggage"], 1)
}

// ---------------------------------------------------------------------------
// Receive: header extraction
// ---------------------------------------------------------------------------

func TestDistributedTracing_Extract_ValidTraceparentWithBaggageParent(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"
	carrier := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
		"baggage":     ParentOtelAttrKey + "=experiment_id:exp-999",
	}

	ctx := braintrustPropagator().Extract(context.Background(), carrier)

	// Span shares the inbound trace id and is parented to the inbound span.
	sc := oteltrace.SpanContextFromContext(ctx)
	assert.True(sc.IsValid())
	assert.Equal(traceID, sc.TraceID().String())
	assert.Equal(parentID, sc.SpanID().String())

	// Routed to the baggage parent.
	ok, parent := GetParent(ctx)
	assert.True(ok)
	assert.Equal(ParentTypeExperimentID, parent.Type)
	assert.Equal("exp-999", parent.ID)
}

func TestDistributedTracing_Extract_ValidTraceparentNoBaggageParent(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"
	carrier := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
	}

	ctx := braintrustPropagator().Extract(context.Background(), carrier)

	// Span shares the inbound trace id and parent.
	sc := oteltrace.SpanContextFromContext(ctx)
	assert.True(sc.IsValid())
	assert.Equal(traceID, sc.TraceID().String())
	assert.Equal(parentID, sc.SpanID().String())

	// No braintrust.parent: routing falls back to the active logger/experiment.
	ok, _ := GetParent(ctx)
	assert.False(ok, "no braintrust.parent should be resolved")
}

func TestDistributedTracing_Extract_NoHeadersIsFreshRoot(t *testing.T) {
	assert := assert.New(t)

	carrier := propagation.MapCarrier{}
	ctx := braintrustPropagator().Extract(context.Background(), carrier)

	sc := oteltrace.SpanContextFromContext(ctx)
	assert.False(sc.IsValid(), "no propagation headers => no inbound span context => fresh root")

	ok, _ := GetParent(ctx)
	assert.False(ok)
}

func TestDistributedTracing_Extract_MalformedTraceparentIsFreshRoot(t *testing.T) {
	cases := map[string]string{
		"bad version":    "ff-f53d4cd03acedba3ca85a4605ca4bdce-baeeec9367deae51-01",
		"wrong length":   "00-f53d4cd03ace-baeeec9367deae51-01",
		"zero trace id":  "00-00000000000000000000000000000000-baeeec9367deae51-01",
		"zero parent id": "00-f53d4cd03acedba3ca85a4605ca4bdce-0000000000000000-01",
		"garbage":        "not-a-traceparent",
	}

	for name, tpValue := range cases {
		t.Run(name, func(t *testing.T) {
			carrier := propagation.MapCarrier{"traceparent": tpValue}
			ctx := braintrustPropagator().Extract(context.Background(), carrier)
			sc := oteltrace.SpanContextFromContext(ctx)
			assert.False(t, sc.IsValid(), "malformed traceparent must be treated as absent => fresh root")
		})
	}
}

func TestDistributedTracing_Extract_CaseInsensitiveHeaderNames(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"

	// Title-cased header names, as some HTTP frameworks normalize them.
	headers := map[string][]string{
		"Traceparent": {"00-" + traceID + "-" + parentID + "-01"},
		"Baggage":     {ParentOtelAttrKey + "=project_id:12345"},
	}
	carrier := propagation.HeaderCarrier(headers)

	ctx := braintrustPropagator().Extract(context.Background(), carrier)

	sc := oteltrace.SpanContextFromContext(ctx)
	assert.True(sc.IsValid(), "case-insensitive lookup must resolve traceparent")
	assert.Equal(traceID, sc.TraceID().String())
	assert.Equal(parentID, sc.SpanID().String())

	ok, parent := GetParent(ctx)
	assert.True(ok)
	assert.Equal("project_id:12345", parent.String())
}

func TestDistributedTracing_Extract_BaggageWithMixedKeys(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"
	carrier := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
		"baggage":     "user.id=abc-123," + ParentOtelAttrKey + "=project_id:12345,tenant=acme",
	}

	ctx := braintrustPropagator().Extract(context.Background(), carrier)

	// braintrust.parent is consumed.
	ok, parent := GetParent(ctx)
	assert.True(ok)
	assert.Equal("project_id:12345", parent.String())

	// Unrelated keys are ignored, not errored: they remain available in baggage.
	bag := baggage.FromContext(ctx)
	assert.Equal("abc-123", bag.Member("user.id").Value())
	assert.Equal("acme", bag.Member("tenant").Value())
}

func TestDistributedTracing_Extract_TracestateCapturedAndForwarded(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"
	tracestate := "vendor1=value1,vendor2=value2"

	inbound := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
		"tracestate":  tracestate,
	}

	prop := braintrustPropagator()
	ctx := prop.Extract(context.Background(), inbound)

	// Inbound tracestate is captured on the span context.
	sc := oteltrace.SpanContextFromContext(ctx)
	assert.Equal(tracestate, sc.TraceState().String())

	// A later inject within the trace re-emits the same tracestate unchanged.
	outbound := propagation.MapCarrier{}
	prop.Inject(ctx, outbound)
	assert.Equal(tracestate, outbound.Get("tracestate"))
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

func TestDistributedTracing_RoundTrip(t *testing.T) {
	assert := assert.New(t)

	parent := Parent{Type: ParentTypeExperimentID, ID: "exp-roundtrip"}
	ctx, span := newActiveSpanCtx(t, &parent)
	prop := braintrustPropagator()

	// Inject from the parent span.
	carrier := propagation.MapCarrier{}
	prop.Inject(ctx, carrier)

	// Extract on a fresh context using the produced headers.
	extractedCtx := prop.Extract(context.Background(), carrier)

	// Trace id and parent span id match the originating span.
	originSC := span.SpanContext()
	extractedSC := oteltrace.SpanContextFromContext(extractedCtx)
	assert.Equal(originSC.TraceID().String(), extractedSC.TraceID().String())
	assert.Equal(originSC.SpanID().String(), extractedSC.SpanID().String())

	// The resolved Braintrust parent matches.
	ok, resolved := GetParent(extractedCtx)
	assert.True(ok)
	assert.Equal(parent, resolved)
}

func TestDistributedTracing_RoundTrip_TracestatePassThrough(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"
	tracestate := "vendor1=value1"
	prop := braintrustPropagator()

	// Inbound carrier with tracestate.
	inbound := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
		"tracestate":  tracestate,
	}
	ctx := prop.Extract(context.Background(), inbound)

	// A span started from it (and descendants) forwards the same tracestate.
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	childCtx, child := tp.Tracer("conformance").Start(ctx, "child")
	t.Cleanup(func() { child.End() })

	outbound := propagation.MapCarrier{}
	prop.Inject(childCtx, outbound)
	assert.Equal(tracestate, outbound.Get("tracestate"))

	// When no inbound tracestate was present, none is emitted.
	inbound2 := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
	}
	ctx2 := prop.Extract(context.Background(), inbound2)
	outbound2 := propagation.MapCarrier{}
	prop.Inject(ctx2, outbound2)
	assert.Empty(outbound2.Get("tracestate"))
}

// ---------------------------------------------------------------------------
// Negative / robustness
// ---------------------------------------------------------------------------

// TestDistributedTracing_Extract_OversizedOrInvalidBaggageDoesNotThrow asserts
// that a syntactically invalid baggage header does not throw; the SDK falls
// back to trace identity from traceparent (or a fresh root).
func TestDistributedTracing_Extract_OversizedOrInvalidBaggageDoesNotThrow(t *testing.T) {
	assert := assert.New(t)

	traceID := "f53d4cd03acedba3ca85a4605ca4bdce"
	parentID := "baeeec9367deae51"

	// Invalid baggage (malformed member, no '=').
	carrier := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + parentID + "-01",
		"baggage":     "this is not valid baggage @@@",
	}

	var ctx context.Context
	assert.NotPanics(func() {
		ctx = braintrustPropagator().Extract(context.Background(), carrier)
	})

	// Falls back to trace identity from traceparent.
	sc := oteltrace.SpanContextFromContext(ctx)
	assert.True(sc.IsValid())
	assert.Equal(traceID, sc.TraceID().String())

	// braintrust.parent could not be resolved from the broken baggage.
	ok, _ := GetParent(ctx)
	assert.False(ok)
}

// TestDistributedTracing_SetParent_OversizedValueDoesNotBreak asserts SetParent
// is robust to a value that cannot be encoded as a baggage member: it still
// returns a usable context (same-process path) rather than panicking.
func TestDistributedTracing_SetParent_OversizedValueDoesNotBreak(t *testing.T) {
	assert := assert.New(t)

	// A control character in the ID makes baggage.NewMember fail; SetParent must
	// not panic and must still expose the parent via the context value.
	bad := Parent{Type: ParentTypeProjectID, ID: "bad\x00value"}

	var ctx context.Context
	assert.NotPanics(func() {
		ctx = SetParent(context.Background(), bad)
	})

	ok, parent := GetParent(ctx)
	assert.True(ok, "context-value fast path must still resolve the parent")
	assert.Equal(bad, parent)
}
