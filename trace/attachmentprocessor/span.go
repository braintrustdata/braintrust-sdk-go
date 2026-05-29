package attachmentprocessor

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// transformedSpan wraps a ReadOnlySpan and overrides its attributes.
// This is the Go equivalent of Java's TransformedReadableSpan.
//
// The private() method is satisfied by embedding the original ReadOnlySpan
// (the same technique used by otel/sdk/trace/tracetest.spanSnapshot).
type transformedSpan struct {
	// Embed the original to satisfy the private() interface method.
	sdktrace.ReadOnlySpan

	attrs []attribute.KeyValue
}

// NewTransformedSpan creates a transformedSpan that overrides the given
// attribute keys with new values. All other attributes are preserved.
// Override entries for keys not present in the original span are appended
// to the attribute list rather than silently dropped.
func NewTransformedSpan(delegate sdktrace.ReadOnlySpan, overrides map[attribute.Key]string) sdktrace.ReadOnlySpan {
	origAttrs := delegate.Attributes()
	newAttrs := make([]attribute.KeyValue, 0, len(origAttrs)+len(overrides))
	seen := make(map[attribute.Key]bool, len(overrides))
	for _, a := range origAttrs {
		if v, ok := overrides[a.Key]; ok {
			newAttrs = append(newAttrs, attribute.String(string(a.Key), v))
			seen[a.Key] = true
		} else {
			newAttrs = append(newAttrs, a)
		}
	}
	// Append overrides for keys that weren't already on the span.
	for k, v := range overrides {
		if !seen[k] {
			newAttrs = append(newAttrs, attribute.String(string(k), v))
		}
	}
	return transformedSpan{
		ReadOnlySpan: delegate,
		attrs:        newAttrs,
	}
}

// Override all methods of ReadOnlySpan to avoid nil-pointer dereferences
// from the embedded interface (which could be nil in degenerate cases).
// The delegate methods forward to the embedded span; Attributes() returns
// the overridden slice.

func (s transformedSpan) Name() string                     { return s.ReadOnlySpan.Name() }
func (s transformedSpan) SpanContext() trace.SpanContext   { return s.ReadOnlySpan.SpanContext() }
func (s transformedSpan) Parent() trace.SpanContext        { return s.ReadOnlySpan.Parent() }
func (s transformedSpan) SpanKind() trace.SpanKind         { return s.ReadOnlySpan.SpanKind() }
func (s transformedSpan) StartTime() time.Time             { return s.ReadOnlySpan.StartTime() }
func (s transformedSpan) EndTime() time.Time               { return s.ReadOnlySpan.EndTime() }
func (s transformedSpan) Attributes() []attribute.KeyValue { return s.attrs }
func (s transformedSpan) Links() []sdktrace.Link           { return s.ReadOnlySpan.Links() }
func (s transformedSpan) Events() []sdktrace.Event         { return s.ReadOnlySpan.Events() }
func (s transformedSpan) Status() sdktrace.Status          { return s.ReadOnlySpan.Status() }
func (s transformedSpan) DroppedAttributes() int           { return s.ReadOnlySpan.DroppedAttributes() }
func (s transformedSpan) DroppedLinks() int                { return s.ReadOnlySpan.DroppedLinks() }
func (s transformedSpan) DroppedEvents() int               { return s.ReadOnlySpan.DroppedEvents() }
func (s transformedSpan) ChildSpanCount() int              { return s.ReadOnlySpan.ChildSpanCount() }
func (s transformedSpan) Resource() *resource.Resource     { return s.ReadOnlySpan.Resource() }
func (s transformedSpan) InstrumentationScope() instrumentation.Scope {
	return s.ReadOnlySpan.InstrumentationScope()
}

//nolint:staticcheck // Required by ReadOnlySpan interface for backward compatibility.
func (s transformedSpan) InstrumentationLibrary() instrumentation.Library {
	return s.ReadOnlySpan.InstrumentationLibrary()
}
