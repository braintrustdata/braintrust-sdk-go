package genkit

import (
	"context"
	"errors"
	"testing"

	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestFlowErrorRecordsInputOnly(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	type FlowInput struct {
		Value string `json:"value"`
	}
	wantErr := errors.New("flow failed")
	g := genkit.Init(context.Background())
	flow := DefineFlow(g, "failing_flow", func(context.Context, FlowInput) (string, error) {
		return "", wantErr
	})

	_, err := flow.Run(context.Background(), FlowInput{Value: "test"})
	require.ErrorIs(t, err, wantErr)

	span := requireSpanNamed(t, exporter.Flush(), "failing_flow")
	assert.Equal(t, map[string]any{"value": "test"}, span.Input())
	assert.False(t, span.HasAttr("braintrust.output_json"))
	assert.Equal(t, codes.Error, span.Status().Code)
}

func TestStreamingFlowRecordsInputAndOutput(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	g := genkit.Init(context.Background())
	flow := DefineStreamingFlow(g, "counting_flow",
		func(ctx context.Context, n int, cb func(context.Context, int) error) ([]int, error) {
			var out []int
			for i := range n {
				if cb != nil {
					if err := cb(ctx, i); err != nil {
						return nil, err
					}
				}
				out = append(out, i)
			}
			return out, nil
		},
	)

	var streamed []int
	for value, err := range flow.Stream(context.Background(), 3) {
		require.NoError(t, err)
		if value.Done {
			assert.Equal(t, []int{0, 1, 2}, value.Output)
		} else {
			streamed = append(streamed, value.Stream)
		}
	}
	assert.Equal(t, []int{0, 1, 2}, streamed)

	span := requireSpanNamed(t, exporter.Flush(), "counting_flow")
	assert.Equal(t, float64(3), span.Input())
	assert.Equal(t, []any{float64(0), float64(1), float64(2)}, span.Output())
}
