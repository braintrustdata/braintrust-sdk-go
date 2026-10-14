package trace

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

type customizingExporter struct {
	sdktrace.SpanExporter
	customizers []config.SpanCustomizer
	logger      logger.Logger
}

var customizerFailureLogOnce sync.Once

func newCustomizingExporter(exporter sdktrace.SpanExporter, customizers []config.SpanCustomizer, log logger.Logger) sdktrace.SpanExporter {
	for _, customizer := range customizers {
		if customizer.OnSpanExport != nil {
			return &customizingExporter{
				SpanExporter: exporter,
				customizers:  slices.Clone(customizers),
				logger:       log,
			}
		}
	}
	// Keep the existing exporter and allocation-free path when no hooks apply.
	return exporter
}

func (e *customizingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	outgoing := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		customized, err := e.customize(span)
		if err != nil {
			// Diagnostics contain only SDK-generated failure kinds, never payloads
			// or hook error/panic text. All exporters share one process-wide budget.
			customizerFailureLogOnce.Do(func() {
				e.logger.Error("span customization failed; span content stripped", "error", err)
			})
		}
		if customized != nil {
			outgoing = append(outgoing, customized)
		}
	}
	// Never mutate the caller's batch. Finish customization (including stripping)
	// before any attachment uploads begin. Transport retries reuse outgoing.
	return e.SpanExporter.ExportSpans(ctx, outgoing)
}

// customize returns the customized span, or a stripped record with an error if
// any hook fails. Spans that cannot be read are dropped (nil result).
func (e *customizingExporter) customize(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
	input, err := captureCustomizerInput(span)
	if err != nil {
		// Without a readable identity there is no safe stripped record to send.
		return nil, err
	}
	customized, err := e.runCustomizers(input)
	if err != nil {
		return input.stripped(), err
	}
	return customized, nil
}

func (e *customizingExporter) runCustomizers(input customizerInput) (result sdktrace.ReadOnlySpan, err error) {
	hookIndex := -1
	defer func() {
		if recovered := recover(); recovered != nil || (result == nil && err == nil) {
			// Panic values may contain secrets. Also handle legacy panic(nil).
			result = nil
			err = fmt.Errorf("span customizer %d panicked", hookIndex)
		}
	}()
	var current sdktrace.ReadOnlySpan = input.snapshot
	for i, customizer := range e.customizers {
		if customizer.OnSpanExport == nil {
			continue
		}
		hookIndex = i
		next, err := customizer.OnSpanExport(current)
		if err != nil {
			// Do not wrap: hook errors may contain span payloads.
			return nil, fmt.Errorf("span customizer %d returned an error", i)
		}
		if nilSpan(next) {
			return nil, fmt.Errorf("span customizer %d returned a nil span", i)
		}
		nextIdentity, nextParent := next.SpanContext(), next.Parent()
		if nextIdentity.TraceID() != input.identity.TraceID() || nextIdentity.SpanID() != input.identity.SpanID() ||
			nextParent.TraceID() != input.parent.TraceID() || nextParent.SpanID() != input.parent.SpanID() {
			return nil, fmt.Errorf("span customizer %d changed span identity or parent IDs", i)
		}
		current = next
	}
	return current, nil
}

// customizerInput is captured before invoking user code, so hooks that mutate
// their input or a caller-owned implementation in place cannot change the
// identity, timing, or routing retained by a stripped record.
type customizerInput struct {
	snapshot         customizerSnapshot
	identity, parent oteltrace.SpanContext
	start, end       time.Time
	routing          attribute.KeyValue
}

func captureCustomizerInput(span sdktrace.ReadOnlySpan) (input customizerInput, err error) {
	captured := false
	defer func() {
		if !captured {
			// Caller-owned span implementations may panic; values may contain secrets.
			_ = recover()
			input, err = customizerInput{}, fmt.Errorf("cannot read span for customization")
		}
	}()
	if nilSpan(span) {
		return customizerInput{}, fmt.Errorf("cannot customize a nil span")
	}
	input.identity, input.parent = span.SpanContext(), span.Parent()
	input.start, input.end = span.StartTime(), span.EndTime()
	input.snapshot, input.routing = newCustomizerSnapshot(span)
	captured = true
	return input, nil
}

// stripped allowlists only identity, timing, and routing. It deliberately does
// not wrap the original span: neither its data nor mutations from earlier hooks
// can escape. A real SDK snapshot keeps ReadOnlySpan methods added by future
// OTel versions returning zero values.
func (input customizerInput) stripped() sdktrace.ReadOnlySpan {
	attrs := make([]attribute.KeyValue, 0, 2)
	if input.routing.Key != "" {
		attrs = append(attrs, input.routing)
	}
	attrs = append(attrs, attribute.String("braintrust.context_json", `{"customizer_error":true}`))
	return tracetest.SpanStub{
		SpanContext: input.identity.WithTraceState(oteltrace.TraceState{}),
		Parent:      input.parent.WithTraceState(oteltrace.TraceState{}),
		StartTime:   input.start,
		EndTime:     input.end,
		Attributes:  attrs,
		Resource:    resource.Empty(),
	}.Snapshot()
}

func nilSpan(span sdktrace.ReadOnlySpan) bool {
	if span == nil {
		return true
	}
	value := reflect.ValueOf(span)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Completed OTel spans expose slices that can share storage with other span
// processors. Isolate those slices (including event/link attributes) so even an
// in-place edit followed by a failed hook cannot change the caller's span data.
// Attribute values, resources, and instrumentation scopes are immutable OTel data.
// Embedding ReadOnlySpan follows the existing transformedSpan wrapper convention.
type customizerSnapshot struct {
	sdktrace.ReadOnlySpan
	attrs  []attribute.KeyValue
	events []sdktrace.Event
	links  []sdktrace.Link
}

// newCustomizerSnapshot also returns the Braintrust routing attribute, found
// while copying attributes to avoid a second scan on the hot export path.
func newCustomizerSnapshot(span sdktrace.ReadOnlySpan) (customizerSnapshot, attribute.KeyValue) {
	original := span.Attributes()
	attrs := make([]attribute.KeyValue, len(original))
	var routing attribute.KeyValue
	for i, attr := range original {
		attrs[i] = attr
		if attr.Key == ParentOtelAttrKey {
			routing = attr
		}
	}
	events := slices.Clone(span.Events())
	for i := range events {
		events[i].Attributes = slices.Clone(events[i].Attributes)
	}
	links := slices.Clone(span.Links())
	for i := range links {
		links[i].Attributes = slices.Clone(links[i].Attributes)
	}
	return customizerSnapshot{
		ReadOnlySpan: span,
		attrs:        attrs,
		events:       events,
		links:        links,
	}, routing
}

func (s customizerSnapshot) Attributes() []attribute.KeyValue { return s.attrs }
func (s customizerSnapshot) Events() []sdktrace.Event         { return s.events }
func (s customizerSnapshot) Links() []sdktrace.Link           { return s.links }
