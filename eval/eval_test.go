package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/logger"
	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
	"github.com/braintrustdata/braintrust-sdk-go/trace"
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
func newUnitTestEval[I, R any](t *testing.T, dataset Dataset[I, R], task TaskFunc[I, R], scorers []Scorer[I, R], parallelism int) *unitTestEval[I, R] {
	t.Helper()
	return newUnitTestEvalWithClassifiers(t, dataset, task, scorers, nil, parallelism)
}

// newUnitTestEvalWithClassifiers is the underlying constructor that accepts both
// scorers and classifiers. Most existing tests only exercise scorers and go
// through newUnitTestEval.
func newUnitTestEvalWithClassifiers[I, R any](t *testing.T, dataset Dataset[I, R], task TaskFunc[I, R], scorers []Scorer[I, R], classifiers []Classifier[I, R], parallelism int) *unitTestEval[I, R] {
	t.Helper()

	// Create test tracer and exporter using oteltest
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	// Create fake session with test data
	session := tests.NewSession(t)

	// Create eval with fake IDs
	e := testNewEval(
		session,
		tracer,
		"exp-12345678",    // fake experiment ID
		"test-experiment", // fake experiment name
		"proj-87654321",   // fake project ID
		"test-project",    // fake project name
		dataset,
		task,
		scorers,
		classifiers,
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
	cases := NewDataset([]Case[testInput, testOutput]{
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

	// Each case produces: task + scorer + eval = 3 spans. 2 cases = 6 spans.
	// Completion order per case: task, scorer, eval.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6)

	// First case spans (completion order: task, scorer, eval)
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

	// Scorer span is named after the scorer, with purpose/name in span_attributes,
	// and the task result logged as input.
	spans[1].AssertEqual(oteltest.TestSpan{
		Name: "accuracy",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.span_attributes": map[string]any{
				"type":    "score",
				"name":    "accuracy",
				"purpose": "scorer",
			},
			"braintrust.input_json": map[string]any{
				"input":    map[string]any{"value": "test1"},
				"expected": map[string]any{"result": "expected1"},
				"output":   map[string]any{"result": "output-test1"},
			},
			"braintrust.scores":   map[string]any{"accuracy": 0.95},
			"braintrust.metadata": map[string]any{"note": "good"},
			"braintrust.output":   map[string]any{"score": 0.95},
		},
	})

	// Task and scorer spans are children of the eval span.
	spans[0].AssertChildOf(&spans[2])
	spans[1].AssertChildOf(&spans[2])

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
		Name: "accuracy",
		Attrs: map[string]any{
			"braintrust.parent": "experiment_id:exp-12345678",
		},
		JSONAttrs: map[string]any{
			"braintrust.span_attributes": map[string]any{
				"type":    "score",
				"name":    "accuracy",
				"purpose": "scorer",
			},
			"braintrust.input_json": map[string]any{
				"input":    map[string]any{"value": "test2"},
				"expected": map[string]any{"result": "expected2"},
				"output":   map[string]any{"result": "output-test2"},
			},
			"braintrust.scores":   map[string]any{"accuracy": 0.95},
			"braintrust.metadata": map[string]any{"note": "good"},
			"braintrust.output":   map[string]any{"score": 0.95},
		},
	})

	// Task and scorer spans are children of the eval span.
	spans[3].AssertChildOf(&spans[5])
	spans[4].AssertChildOf(&spans[5])

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

func TestPermalink_EscapesOrgName(t *testing.T) {
	t.Parallel()

	session := auth.NewTestSession(
		auth.TestAPIKey,
		"org-test-12345",
		"Test Org With Space",
		"https://api-test.braintrust.dev",
		"https://test.braintrust.dev",
		"https://test.braintrust.dev",
		logger.NewFailTestLogger(t),
	)

	tp, _ := oteltest.Setup(t)
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{}, nil
	})
	e := testNewEval[testInput, testOutput](
		session,
		tp.Tracer(t.Name()),
		"exp-12345678",
		"test-experiment",
		"proj-87654321",
		"test-project",
		NewDataset([]Case[testInput, testOutput]{}),
		task,
		nil,
		nil,
		1,
	)

	const want = "https://test.braintrust.dev/app/Test%20Org%20With%20Space/object?object_type=experiment&object_id=exp-12345678"
	assert.Equal(t, want, e.permalink())
}

func TestNewEval_Parallelism(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 4)
	assert.Equal(t, 4, ute.eval.goroutines)
}

func TestEval_TrialCount(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	var calls atomic.Int64
	var mu sync.Mutex
	trialIndices := map[string][]int{}
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		calls.Add(1)
		mu.Lock()
		trialIndices[input.Value] = append(trialIndices[input.Value], hooks.TrialIndex)
		mu.Unlock()
		return TaskOutput[testOutput]{Value: testOutput{Result: input.Value}}, nil
	}

	ute := newUnitTestEval(t, cases, task, nil, 1)
	ute.eval.trialCount = 3

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(6), calls.Load())
	assert.ElementsMatch(t, []int{0, 1, 2}, trialIndices["test1"])
	assert.ElementsMatch(t, []int{0, 1, 2}, trialIndices["test2"])

	spans := ute.exporter.Flush()
	assert.Len(t, spans, 12) // 2 cases * 3 trials * (task + eval)
}

func TestEval_CaseTrialCountOverridesDefault(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}, TrialCount: 4},
	})

	var calls atomic.Int64
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		calls.Add(1)
		return testOutput{Result: input.Value}, nil
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)
	ute.eval.trialCount = 2

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(6), calls.Load())
}

func TestNewEval_DefaultParallelism(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: each case produces task + eval = 2 spans.
	// 3 cases * 2 spans = 6 spans total.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6)

	// First case succeeds (task, eval)
	spans[0].AssertNameIs("task")
	spans[1].AssertNameIs("eval")

	// Second case fails: task has error, eval has error with attrs set upfront and null output_json.
	spans[2].AssertNameIs("task")
	assert.Equal(t, codes.Error, spans[2].Status().Code)
	taskEvents := spans[2].Events()
	require.Len(t, taskEvents, 1)
	assert.Equal(t, "exception", taskEvents[0].Name)

	spans[3].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[3].Status().Code)
	// Eval span has input/expected set upfront even on task failure.
	spans[3].AssertJSONAttrEquals("braintrust.input_json", map[string]any{"value": "error"})
	spans[3].AssertJSONAttrEquals("braintrust.expected", map[string]any{"result": ""})
	spans[3].AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "eval"})
	// Third case succeeds (task, eval)
	spans[4].AssertNameIs("task")
	spans[5].AssertNameIs("eval")
}

func TestEval_Run_ScorerError(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// Scorers: one succeeds, one fails, one succeeds
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

	// Each case: task + good-scorer + bad-scorer + another-good-scorer + eval = 5 spans.
	// 2 cases * 5 spans = 10 spans total.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 10)

	// First case
	spans[0].AssertNameIs("task")

	spans[1].AssertNameIs("good-scorer")
	spans[1].AssertJSONAttrEquals("braintrust.scores", map[string]any{"good-scorer": 0.8})
	spans[1].AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "good-scorer", "purpose": "scorer",
	})

	spans[2].AssertNameIs("bad-scorer")
	assert.Equal(t, codes.Error, spans[2].Status().Code)
	spans[2].AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "bad-scorer", "purpose": "scorer",
	})
	badScorerEvents := spans[2].Events()
	require.Len(t, badScorerEvents, 1)
	assert.Equal(t, "exception", badScorerEvents[0].Name)

	spans[3].AssertNameIs("another-good-scorer")
	spans[3].AssertJSONAttrEquals("braintrust.scores", map[string]any{"another-good-scorer": 0.9})

	spans[4].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[4].Status().Code)

	// Second case (same pattern)
	spans[5].AssertNameIs("task")
	spans[6].AssertNameIs("good-scorer")
	spans[7].AssertNameIs("bad-scorer")
	assert.Equal(t, codes.Error, spans[7].Status().Code)
	spans[8].AssertNameIs("another-good-scorer")
	spans[9].AssertNameIs("eval")
	assert.Equal(t, codes.Error, spans[9].Status().Code)
}

func TestEval_Run_PrintsSummary(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	cases := NewDataset([]Case[testInput, testOutput]{
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
	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: task + eval = 2 spans.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 2)
	spans[0].AssertNameIs("task")
	spans[1].AssertNameIs("eval")
}

func TestTaskFunc_ModifyTaskSpan(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: task + eval = 2 spans.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 2)

	taskSpan := spans[0]
	taskSpan.AssertNameIs("task")
	taskSpan.AssertAttrEquals("custom.task.attribute", "task-value")
	taskSpan.AssertAttrEquals("custom.task.count", int64(42))
}

func TestTaskFunc_ModifyEvalSpan(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: task + eval = 2 spans.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 2)

	evalSpan := spans[1]
	evalSpan.AssertNameIs("eval")
	evalSpan.AssertAttrEquals("custom.eval.attribute", "eval-value")
	evalSpan.AssertAttrEquals("custom.model", "gpt-4")
}

func TestTaskFunc_ReturnsTaskResult(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: 2 cases * (task + eval) = 4 spans.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 4)

	// First case
	spans[0].AssertNameIs("task")
	spans[0].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "processed-test1",
	})

	// Second case
	spans[2].AssertNameIs("task")
	spans[2].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "processed-test2",
	})
}

func TestTaskFunc_TAdapter(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
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

	// No scorers: task + eval = 2 spans.
	spans := ute.exporter.Flush()
	require.Len(t, spans, 2)

	// Verify output from simple task
	spans[0].AssertNameIs("task")
	spans[0].AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"result": "simple-test",
	})
}

func TestEval_ParallelWithTaskErrors(t *testing.T) {
	t.Parallel()

	// Test that parallel execution properly handles multiple task errors
	cases := NewDataset([]Case[testInput, testOutput]{
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
	// No scorers: 4 cases * (task + eval) = 8 spans
	assert.Len(t, spans, 8)

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
	cases := NewDataset([]Case[testInput, testOutput]{
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
	// 1 scorer per case: 4 cases * (task + conditional + eval) = 12 spans
	assert.Len(t, spans, 12)

	// Count scorer spans with errors (scorer is named "conditional")
	scorerErrorCount := 0
	for _, span := range spans {
		if span.Name() == "conditional" && span.Status().Code == codes.Error {
			scorerErrorCount++
		}
	}
	assert.Equal(t, 2, scorerErrorCount) // 2 failed scorers
}

func TestEval_ParallelAllTasksFail(t *testing.T) {
	t.Parallel()

	// Test that parallel execution handles the case where all tasks fail
	cases := NewDataset([]Case[testInput, testOutput]{
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
	ute.eval.trialCount = 3

	ctx := context.Background()
	result, err := ute.eval.run(ctx)

	// Should return the iterator error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iterator error during parallel execution")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()
	// No scorers: 3 successful cases * 3 trials * 2 spans (task+eval) + 1 iterator error span = 19 spans
	assert.Len(t, spans, 19)
}

// customCases allows custom Next() implementation for testing
type customCases[I, R any] struct {
	nextFunc func() (Case[I, R], error)
}

func (c *customCases[I, R]) Next() (Case[I, R], error) {
	return c.nextFunc()
}

func (c *customCases[I, R]) ID() string {
	return ""
}

func (c *customCases[I, R]) Version() string {
	return ""
}

func TestEval_ScoreMetadata_SingleScorer(t *testing.T) {
	t.Parallel()

	// Single scorer span: metadata and output are flat (not nested by scorer name).
	cases := NewDataset([]Case[testInput, testOutput]{
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

	// task + with_metadata + eval = 3 spans
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)

	scoreSpan := spans[1]
	scoreSpan.AssertNameIs("with_metadata")
	scoreSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "with_metadata", "purpose": "scorer",
	})

	// Single score returned: metadata and output are flat.
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

	// Two scorers each get their own span; within each span the single score is flat.
	cases := NewDataset([]Case[testInput, testOutput]{
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

	// task + with_metadata + without_metadata + eval = 4 spans
	spans := ute.exporter.Flush()
	require.Len(t, spans, 4)

	// "with_metadata" scorer span
	withMeta := spans[1]
	withMeta.AssertNameIs("with_metadata")
	withMeta.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "with_metadata", "purpose": "scorer",
	})
	withMeta.AssertJSONAttrEquals("braintrust.scores", map[string]any{"accuracy": 0.95})
	withMeta.AssertJSONAttrEquals("braintrust.metadata", map[string]any{"reasoning": "Good result"})
	withMeta.AssertJSONAttrEquals("braintrust.output", map[string]any{"score": 0.95})

	// "without_metadata" scorer span
	withoutMeta := spans[2]
	withoutMeta.AssertNameIs("without_metadata")
	withoutMeta.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "without_metadata", "purpose": "scorer",
	})
	withoutMeta.AssertJSONAttrEquals("braintrust.scores", map[string]any{"without_metadata": 0.8})
	withoutMeta.AssertJSONAttrEquals("braintrust.output", map[string]any{"score": 0.8})
	assert.False(t, withoutMeta.HasAttr("braintrust.metadata"), "no metadata expected")
}

func TestEval_ScoreMetadata_NoMetadata(t *testing.T) {
	t.Parallel()

	// Test that when a single score has no metadata, metadata attribute is not set
	cases := NewDataset([]Case[testInput, testOutput]{
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

	// task + no_metadata + eval = 3 spans
	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)

	scoreSpan := spans[1]
	scoreSpan.AssertNameIs("no_metadata")
	scoreSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type": "score", "name": "no_metadata", "purpose": "scorer",
	})
	scoreSpan.AssertJSONAttrEquals("braintrust.scores", map[string]any{
		"no_metadata": 0.5,
	})
	scoreSpan.AssertJSONAttrEquals("braintrust.output", map[string]any{
		"score": 0.5,
	})

	// Verify metadata is NOT present
	assert.False(t, scoreSpan.HasAttr("braintrust.metadata"), "braintrust.metadata should not be present")
}

// TestCase_DatasetFields tests that the Case struct can hold dataset-specific fields
func TestCase_DatasetFields(t *testing.T) {
	// Create a case with dataset fields populated
	c := Case[testInput, testOutput]{
		Input:    testInput{Value: "test"},
		Expected: testOutput{Result: "expected"},
		Tags:     []string{"test"},
		Metadata: map[string]interface{}{"key": "value"},
		ID:       "event-123",
		XactID:   "xact-456",
		Created:  "2024-01-15T10:30:00Z",
	}

	// Verify all fields are accessible
	assert.Equal(t, "test", c.Input.Value)
	assert.Equal(t, "expected", c.Expected.Result)
	assert.Equal(t, []string{"test"}, c.Tags)
	assert.Equal(t, "value", c.Metadata["key"])
	assert.Equal(t, "event-123", c.ID)
	assert.Equal(t, "xact-456", c.XactID)
	assert.Equal(t, "2024-01-15T10:30:00Z", c.Created)

	// Create a case without dataset fields (in-memory case)
	c2 := Case[testInput, testOutput]{
		Input:    testInput{Value: "test2"},
		Expected: testOutput{Result: "expected2"},
	}

	// Verify dataset fields are empty strings by default
	assert.Empty(t, c2.ID)
	assert.Empty(t, c2.XactID)
	assert.Empty(t, c2.Created)
}

// TestEval_OriginAttributeFromDataset tests that origin attribute is set for dataset cases
func TestEval_OriginAttributeFromDataset(t *testing.T) {
	t.Parallel()

	// Create a case that came from a dataset (has ID, XactID, Created populated)
	datasetCase := Case[testInput, testOutput]{
		Input:    testInput{Value: "from-dataset"},
		Expected: testOutput{Result: "expected"},
		ID:       "event-abc123",
		XactID:   "xact-def456",
		Created:  "2024-01-15T10:30:00Z",
	}

	// Task function
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: input.Value + "-processed"}, nil
	})

	// Create eval with the dataset case
	testEval := newUnitTestEval(t, NewDataset([]Case[testInput, testOutput]{datasetCase}), task, nil, 1)

	// Run the eval
	ctx := context.Background()
	result, err := testEval.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Check spans
	spans := testEval.exporter.Flush()
	require.GreaterOrEqual(t, len(spans), 2, "Expected at least task and eval spans")

	// The eval span is typically the last one
	evalSpan := spans[len(spans)-1]
	evalSpan.AssertNameIs("eval")

	// Verify origin attribute is set with the case's dataset fields
	assert.True(t, evalSpan.HasAttr("braintrust.origin"), "Origin should be set for dataset cases")
}

// TestEval_NoOriginAttributeForInMemoryCase tests that origin is NOT set for in-memory cases
func TestEval_NoOriginAttributeForInMemoryCase(t *testing.T) {
	t.Parallel()

	// Create an in-memory case (no dataset fields)
	inMemoryCase := Case[testInput, testOutput]{
		Input:    testInput{Value: "in-memory"},
		Expected: testOutput{Result: "expected"},
	}

	// Task function
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: input.Value + "-processed"}, nil
	})

	// Create eval with the in-memory case
	testEval := newUnitTestEval(t, NewDataset([]Case[testInput, testOutput]{inMemoryCase}), task, nil, 1)

	// Run the eval
	ctx := context.Background()
	result, err := testEval.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Check spans
	spans := testEval.exporter.Flush()
	require.GreaterOrEqual(t, len(spans), 2, "Expected at least task and eval spans")

	// The eval span is typically the last one
	evalSpan := spans[len(spans)-1]
	evalSpan.AssertNameIs("eval")

	// Verify origin attribute is NOT present
	assert.False(t, evalSpan.HasAttr("braintrust.origin"), "Origin should not be set for in-memory cases")
}

func TestEval_ParentPropagation(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	var taskParent trace.Parent
	var scorerParent trace.Parent
	var ok bool

	cases := NewDataset([]Case[int, int]{
		{Input: 1, Expected: 2},
	})

	task := T(func(ctx context.Context, input int) (int, error) {
		ok, taskParent = trace.GetParent(ctx)
		assert.True(ok)
		return input + 1, nil
	})

	scorer := NewScorer("s", func(ctx context.Context, result TaskResult[int, int]) (Scores, error) {
		ok, scorerParent = trace.GetParent(ctx)
		assert.True(ok)
		return S(1.0), nil
	})

	ute := newUnitTestEval(t, cases, task, []Scorer[int, int]{scorer}, 1)
	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(taskParent, trace.Parent{Type: trace.ParentTypeExperimentID, ID: result.ID()})
	assert.Equal(scorerParent, trace.Parent{Type: trace.ParentTypeExperimentID, ID: result.ID()})
}

func TestOnCaseComplete_Callback(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}, Expected: testOutput{Result: "expected1"}},
		{Input: testInput{Value: "test2"}, Expected: testOutput{Result: "expected2"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output-" + input.Value}, nil
	})

	scorer := NewScorer("accuracy", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		return S(0.75), nil
	})

	// Track callback invocations
	var mu sync.Mutex
	var progresses []CaseProgress
	callback := func(cp CaseProgress) {
		mu.Lock()
		progresses = append(progresses, cp)
		mu.Unlock()
	}

	// Create eval manually with the callback
	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-callback", "callback-experiment",
		"proj-callback", "callback-project",
		cases, task,
		[]Scorer[testInput, testOutput]{scorer},
		nil, 1, 1, true, callback, trace.Parent{}, nil,
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, progresses, 2)

	// Both should have scores and no errors
	for _, p := range progresses {
		assert.NoError(t, p.Error)
		assert.NotNil(t, p.Scores)
		assert.Equal(t, 0.75, p.Scores["accuracy"])
		assert.NotNil(t, p.Output)
	}
}

func TestOnCaseComplete_CallbackOnError(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "will-fail"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{}, errors.New("task failed")
	})

	var called bool
	var capturedProgress CaseProgress
	callback := func(cp CaseProgress) {
		called = true
		capturedProgress = cp
	}

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-err", "err-experiment",
		"proj-err", "err-project",
		cases, task,
		nil, nil, 1, 1, true, callback, trace.Parent{}, nil,
	)

	_, _ = e.run(context.Background())

	assert.True(t, called)
	assert.Error(t, capturedProgress.Error)
	assert.Contains(t, capturedProgress.Error.Error(), "task failed")
}

func TestOnCaseComplete_NilCallback(t *testing.T) {
	t.Parallel()

	// Ensure nil callback doesn't panic
	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-nil", "nil-experiment",
		"proj-nil", "nil-project",
		cases, task,
		nil, nil, 1, 1, true, nil, trace.Parent{}, nil,
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestOnCaseComplete_Parallel(t *testing.T) {
	t.Parallel()

	// 20 cases with parallelism=4 to exercise concurrent callback invocation
	var inputCases []Case[testInput, testOutput]
	for i := 0; i < 20; i++ {
		inputCases = append(inputCases, Case[testInput, testOutput]{
			Input:    testInput{Value: fmt.Sprintf("test%d", i)},
			Expected: testOutput{Result: fmt.Sprintf("output-test%d", i)},
		})
	}
	cases := NewDataset(inputCases)

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "output-" + input.Value}, nil
	})

	scorer := NewScorer("score", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		return S(1.0), nil
	})

	var mu sync.Mutex
	var progresses []CaseProgress
	callback := func(cp CaseProgress) {
		mu.Lock()
		progresses = append(progresses, cp)
		mu.Unlock()
	}

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-parallel", "parallel-experiment",
		"proj-parallel", "parallel-project",
		cases, task,
		[]Scorer[testInput, testOutput]{scorer},
		nil, 4, 1, true, callback, trace.Parent{}, nil,
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, progresses, 20, "callback should fire for all 20 cases")

	for _, p := range progresses {
		assert.NoError(t, p.Error)
		assert.Equal(t, 1.0, p.Scores["score"])
		assert.NotNil(t, p.Output)
	}
}

func TestCaseProgress_IDIsSpanID(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	var capturedProgress CaseProgress
	callback := func(cp CaseProgress) {
		capturedProgress = cp
	}

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-id", "id-experiment",
		"proj-id", "id-project",
		cases, task,
		nil, nil, 1, 1, true, callback, trace.Parent{}, nil,
	)

	_, err := e.run(context.Background())
	require.NoError(t, err)

	// ID should be a 16-character hex span ID
	assert.NotEmpty(t, capturedProgress.ID)
	assert.Regexp(t, `^[0-9a-f]{16}$`, capturedProgress.ID)
}

func TestCaseProgress_OriginFromDataset(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{
			Input:   testInput{Value: "test"},
			ID:      "case-123",
			XactID:  "xact-456",
			Created: "2024-01-15T10:30:00Z",
		},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	var capturedProgress CaseProgress
	callback := func(cp CaseProgress) {
		capturedProgress = cp
	}

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-origin", "origin-experiment",
		"proj-origin", "origin-project",
		cases, task,
		nil, nil, 1, 1, true, callback, trace.Parent{}, nil,
	)

	_, err := e.run(context.Background())
	require.NoError(t, err)

	require.NotNil(t, capturedProgress.Origin)
	assert.Equal(t, "dataset", capturedProgress.Origin["object_type"])
	assert.Equal(t, "case-123", capturedProgress.Origin["id"])
	assert.Equal(t, "xact-456", capturedProgress.Origin["_xact_id"])
}

func TestCaseProgress_OriginNilWithoutDatasetID(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	var capturedProgress CaseProgress
	callback := func(cp CaseProgress) {
		capturedProgress = cp
	}

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-no-origin", "no-origin-experiment",
		"proj-no-origin", "no-origin-project",
		cases, task,
		nil, nil, 1, 1, true, callback, trace.Parent{}, nil,
	)

	_, err := e.run(context.Background())
	require.NoError(t, err)

	assert.Nil(t, capturedProgress.Origin)
}

func TestResult_ProjectIDAndProjectName(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test"}},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-proj", "project-experiment",
		"proj-abc123", "test-project-name",
		cases, task,
		nil, nil, 1, 1, true, nil, trace.Parent{}, nil,
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "proj-abc123", result.ProjectID())
	assert.Equal(t, "test-project-name", result.ProjectName())
}

func TestTaskOutput_UserData(t *testing.T) {
	t.Parallel()

	// Test that UserData flows from task to scorer and is NOT logged
	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "test1"}},
		{Input: testInput{Value: "test2"}},
	})

	// Task that passes data via UserData
	task := func(ctx context.Context, input testInput, hooks *TaskHooks) (TaskOutput[testOutput], error) {
		result := testOutput{Result: "processed-" + input.Value}

		// Pass user data to scorer via UserData
		userData := map[string]any{
			"connection": "fake-db-connection",
		}

		return TaskOutput[testOutput]{
			Value:    result,
			UserData: userData,
		}, nil
	}

	// Scorer that verifies it can access UserData
	scorer := NewScorer("verify_userdata", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		// Verify UserData is passed correctly
		assert.Equal(t, map[string]any{"connection": "fake-db-connection"}, result.UserData)

		return Scores{{
			Name:  "verify_userdata",
			Score: 1.0,
		}}, nil
	})

	ute := newUnitTestEval(t, cases, task, []Scorer[testInput, testOutput]{scorer}, 1)

	ctx := context.Background()
	result, err := ute.eval.run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// CRITICAL: Verify UserData is NOT logged to spans
	// 2 cases * (task + verify_userdata + eval) = 6 spans
	spans := ute.exporter.Flush()
	require.Len(t, spans, 6)

	// Check all spans - UserData should NOT appear in any attributes
	for i, span := range spans {
		// Iterate over all attributes in the span
		for _, attr := range span.Stub.Attributes {
			key := string(attr.Key)

			// UserData should not appear in any attribute key
			assert.NotContains(t, key, "UserData", "span %d should not contain UserData in attribute keys", i)
			assert.NotContains(t, key, "user_data", "span %d should not contain user_data in attribute keys", i)

			// Check that UserData value is not in attribute values
			if attr.Value.Type() == attribute.STRING {
				valueStr := attr.Value.AsString()
				assert.NotContains(t, valueStr, "fake-db-connection", "span %d should not contain UserData values", i)
			}
		}
	}
}

// TestEval_ScorerSpanInput verifies that each scorer span receives the full task result
// (input, expected, output) as its braintrust.input_json.
func TestEval_ScorerSpanInput(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{
			Input:    testInput{Value: "hello"},
			Expected: testOutput{Result: "world"},
		},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "actual-output"}, nil
	})

	scorer := NewScorer("check_input", func(ctx context.Context, result TaskResult[testInput, testOutput]) (Scores, error) {
		return S(1.0), nil
	})

	ute := newUnitTestEval(t, cases, task, []Scorer[testInput, testOutput]{scorer}, 1)

	ctx := context.Background()
	_, err := ute.eval.run(ctx)
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	require.Len(t, spans, 3) // task + check_input + eval

	scorerSpan := spans[1]
	scorerSpan.AssertNameIs("check_input")
	scorerSpan.AssertJSONAttrEquals("braintrust.input_json", map[string]any{
		"input":    map[string]any{"value": "hello"},
		"expected": map[string]any{"result": "world"},
		"output":   map[string]any{"result": "actual-output"},
	})
}

// TestEval_EvalSpanAttrsOnTaskFailure verifies that the eval span has input/expected set
// and output_json set to null even when the task fails.
func TestEval_EvalSpanAttrsOnTaskFailure(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{
			Input:    testInput{Value: "failing-input"},
			Expected: testOutput{Result: "never-reached"},
			Metadata: map[string]interface{}{"key": "meta"},
			Tags:     []string{"tag1"},
		},
	})

	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{}, errors.New("task exploded")
	})

	ute := newUnitTestEval(t, cases, task, nil, 1)

	ctx := context.Background()
	_, err := ute.eval.run(ctx)
	require.Error(t, err)

	// task + eval = 2 spans (no scorer spans since task failed)
	spans := ute.exporter.Flush()
	require.Len(t, spans, 2)

	evalSpan := spans[1]
	evalSpan.AssertNameIs("eval")
	assert.Equal(t, codes.Error, evalSpan.Status().Code)

	// These attrs are set upfront, before the task runs.
	evalSpan.AssertJSONAttrEquals("braintrust.input_json", map[string]any{"value": "failing-input"})
	evalSpan.AssertJSONAttrEquals("braintrust.expected", map[string]any{"result": "never-reached"})
	evalSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "eval"})
	evalSpan.AssertJSONAttrEquals("braintrust.metadata", map[string]any{"key": "meta"})

	// Tags are stored as a string-slice attribute (not JSON).
	var foundTags []string
	for _, attr := range evalSpan.Stub.Attributes {
		if string(attr.Key) == "braintrust.tags" {
			foundTags = attr.Value.AsStringSlice()
			break
		}
	}
	assert.Equal(t, []string{"tag1"}, foundTags)
}

func TestSpanParentOverride(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "x"}, Expected: testOutput{Result: "y"}},
	})
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: input.Value}, nil
	})
	scorer := NewScorer[testInput, testOutput]("s", func(_ context.Context, _ TaskResult[testInput, testOutput]) (Scores, error) {
		return S(1.0), nil
	})

	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-id", "exp-name",
		"proj-id", "proj-name",
		cases, task,
		[]Scorer[testInput, testOutput]{scorer},
		nil, 1, 1, true, nil,
		trace.NewParent(trace.ParentTypePlaygroundID, "pg-999"), // SpanParent override
		42, // Generation
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.NotEmpty(t, spans)

	// Every span should have the overridden parent attribute
	for _, span := range spans {
		parentVal := span.Attr("braintrust.parent").Value.AsString()
		assert.Equal(t, "playground_id:pg-999", parentVal,
			"span %q should have overridden parent", span.Name())
	}

	// Eval span should have generation in span_attributes
	evalSpan := spans[len(spans)-1]
	evalSpan.AssertNameIs("eval")
	spanAttrsJSON := evalSpan.Attr("braintrust.span_attributes").Value.AsString()
	assert.Contains(t, spanAttrsJSON, `"generation"`)
	assert.Contains(t, spanAttrsJSON, `42`)
}

func TestSpanParentDefault(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "x"}, Expected: testOutput{Result: "y"}},
	})
	task := T(func(ctx context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: input.Value}, nil
	})

	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())
	session := tests.NewSession(t)

	e := newEval(
		session, tracer,
		"exp-id", "exp-name",
		"proj-id", "proj-name",
		cases, task, nil, nil,
		1, 1, true, nil,
		trace.Parent{}, nil, // no override, no generation
	)

	result, err := e.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.NotEmpty(t, spans)

	// Every span should have the default experiment_id parent
	for _, span := range spans {
		parentVal := span.Attr("braintrust.parent").Value.AsString()
		assert.Equal(t, "experiment_id:exp-id", parentVal,
			"span %q should have default experiment parent", span.Name())
	}

	// Eval span should NOT have generation in span_attributes
	evalSpan := spans[len(spans)-1]
	spanAttrsJSON := evalSpan.Attr("braintrust.span_attributes").Value.AsString()
	assert.NotContains(t, spanAttrsJSON, `"generation"`)
}
