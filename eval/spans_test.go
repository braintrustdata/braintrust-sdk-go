package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
)

func TestSpans_Integration(t *testing.T) {
	session, apiClient := setupIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()

	_, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	evaluator := NewEvaluator[string, string](session, tp, apiClient, integrationTestProject)
	result, err := evaluator.Run(ctx, Opts[string, string]{
		Experiment: "test-spans",
		Dataset: NewDataset([]Case[string, string]{
			{Input: "hello", Expected: "hello"},
		}),
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{
			NewScorer("spans-test", func(ctx context.Context, tr TaskResult[string, string]) (Scores, error) {
				// Fetch all spans
				spans, err := tr.Spans(ctx)
				require.NoError(t, err)

				// Fetch thread (will be nil for simple task, but should not error)
				thread, err := tr.Thread(ctx)
				require.NoError(t, err)

				t.Logf("spans=%d thread=%d", len(spans), len(thread))

				// Filter by nonexistent type — should return empty
				filtered, err := tr.Spans(ctx, WithSpanTypes("nonexistent"))
				require.NoError(t, err)
				assert.Empty(t, filtered)

				return S(1.0), nil
			}),
		},
		Quiet: true,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestSpans_NilFetcher(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// TaskResult with no fetcher should return nil, nil
	tr := TaskResult[string, string]{
		Input:  "hello",
		Output: "hello",
	}

	spans, err := tr.Spans(ctx)
	assert.NoError(t, err)
	assert.Nil(t, spans)

	thread, err := tr.Thread(ctx)
	assert.NoError(t, err)
	assert.Nil(t, thread)
}
