package trace

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

type customizingExporter struct {
	sdktrace.SpanExporter
	customizers []config.SpanCustomizer
	logger      logger.Logger
}

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
	outgoing := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		customized, err := e.customize(span)
		if err != nil {
			err = fmt.Errorf("span customization failed for batch span %d: %w", i, err)
			e.logger.Error("span customization failed; batch not exported", "error", err)
			return err
		}
		outgoing[i] = customized
	}
	// Never mutate the caller's batch, and do not send anything until every
	// customizer has succeeded for every span. Transport retries reuse outgoing.
	return e.SpanExporter.ExportSpans(ctx, outgoing)
}

func (e *customizingExporter) customize(span sdktrace.ReadOnlySpan) (result sdktrace.ReadOnlySpan, err error) {
	hookIndex := -1
	defer func() {
		if recovered := recover(); recovered != nil || (result == nil && err == nil) {
			// Panic values may contain secrets. Also handle legacy panic(nil).
			result = nil
			err = fmt.Errorf("span customizer %d panicked", hookIndex)
		}
	}()
	if nilSpan(span) {
		return nil, fmt.Errorf("cannot customize a nil span")
	}
	identity, parent := span.SpanContext(), span.Parent()
	current := newCustomizerSnapshot(span)
	for i, customizer := range e.customizers {
		if customizer.OnSpanExport == nil {
			continue
		}
		hookIndex = i
		next, err := customizer.OnSpanExport(current)
		if err != nil {
			return nil, fmt.Errorf("span customizer %d: %w", i, err)
		}
		if nilSpan(next) {
			return nil, fmt.Errorf("span customizer %d returned a nil span", i)
		}
		nextIdentity, nextParent := next.SpanContext(), next.Parent()
		if nextIdentity.TraceID() != identity.TraceID() || nextIdentity.SpanID() != identity.SpanID() ||
			nextParent.TraceID() != parent.TraceID() || nextParent.SpanID() != parent.SpanID() {
			return nil, fmt.Errorf("span customizer %d changed span identity or parent IDs", i)
		}
		current = next
	}
	return current, nil
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

func newCustomizerSnapshot(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
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
		attrs:        slices.Clone(span.Attributes()),
		events:       events,
		links:        links,
	}
}

func (s customizerSnapshot) Attributes() []attribute.KeyValue { return s.attrs }
func (s customizerSnapshot) Events() []sdktrace.Event         { return s.events }
func (s customizerSnapshot) Links() []sdktrace.Link           { return s.links }
