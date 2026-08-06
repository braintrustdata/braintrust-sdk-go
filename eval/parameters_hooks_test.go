package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The task must receive the run's resolved parameters via hooks.
func TestTaskFunc_ReceivesParameters(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	var captured Parameters
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		captured = hooks.Parameters
		return TaskOutput[testOutput]{Value: testOutput{Result: "ok"}}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)
	// Simulate what the run path injects (RunOpts.Parameters -> eval.parameters).
	ute.eval.parameters = Parameters{"model": "gpt-4o", "max_length": 100.0}

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	require.NotNil(t, captured, "hooks.Parameters should be set")
	assert.Equal(t, "gpt-4o", captured.String("model"))
	assert.Equal(t, 100, captured.Int("max_length"))
}

// TaskWithHooks adapts a (ctx, input, hooks) -> (R, error) func into a TaskFunc,
// giving fluent access to parameters without manual TaskOutput wrapping.
func TestTaskWithHooks_ReadsParameters(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}},
	})

	var seenModel string
	task := TaskWithHooks(func(ctx context.Context, input testInput, hooks *TaskHooks) (testOutput, error) {
		seenModel = hooks.Parameters.String("model")
		return testOutput{Result: input.Value + ":" + seenModel}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)
	ute.eval.parameters = Parameters{"model": "rule-based"}

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "rule-based", seenModel)
}
