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
	return firebasegenkit.DefineFlow(g, name, wrapFlowFunc(fn))
}

// DefineStreamingFlow defines a traced Genkit streaming flow. It is a drop-in
// replacement for genkit.DefineStreamingFlow.
func DefineStreamingFlow[In, Out, Stream any](
	g *firebasegenkit.Genkit,
	name string,
	fn core.StreamingFunc[In, Out, Stream],
) *core.Flow[In, Out, Stream] {
	return firebasegenkit.DefineStreamingFlow(g, name, wrapStreamingFlowFunc(fn))
}

// NewFlow creates a traced, unregistered Genkit flow. It is a drop-in
// replacement for genkit.NewFlow.
func NewFlow[In, Out any](name string, fn core.Func[In, Out]) *core.Flow[In, Out, struct{}] {
	return firebasegenkit.NewFlow(name, wrapFlowFunc(fn))
}

// NewStreamingFlow creates a traced, unregistered Genkit streaming flow. It is
// a drop-in replacement for genkit.NewStreamingFlow.
func NewStreamingFlow[In, Out, Stream any](
	name string,
	fn core.StreamingFunc[In, Out, Stream],
) *core.Flow[In, Out, Stream] {
	return firebasegenkit.NewStreamingFlow(name, wrapStreamingFlowFunc(fn))
}

func wrapFlowFunc[In, Out any](fn core.Func[In, Out]) core.Func[In, Out] {
	return func(ctx context.Context, input In) (Out, error) {
		return recordFlow(ctx, input, func() (Out, error) { return fn(ctx, input) })
	}
}

func wrapStreamingFlowFunc[In, Out, Stream any](
	fn core.StreamingFunc[In, Out, Stream],
) core.StreamingFunc[In, Out, Stream] {
	return func(ctx context.Context, input In, cb core.StreamCallback[Stream]) (Out, error) {
		return recordFlow(ctx, input, func() (Out, error) { return fn(ctx, input, cb) })
	}
}

// recordFlow records input and output on the flow span Genkit started for this
// call. Genkit records errors on that span itself, so only data is added here.
func recordFlow[Out any](ctx context.Context, input any, call func() (Out, error)) (Out, error) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return call()
	}
	_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
	output, err := call()
	if err == nil {
		_ = internal.SetJSONAttr(span, "braintrust.output_json", output)
	}
	return output, err
}
