package eval

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

// testInput and testOutput are simple types for testing
type testInput struct {
	Value string `json:"value"`
}

type testOutput struct {
	Result string `json:"result"`
}

// unitTestEval wraps eval with testing utilities
type unitTestEval[I, R any] struct {
	eval     *eval[I, R]
	exporter *oteltest.Exporter
}

// newUnitTestEval creates a fully configured eval for unit testing with fake data.
// It generates its own fake session, config, tracer, experiment/project IDs, etc.
func newUnitTestEval[I, R any](t *testing.T, cases Cases[I, R], task TaskFunc[I, R], scorers []Scorer[I, R], parallelism int) *unitTestEval[I, R] {
	t.Helper()

	// Create test tracer and exporter using oteltest
	tracer, exporter := oteltest.Setup(t)

	// Create fake session with test data
	session := tests.NewSession(t)

	// Create test config
	cfg := &config.Config{
		AppURL:             "https://test.braintrust.dev",
		OrgName:            "test-org",
		DefaultProjectName: "test-project",
	}

	// Create eval with fake IDs
	e := testNewEval(
		cfg,
		session,
		tracer,
		"exp-12345678",    // fake experiment ID
		"test-experiment", // fake experiment name
		"proj-87654321",   // fake project ID
		"test-project",    // fake project name
		cases,
		task,
		scorers,
		parallelism,
	)

	return &unitTestEval[I, R]{
		eval:     e,
		exporter: exporter,
	}
}

// simpleScorer is a test scorer that returns a fixed score
type simpleScorer struct {
	name  string
	score float64
	meta  map[string]interface{}
	err   error
}

func (s *simpleScorer) Name() string {
	return s.name
}

func (s *simpleScorer) Run(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
	if s.err != nil {
		return nil, s.err
	}
	return Scores{{
		Name:     s.name,
		Score:    s.score,
		Metadata: s.meta,
	}}, nil
}

func TestNewEval_Success(t *testing.T) {
	t.Parallel()

	// Create test cases with tags and metadata
	cases := NewCases([]Case[testInput, testOutput]{
		{
			Input:    testInput{Value: "test1"},
			Expected: testOutput{Result: "expected1"},
			Tags:     []string{"tag1", "tag2"},
			Metadata: map[string]interface{}{"key": "value"},
		},
		{
			Input:    testInput{Value: "test2"},
			Expected: testOutput{Result: "expected2"},
		},
	})

	// Create test task
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output-" + input.Value}, nil
	})

	// Create test scorers
	scorers := []Scorer[testInput, testOutput]{
		&simpleScorer{name: "accuracy", score: 0.95, meta: map[string]interface{}{"note": "good"}},
	}

	// Create eval
	ute := newUnitTestEval(t, cases, task, scorers, 1)

	// Verify eval was created correctly
	assert.NotNil(t, ute.eval)
	assert.Equal(t, "exp-12345678", ute.eval.experimentID)
	assert.Equal(t, "test-experiment", ute.eval.experimentName)
	assert.Equal(t, "proj-87654321", ute.eval.projectID)
	assert.Equal(t, "test-project", ute.eval.projectName)
	assert.Equal(t, 1, ute.eval.goroutines)
	assert.NotNil(t, ute.eval.tracer)
	assert.NotNil(t, ute.eval.startSpanOpt)

	// Verify permalink generation
	permalink := ute.eval.permalink()
	assert.Equal(t, "https://test.braintrust.dev/app/test-org/object?object_type=experiment&object_id=exp-12345678", permalink)

	// Run the eval and verify span structure
	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify all spans were created with correct structure
	// Spans are in completion order: task, score, eval for each case
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6) // 2 cases * (task + score + eval) = 6 spans

	// First case spans (in completion order: task, score, eval)
	spans[0].AssertEqual(oteltest.TestSpan{
		Name: "task",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.input_json":      map[string]any{"value": "test1"},
			"braintrust.expected":        map[string]any{"result": "expected1"},
			"braintrust.output_json":     map[string]any{"result": "output-test1"},
			"braintrust.span_attributes": map[string]any{"type": "task"},
		},
	})

	spans[1].AssertEqual(oteltest.TestSpan{
		Name: "score",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.span_attributes": map[string]any{"type": "score"},
			"braintrust.scores":          map[string]any{"accuracy": 0.95},
			"braintrust.metadata":        map[string]any{"note": "good"},
			"braintrust.output":          map[string]any{"score": 0.95},
		},
	})

	spans[2].AssertEqual(oteltest.TestSpan{
		Name: "eval",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
			"braintrust.tags":   []string{"tag1", "tag2"},
		},
		JSONAttrs: map[string]any{
			"braintrust.input_json":      map[string]any{"value": "test1"},
			"braintrust.output_json":     map[string]any{"result": "output-test1"},
			"braintrust.expected":        map[string]any{"result": "expected1"},
			"braintrust.metadata":        map[string]any{"key": "value"},
			"braintrust.span_attributes": map[string]any{"type": "eval"},
		},
	})

	// Second case spans (no tags or metadata)
	spans[3].AssertEqual(oteltest.TestSpan{
		Name: "task",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.input_json":      map[string]any{"value": "test2"},
			"braintrust.expected":        map[string]any{"result": "expected2"},
			"braintrust.output_json":     map[string]any{"result": "output-test2"},
			"braintrust.span_attributes": map[string]any{"type": "task"},
		},
	})

	spans[4].AssertEqual(oteltest.TestSpan{
		Name: "score",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.span_attributes": map[string]any{"type": "score"},
			"braintrust.scores":          map[string]any{"accuracy": 0.95},
			"braintrust.metadata":        map[string]any{"note": "good"},
			"braintrust.output":          map[string]any{"score": 0.95},
		},
	})

	spans[5].AssertEqual(oteltest.TestSpan{
		Name: "eval",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.input_json":      map[string]any{"value": "test2"},
			"braintrust.output_json":     map[string]any{"result": "output-test2"},
			"braintrust.expected":        map[string]any{"result": "expected2"},
			"braintrust.span_attributes": map[string]any{"type": "eval"},
		},
	})
}

func TestNewEval_Parallelism(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 4)
	assert.Equal(t, 4, ute.eval.goroutines)
}

func TestNewEval_DefaultParallelism(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// Test with 0
	ute := newUnitTestEval(t, cases, task, nil, 0)
	assert.Equal(t, 1, ute.eval.goroutines)

	// Test with negative
	ute2 := newUnitTestEval(t, cases, task, nil, -5)
	assert.Equal(t, 1, ute2.eval.goroutines)
}

func TestEval_Run_TaskError(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "error"}},
		{Input: testInput{Value: "test2"}},
	})

	// Task that fails on specific input
	taskErr := errors.New("task failed")
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		if input.Value == "error" {
			return testOutput{}, taskErr
		}
		return testOutput{Result: "ok-" + input.Value}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return error but continue processing other cases
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task failed")
	assert.NotNil(t, result)

	// Verify spans (in completion order: task, score, eval per case)
	// Score span is only created when task succeeds
	spans := ute.exporter.Flush()
	require.Len(t, spans, 8) // First: 3 spans, Second: 2 spans (no score), Third: 3 spans

	// First case succeeds (task, score, eval)
	spans[0].AssertNameIs("task")
	spans[1].AssertNameIs("score")
	spans[2].AssertNameIs("eval")

	// Second case fails (task with error, then eval with error - NO score span)
	spans[3].AssertNameIs("task")
	assert.Equal(t, codes.Error, spans[3].Status().Code)
	events := spans[3].Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)

	spans[4].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[4].Status().Code)

	// Third case succeeds (task, score, eval)
	spans[5].AssertNameIs("task")
	spans[6].AssertNameIs("score")
	spans[7].AssertNameIs("eval")
}

func TestEval_Run_ScorerError(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// Scorers: one succeeds, one fails
	scorerErr := errors.New("scorer failed")
	scorers := []Scorer[testInput, testOutput]{
		&simpleScorer{name: "good-scorer", score: 0.8},
		&simpleScorer{name: "bad-scorer", err: scorerErr},
		&simpleScorer{name: "another-good-scorer", score: 0.9},
	}

	ute := newUnitTestEval(t, cases, task, scorers, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return error but continue processing
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scorer failed")
	assert.NotNil(t, result)

	// Verify spans (in completion order: task, score, eval per case)
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6) // 2 cases * (task + score + eval) = 6 spans

	// First case (task succeeds, scorer partially fails, eval fails)
	spans[0].AssertNameIs("task")

	// Score span should have error status and only successful scores recorded
	spans[1].AssertNameIs("score")
	assert.Equal(t, codes.Error, spans[1].Status().Code)
	spans[1].AssertJSONAttrEquals("braintrust.scores", map[string]any{
		"good-scorer":         0.8,
		"another-good-scorer": 0.9,
	})
	spans[1].AssertJSONAttrEquals("braintrust.output", map[string]any{
		"good-scorer":         map[string]any{"score": 0.8},
		"another-good-scorer": map[string]any{"score": 0.9},
	})
	events := spans[1].Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)

	spans[2].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[2].Status().Code)

	// Second case (same pattern)
	spans[3].AssertNameIs("task")
	spans[4].AssertNameIs("score")
	assert.Equal(t, codes.Error, spans[4].Status().Code)
	spans[5].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[5].Status().Code)
}

func TestEval_Run_PrintsSummary(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)
	// Set quiet to false to enable summary printing
	ute.eval.quiet = false

	// Capture output by providing a custom writer
	// For now, just verify the result has expected fields for String()
	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify String() produces expected output
	summary := result.String()
	assert.Contains(t, summary, "=== Experiment: test-experiment ===")
	assert.Contains(t, summary, "Name: test-experiment")
	assert.Contains(t, summary, "Project: test-project")
	assert.Contains(t, summary, "Duration:")
	assert.Contains(t, summary, "Link: https://test.braintrust.dev/app/test-org/object?object_type=experiment&object_id=exp-12345678")
}

func TestEval_Run_QuietSuppressesSummary(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)
	// Ensure quiet is true (should be default from testNewEval)
	assert.True(t, ute.eval.quiet, "quiet should be true by default in test helper")

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// When quiet is true, summary should not be printed
	// We can't easily capture stdout in a test, but we verify:
	// 1. quiet is set correctly
	// 2. The result object is still valid
	summary := result.String()
	assert.NotEmpty(t, summary, "result should still have a valid String() representation")
}

func TestTaskFunc_ReceivesTaskHooks(t *testing.T) {
	t.Parallel()

	// Create test case with metadata, tags, and expected value
	cases := NewCases([]Case[testInput, testOutput]{
		{
			Input:    testInput{Value: "test"},
			Expected: testOutput{Result: "expected-result"},
			Tags:     []string{"tag1", "tag2"},
			Metadata: map[string]interface{}{"meta-key": "meta-value"},
		},
	})

	// Track what the task receives via hooks
	var capturedExpected any
	var capturedMetadata Metadata
	var capturedTags []string
	var capturedTaskSpan, capturedEvalSpan bool

	// Task using new TaskFunc signature with TaskHooks
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		// Capture all fields from hooks
		capturedExpected = hooks.Expected
		capturedMetadata = hooks.Metadata
		capturedTags = hooks.Tags
		capturedTaskSpan = hooks.TaskSpan != nil
		capturedEvalSpan = hooks.EvalSpan != nil

		result := testOutput{Result: "output-" + input.Value}
		return TaskOutput[testOutput]{Value: result}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify all hook fields were properly set
	assert.NotNil(t, capturedExpected, "Expected should be set in hooks")
	expectedOutput, ok := capturedExpected.(testOutput)
	require.True(t, ok, "Expected should be castable to testOutput")
	assert.Equal(t, "expected-result", expectedOutput.Result)

	assert.Equal(t, Metadata{"meta-key": "meta-value"}, capturedMetadata, "Metadata should match case metadata")
	assert.Equal(t, []string{"tag1", "tag2"}, capturedTags, "Tags should match case tags")
	assert.True(t, capturedTaskSpan, "TaskSpan should be set")
	assert.True(t, capturedEvalSpan, "EvalSpan should be set")

	// Verify consistent span structure (task + score + eval)
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + score + eval
	spans[0].AssertNameIs("task")
	spans[1].AssertNameIs("score")
	spans[2].AssertNameIs("eval")
}

func TestTaskFunc_ModifyTaskSpan(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	// Task that adds custom attributes to TaskSpan
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		// Add custom attributes to the task span
		hooks.TaskSpan.SetAttributes(
			attribute.String("custom.task.attribute", "task-value"),
			attribute.Int("custom.task.count", 42),
		)

		result := testOutput{Result: "output"}
		return TaskOutput[testOutput]{Value: result}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the custom attributes appear on the task span
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + score + eval

	// Find the task span
	taskSpan := spans[0]
	taskSpan.AssertNameIs("task")
	taskSpan.AssertAttrEquals("custom.task.attribute", "task-value")
	taskSpan.AssertAttrEquals("custom.task.count", int64(42))
}

func TestTaskFunc_ModifyEvalSpan(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	// Task that adds custom attributes to EvalSpan (parent case span)
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		// Add custom attributes to the eval/case span
		hooks.EvalSpan.SetAttributes(
			attribute.String("custom.eval.attribute", "eval-value"),
			attribute.String("custom.model", "gpt-4"),
		)

		result := testOutput{Result: "output"}
		return TaskOutput[testOutput]{Value: result}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the custom attributes appear on the eval span
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + score + eval

	// Find the eval span
	evalSpan := spans[2]
	evalSpan.AssertNameIs("eval")
	evalSpan.AssertAttrEquals("custom.eval.attribute", "eval-value")
	evalSpan.AssertAttrEquals("custom.model", "gpt-4")
}

func TestTaskFunc_ReturnsTaskResult(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	// Task returns TaskOutput[R] with the value
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		result := testOutput{Result: "processed-" + input.Value}
		return TaskOutput[testOutput]{Value: result}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the TaskResult.Value is properly extracted and used
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6) // 2 cases * (task + score + eval) = 6 spans

	// First case - verify output matches TaskResult.Value
	spans[0].AssertNameIs("task")
	spans[0].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "processed-test1",
	})

	// Second case
	spans[3].AssertNameIs("task")
	spans[3].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "processed-test2",
	})
}

func TestTaskFunc_TAdapter(t *testing.T) {
	t.Parallel()

	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	// Simple task using old signature - will be wrapped with T()
	simpleTask := func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "simple-" + input.Value}, nil
	}

	// Wrap it with T() adapter
	task := T(simpleTask)

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the adapted task works correctly
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + score + eval

	// Verify output from simple task
	spans[0].AssertNameIs("task")
	spans[0].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "simple-test",
	})
}

func TestEval_ParallelWithTaskErrors(t *testing.T) {
	t.Parallel()

	// Test that parallel execution properly handles multiple task errors
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "pass1"}},
		{Input: testInput{Value: "fail1"}},
		{Input: testInput{Value: "pass2"}},
		{Input: testInput{Value: "fail2"}},
	})

	// Task that fails for inputs containing "fail"
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		if input.Value[:4] == "fail" {
			return testOutput{}, errors.New("task failed for " + input.Value)
		}
		return testOutput{Result: "ok-" + input.Value}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 2) // parallel=2

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return errors from failed tasks
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task failed")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	// 2 successful cases: 3 spans each (task+score+eval)
	// 2 failed cases: 2 spans each (task+eval, no score)
	// Total: 2*3 + 2*2 = 10 spans
	assert.Len(t, spans, 10)

	// Count error spans
	errorCount := 0
	for _, span := range spans {
		if span.Status().Code == codes.Error {
			errorCount++
		}
	}
	assert.Equal(t, 4, errorCount) // 2 failed tasks + 2 failed evals
}

func TestEval_ParallelWithScorerErrors(t *testing.T) {
	t.Parallel()

	// Test that parallel execution properly handles multiple scorer errors
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
		{Input: testInput{Value: "test3"}},
		{Input: testInput{Value: "test4"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// Scorer that fails for test2 and test4
	scorers := []Scorer[testInput, testOutput]{
		NewScorer("conditional", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
			if result.Input.Value == "test2" || result.Input.Value == "test4" {
				return nil, errors.New("scorer failed for " + result.Input.Value)
			}
			return S(1.0), nil
		}),
	}

	ute := newUnitTestEval(t, cases, task, scorers, 2) // parallel=2

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return errors from failed scorers
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scorer failed")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	// All tasks succeed, all have scores (some with errors)
	// 4 cases * 3 spans each = 12 spans
	assert.Len(t, spans, 12)

	// Count score spans with errors
	scoreErrorCount := 0
	for _, span := range spans {
		if span.Name() == "score" && span.Status().Code == codes.Error {
			scoreErrorCount++
		}
	}
	assert.Equal(t, 2, scoreErrorCount) // 2 failed scorers
}

func TestEval_ParallelAllTasksFail(t *testing.T) {
	t.Parallel()

	// Test that parallel execution handles the case where all tasks fail
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
		{Input: testInput{Value: "test3"}},
	})

	// Task that always fails
	taskErr := errors.New("task always fails")
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{}, taskErr
	})

	ute := newUnitTestEval(t, cases, task, nil, 2) // parallel=2

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return all errors
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task always fails")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	// 3 cases * 2 spans each (task+eval, no scores) = 6 spans
	assert.Len(t, spans, 6)

	// All spans should be errors
	for _, span := range spans {
		assert.Equal(t, codes.Error, span.Status().Code)
	}
}

func TestEval_ParallelWithIteratorErrors(t *testing.T) {
	t.Parallel()

	// Test that parallel execution properly handles iterator errors mixed with successful cases
	// Create a generator that returns: case1, case2, error, case3
	index := 0
	generator := &customCases[testInput, testOutput]{
		nextFunc: func() (Case[testInput, testOutput], error) {
			index++
			switch index {
			case 1:
				return Case[testInput, testOutput]{Input: testInput{Value: "first"}}, nil
			case 2:
				return Case[testInput, testOutput]{Input: testInput{Value: "second"}}, nil
			case 3:
				return Case[testInput, testOutput]{}, errors.New("iterator error during parallel execution")
			case 4:
				return Case[testInput, testOutput]{Input: testInput{Value: "third"}}, nil
			default:
				return Case[testInput, testOutput]{}, io.EOF
			}
		},
	}

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: input.Value}, nil
	})

	ute := newUnitTestEval(t, generator, task, nil, 2) // parallel=2

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return the iterator error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iterator error during parallel execution")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	// 3 successful cases * 3 spans + 1 iterator error span = 10 spans
	assert.Len(t, spans, 10)
}

// customCases allows custom Next() implementation for testing
type customCases[I, R any] struct {
	nextFunc func() (Case[I, R], error)
}

func (c *customCases[I, R]) Next() (Case[I, R], error) {
	return c.nextFunc()
}

func TestEval_ScoreMetadata_SingleScorer(t *testing.T) {
	t.Parallel()

	// Test single scorer with metadata - matches Python/TypeScript behavior
	// Single score: metadata and output should be flat at top level
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output"}, nil
	})

	// Single scorer that returns metadata
	scorer := NewScorer("with_metadata", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		return Scores{
			{
				Name:  "accuracy",
				Score: 0.95,
				Metadata: map[string]interface{}{
					"reasoning":  "Result is good",
					"confidence": 0.9,
				},
			},
		}, nil
	})

	ute := newUnitTestEval(t, cases, task, []Scorer[testInput, testOutput]{scorer}, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + score + eval

	// Find the score span
	scoreSpan := spans[1]
	scoreSpan.AssertNameIs("score")

	// For single score, metadata should be flat at top level
	scoreSpan.AssertJSONAttrEquals("braintrust.scores", map[string]any{"accuracy": 0.95})
	scoreSpan.AssertJSONAttrEquals("braintrust.metadata", map[string]any{
		"reasoning":  "Result is good",
		"confidence": 0.9,
	})
	scoreSpan.AssertJSONAttrEquals("braintrust.output", map[string]any{
		"score": 0.95,
	})
}

func TestEval_ScoreMetadata_MultipleScorers(t *testing.T) {
	t.Parallel()

	// Test multiple scorers with mixed metadata - matches Python/TypeScript behavior
	// Multiple scores: metadata and output should be nested by score name
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output"}, nil
	})

	scorers := []Scorer[testInput, testOutput]{
		NewScorer("with_metadata", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
			return Scores{
				{
					Name:  "accuracy",
					Score: 0.95,
					Metadata: map[string]interface{}{
						"reasoning": "Good result",
					},
				},
			}, nil
		}),
		NewScorer("without_metadata", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
			return S(0.8), nil
		}),
	}

	ute := newUnitTestEval(t, cases, task, scorers, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)

	scoreSpan := spans[1]
	scoreSpan.AssertNameIs("score")

	// For multiple scores, metadata is nested by score name
	scoreSpan.AssertJSONAttrEquals("braintrust.scores", map[string]any{
		"accuracy":         0.95,
		"without_metadata": 0.8,
	})
	// Only scores with metadata appear here
	scoreSpan.AssertJSONAttrEquals("braintrust.metadata", map[string]any{
		"accuracy": map[string]any{
			"reasoning": "Good result",
		},
	})
	// Output is nested by score name
	scoreSpan.AssertJSONAttrEquals("braintrust.output", map[string]any{
		"accuracy": map[string]any{
			"score": 0.95,
		},
		"without_metadata": map[string]any{
			"score": 0.8,
		},
	})
}

func TestEval_ScoreMetadata_NoMetadata(t *testing.T) {
	t.Parallel()

	// Test that when a single score has no metadata, metadata attribute is not set
	cases := NewCases([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output"}, nil
	})

	scorer := NewScorer("no_metadata", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		return S(0.5), nil
	})

	ute := newUnitTestEval(t, cases, task, []Scorer[testInput, testOutput]{scorer}, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)

	scoreSpan := spans[1]
	scoreSpan.AssertNameIs("score")

	scoreSpan.AssertJSONAttrEquals("braintrust.scores", map[string]any{
		"no_metadata": 0.5,
	})
	scoreSpan.AssertJSONAttrEquals("braintrust.output", map[string]any{
		"score": 0.5,
	})

	// Verify metadata is NOT present
	assert.False(t, scoreSpan.HasAttr("braintrust.metadata"), "braintrust.metadata should not be present")
}
