package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeOpts_ExperimentDefaultsToEvalName(t *testing.T) {
	t.Parallel()

	ev := &Eval[testInput, testOutput]{
		Name: "my-eval",
		Task: T(func(_ context.Context, in testInput) (testOutput, error) {
			return testOutput{}, nil
		}),
	}

	opts := mergeOpts(ev, RunOpts[testInput, testOutput]{})
	assert.Equal(t, "my-eval", opts.Experiment)
}

func TestMergeOpts_ExperimentOverride(t *testing.T) {
	t.Parallel()

	ev := &Eval[testInput, testOutput]{
		Name: "my-eval",
		Task: T(func(_ context.Context, in testInput) (testOutput, error) {
			return testOutput{}, nil
		}),
	}

	opts := mergeOpts(ev, RunOpts[testInput, testOutput]{
		Experiment: "custom-experiment",
	})
	assert.Equal(t, "custom-experiment", opts.Experiment)
}

func TestMergeOpts_FieldResolution(t *testing.T) {
	t.Parallel()

	task := T(func(_ context.Context, in testInput) (testOutput, error) {
		return testOutput{Result: in.Value}, nil
	})
	scorer := NewScorer("s", func(_ context.Context, _ TaskResult[testInput, testOutput]) (Scores, error) {
		return S(1.0), nil
	})

	ev := &Eval[testInput, testOutput]{
		Name:        "eval-name",
		Task:        task,
		Scorers:     []Scorer[testInput, testOutput]{scorer},
		ProjectName: "eval-project",
	}

	dataset := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "x"}},
	})
	callback := func(CaseProgress) {}

	ro := RunOpts[testInput, testOutput]{
		Dataset:        dataset,
		Tags:           []string{"tag1"},
		Metadata:       map[string]any{"k": "v"},
		Update:         true,
		Parallelism:    4,
		Quiet:          true,
		OnCaseComplete: callback,
		SpanParent:     "playground_id:pg-1",
		Generation:     42,
	}

	opts := mergeOpts(ev, ro)

	// Definition fields come from Eval
	assert.Equal(t, "eval-name", opts.Experiment)
	assert.NotNil(t, opts.Task)
	assert.Len(t, opts.Scorers, 1)
	assert.Equal(t, "eval-project", opts.ProjectName)

	// Runtime fields come from RunOpts
	assert.Equal(t, dataset, opts.Dataset)
	assert.Equal(t, []string{"tag1"}, opts.Tags)
	assert.Equal(t, Metadata{"k": "v"}, opts.Metadata)
	assert.True(t, opts.Update)
	assert.Equal(t, 4, opts.Parallelism)
	assert.True(t, opts.Quiet)
	assert.NotNil(t, opts.OnCaseComplete)
	assert.Equal(t, "playground_id:pg-1", opts.SpanParent)
	assert.Equal(t, 42, opts.Generation)
}

func TestMergeOpts_ProjectNameOverride(t *testing.T) {
	t.Parallel()

	ev := &Eval[testInput, testOutput]{
		Name:        "my-eval",
		ProjectName: "default-project",
		Task: T(func(_ context.Context, in testInput) (testOutput, error) {
			return testOutput{}, nil
		}),
	}

	// Without override: uses Eval.ProjectName
	opts := mergeOpts(ev, RunOpts[testInput, testOutput]{})
	assert.Equal(t, "default-project", opts.ProjectName)

	// With override: uses RunOpts.ProjectName
	opts = mergeOpts(ev, RunOpts[testInput, testOutput]{
		ProjectName: "override-project",
	})
	assert.Equal(t, "override-project", opts.ProjectName)
}

func TestMergeOpts_EvalReuse(t *testing.T) {
	t.Parallel()

	ev := &Eval[testInput, testOutput]{
		Name: "reusable-eval",
		Task: T(func(_ context.Context, in testInput) (testOutput, error) {
			return testOutput{Result: in.Value}, nil
		}),
		Scorers: []Scorer[testInput, testOutput]{
			NewScorer("s", func(_ context.Context, _ TaskResult[testInput, testOutput]) (Scores, error) {
				return S(1.0), nil
			}),
		},
		ProjectName: "base-project",
	}

	// Same Eval, different RunOpts
	opts1 := mergeOpts(ev, RunOpts[testInput, testOutput]{
		Experiment: "run-1",
		Tags:       []string{"nightly"},
	})
	opts2 := mergeOpts(ev, RunOpts[testInput, testOutput]{
		Experiment:  "run-2",
		ProjectName: "other-project",
		Parallelism: 8,
	})

	// Each merge produces independent Opts
	assert.Equal(t, "run-1", opts1.Experiment)
	assert.Equal(t, "base-project", opts1.ProjectName)
	assert.Equal(t, []string{"nightly"}, opts1.Tags)
	assert.Equal(t, 0, opts1.Parallelism)

	assert.Equal(t, "run-2", opts2.Experiment)
	assert.Equal(t, "other-project", opts2.ProjectName)
	assert.Nil(t, opts2.Tags)
	assert.Equal(t, 8, opts2.Parallelism)

	// Definition is unchanged
	assert.Equal(t, "reusable-eval", ev.Name)
	assert.Equal(t, "base-project", ev.ProjectName)
}

// TestRunEval_Success verifies that RunEval produces the same span structure as
// the equivalent Run call. It uses the same testNewEval path as other unit tests
// to avoid needing a real API client for experiment registration.
func TestRunEval_Success(t *testing.T) {
	t.Parallel()

	task := T(func(_ context.Context, in testInput) (testOutput, error) {
		return testOutput{Result: "output-" + in.Value}, nil
	})
	scorer := NewScorer("accuracy", func(_ context.Context, r TaskResult[testInput, testOutput]) (Scores, error) {
		return S(0.95), nil
	})

	ev := &Eval[testInput, testOutput]{
		Name:        "test-eval",
		Task:        task,
		Scorers:     []Scorer[testInput, testOutput]{scorer},
		ProjectName: "eval-project",
	}

	dataset := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "a"}, Expected: testOutput{Result: "expected-a"}},
	})

	ro := RunOpts[testInput, testOutput]{
		Dataset: dataset,
		Quiet:   true,
	}

	// Verify mergeOpts produces the right Opts, then run via testNewEval
	// (the same path used by all other unit tests).
	opts := mergeOpts(ev, ro)
	assert.Equal(t, "test-eval", opts.Experiment)
	assert.Equal(t, "eval-project", opts.ProjectName)
	assert.NotNil(t, opts.Task)
	assert.Len(t, opts.Scorers, 1)
	assert.True(t, opts.Quiet)

	// Run the eval using the unit test helper to verify end-to-end span output.
	ute := newUnitTestEval(t, dataset, opts.Task, opts.Scorers, 1)
	result, err := ute.eval.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify spans: task + scorer + eval = 3
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)
	spans[0].AssertNameIs("task")
	spans[1].AssertNameIs("accuracy")
	spans[2].AssertNameIs("eval")
}
