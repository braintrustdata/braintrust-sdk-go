package trace

import (
	"encoding/json"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/attachmentprocessor"
)

// normalizeGenkitFlow exposes Genkit's already-serialized flow data to Braintrust.
// Restrict the conversion to Genkit flow actions; model and tool spans may have
// their own instrumentation and must not be overwritten.
func normalizeGenkitFlow(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	if span.InstrumentationScope().Name != "genkit-tracer" {
		return span
	}
	var actionType, subtype string
	for _, item := range span.Attributes() {
		if item.Value.Type() != attribute.STRING {
			continue
		}
		switch item.Key {
		case "genkit:type":
			actionType = item.Value.AsString()
		case "genkit:metadata:subtype":
			subtype = item.Value.AsString()
		}
	}
	if actionType != "action" || subtype != "flow" {
		return span
	}
	values := make(map[attribute.Key]attribute.Value)
	for _, item := range span.Attributes() {
		values[item.Key] = item.Value
	}
	overrides := make(map[attribute.Key]string)
	for source, target := range map[attribute.Key]attribute.Key{"genkit:input": "braintrust.input_json", "genkit:output": "braintrust.output_json"} {
		if _, exists := values[target]; exists {
			continue
		}
		if value, ok := values[source]; ok && value.Type() == attribute.STRING && json.Valid([]byte(value.AsString())) {
			overrides[target] = value.AsString()
		}
	}
	if len(overrides) == 0 {
		return span
	}
	return attachmentprocessor.NewTransformedSpan(span, overrides)
}
