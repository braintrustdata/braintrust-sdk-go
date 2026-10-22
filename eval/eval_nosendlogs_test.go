package eval

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/evalhooks"
	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// A NoSendLogs run must work with no session and no API client at all -- that
// is what lets `bt eval --no-send-logs` run offline without credentials -- and
// must neither register an experiment nor hand spans to the evaluator's
// tracer provider, which in normal use exports to Braintrust.
func TestRun_NoSendLogsRunsLocally(t *testing.T) {
	t.Parallel()

	tp, exporter := oteltest.Setup(t)
	sdkTP, ok := tp.(*sdktrace.TracerProvider)
	require.True(t, ok)

	// No session, no API client: any attempt to reach Braintrust panics.
	evaluator := NewEvaluatorWithClient[testInput, testOutput](nil, sdkTP, nil, "local-project")

	var mu sync.Mutex
	var scores []map[string]float64
	var spanIDs []string
	experimentStarted := false

	result, err := evaluator.Run(context.Background(), Opts[testInput, testOutput]{
		Experiment: "local-experiment",
		Dataset: NewDataset([]Case[testInput, testOutput]{
			{Input: testInput{Value: "a"}, Expected: testOutput{Result: "a"}},
			{Input: testInput{Value: "b"}, Expected: testOutput{Result: "nope"}},
		}),
		Task: T(func(_ context.Context, in testInput) (testOutput, error) {
			return testOutput{Result: in.Value}, nil
		}),
		Scorers: []Scorer[testInput, testOutput]{
			NewScorer("exact", func(_ context.Context, r TaskResult[testInput, testOutput]) (Scores, error) {
				if r.Output == r.Expected {
					return S(1), nil
				}
				return S(0), nil
			}),
		},
		Quiet:      true,
		NoSendLogs: true,
		Hooks: &evalhooks.Hooks{
			OnCaseComplete: func(cp evalhooks.CaseProgress) {
				mu.Lock()
				defer mu.Unlock()
				scores = append(scores, cp.Scores)
				spanIDs = append(spanIDs, cp.ID)
			},
			OnExperimentStart: func(evalhooks.ExperimentInfo) { experimentStarted = true },
		},
	})
	require.NoError(t, err)

	// The cases still ran and were scored.
	assert.ElementsMatch(t, []map[string]float64{{"exact": 1}, {"exact": 0}}, scores)
	for _, id := range spanIDs {
		assert.NotEqual(t, "0000000000000000", id, "each case still reports a span ID")
	}

	// There is no experiment to point at.
	assert.False(t, experimentStarted, "no experiment exists, so none can start")
	assert.Empty(t, result.ID())
	link, err := result.Permalink()
	require.NoError(t, err)
	assert.Empty(t, link)
	assert.Equal(t, "local-experiment", result.Name())
	assert.Equal(t, "local-project", result.ProjectName())

	// Nothing reached the evaluator's (exporting) tracer provider.
	assert.Empty(t, exporter.Flush(), "no spans may be handed to the exporting tracer provider")
}
