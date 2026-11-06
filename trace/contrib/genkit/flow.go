package genkit

import (
	"context"

	"github.com/firebase/genkit/go/core"
	firebasegenkit "github.com/firebase/genkit/go/genkit"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// DefineFlow defines a Genkit flow whose input and output are recorded on the
// flow span. It is a drop-in replacement for genkit.DefineFlow.
func DefineFlow[In, Out any](
	g *firebasegenkit.Genkit,
	name string,
	fn core.Func[In, Out],
) *core.Flow[In, Out, struct{}] {
	return firebasegenkit.DefineFlow(g, name, func(ctx context.Context, input In) (Out, error) {
		span := startFlowSpan(ctx, input)
		output, err := fn(ctx, input)
		finishFlowSpan(span, output, err)
		return output, err
	})
}

// DefineStreamingFlow defines a traced Genkit streaming flow. It is a drop-in
// replacement for genkit.DefineStreamingFlow.
func DefineStreamingFlow[In, Out, Stream any](
	g *firebasegenkit.Genkit,
	name string,
	fn core.StreamingFunc[In, Out, Stream],
) *core.Flow[In, Out, Stream] {
	return firebasegenkit.DefineStreamingFlow(g, name,
		func(ctx context.Context, input In, cb core.StreamCallback[Stream]) (Out, error) {
			span := startFlowSpan(ctx, input)
			output, err := fn(ctx, input, cb)
			finishFlowSpan(span, output, err)
			return output, err
		},
	)
}

// startFlowSpan records the input on the flow span Genkit started for this
// call. Genkit records errors on that span itself, so only data is added here.
func startFlowSpan(ctx context.Context, input any) trace.Span {
	span := trace.SpanFromContext(ctx)
	_ = internal.SetJSONAttr(span, "braintrust.input_json", normalizeJSON(input))
	return span
}

func finishFlowSpan(span trace.Span, output any, err error) {
	if err == nil {
		_ = internal.SetJSONAttr(span, "braintrust.output_json", normalizeJSON(output))
	}
}
