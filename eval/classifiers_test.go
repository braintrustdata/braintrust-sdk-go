package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// simpleClassifier is a test Classifier that returns a fixed set of
// classifications, optionally with a forced error.
type simpleClassifier struct {
	name            string
	classifications Classifications
	err             error
}

func (c *simpleClassifier) Name() string { return c.name }

func (c *simpleClassifier) Run(_ context.Context, _ TaskResult[testInput, testOutput]) (Classifications, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.classifications, nil
}

// findSpanByName returns the first span matching name. Spans from parallel
// scorer/classifier passes may be interleaved, so name-based lookup is the
// stable way to assert on them.
func findSpanByName(t *testing.T, spans []oteltest.Span, name string) *oteltest.Span {
	t.Helper()
	for i := range spans {
		if spans[i].Name() == name {
			return &spans[i]
		}
	}
	t.Fatalf("no span with name %q (got: %s)", name, spanNames(spans))
	return nil
}

func spanNames(spans []oteltest.Span) string {
	names := make([]string, len(spans))
	for i := range spans {
		names[i] = spans[i].Name()
	}
	return "[" + joinStrings(names, ", ") + "]"
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func TestNewEval_Classifier_SingleLabel(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}, Expected: testOutput{Result: "greeting"}},
	})
	task := T(func(_ context.Context, input testInput) (testOutput, error) {
		return testOutput{Result: "greeting-" + input.Value}, nil
	})

	classifier := &simpleClassifier{
		name: "category",
		classifications: Classifications{
			{ID: "greeting", Label: "Greeting"},
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, []Classifier[testInput, testOutput]{classifier}, 1)

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	// Per case: task + classifier + eval = 3 spans. 1 case = 3 spans.
	require.Len(t, spans, 3)

	classifierSpan := findSpanByName(t, spans, "category")
	classifierSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"type":    "classifier",
		"name":    "category",
		"purpose": "scorer",
	})
	classifierSpan.AssertJSONAttrEquals("braintrust.input_json", map[string]any{
		"input":    map[string]any{"value": "hi"},
		"expected": map[string]any{"result": "greeting"},
		"output":   map[string]any{"result": "greeting-hi"},
	})
	classifierSpan.AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"category": []any{
			map[string]any{"id": "greeting", "label": "Greeting"},
		},
	})

	evalSpan := findSpanByName(t, spans, "eval")
	evalSpan.AssertJSONAttrEquals("braintrust.classifications", map[string]any{
		"category": []any{
			map[string]any{"id": "greeting", "label": "Greeting"},
		},
	})
}

func TestNewEval_Classifier_MultiLabel(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "please send it immediately"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// A classifier returning multiple classifications with the same Name
	// should aggregate as a slice keyed by that name.
	classifier := &simpleClassifier{
		name: "tone",
		classifications: Classifications{
			{Name: "tone", ID: "urgent", Label: "Urgent"},
			{Name: "tone", ID: "polite", Label: "Polite"},
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, []Classifier[testInput, testOutput]{classifier}, 1)

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	require.Len(t, spans, 3)

	expected := map[string]any{
		"tone": []any{
			map[string]any{"id": "urgent", "label": "Urgent"},
			map[string]any{"id": "polite", "label": "Polite"},
		},
	}
	findSpanByName(t, spans, "tone").AssertJSONAttrEquals("braintrust.output_json", expected)
	findSpanByName(t, spans, "eval").AssertJSONAttrEquals("braintrust.classifications", expected)
}

func TestNewEval_Classifier_NameDefaulting(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	// Classification.Name left empty — should default to the classifier name.
	classifier := &simpleClassifier{
		name: "category",
		classifications: Classifications{
			{ID: "greeting"}, // no Name
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, []Classifier[testInput, testOutput]{classifier}, 1)

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	findSpanByName(t, spans, "eval").AssertJSONAttrEquals("braintrust.classifications", map[string]any{
		"category": []any{
			map[string]any{"id": "greeting"},
		},
	})
}

func TestNewEval_Classifier_MultipleClassifiers(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	classifiers := []Classifier[testInput, testOutput]{
		&simpleClassifier{
			name: "category",
			classifications: Classifications{
				{ID: "greeting", Label: "Greeting"},
			},
		},
		&simpleClassifier{
			name: "sentiment",
			classifications: Classifications{
				{ID: "positive", Metadata: map[string]any{"confidence": 0.9}},
			},
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, classifiers, 1)

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	findSpanByName(t, spans, "eval").AssertJSONAttrEquals("braintrust.classifications", map[string]any{
		"category": []any{
			map[string]any{"id": "greeting", "label": "Greeting"},
		},
		"sentiment": []any{
			map[string]any{"id": "positive", "metadata": map[string]any{"confidence": 0.9}},
		},
	})
}

func TestNewEval_Classifier_ErrorIsNonFatal(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}, Metadata: map[string]any{"caseKey": "caseVal"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	boom := errors.New("classifier exploded")
	classifiers := []Classifier[testInput, testOutput]{
		&simpleClassifier{
			name: "ok-classifier",
			classifications: Classifications{
				{ID: "yes", Label: "Yes"},
			},
		},
		&simpleClassifier{name: "broken", err: boom},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, classifiers, 1)

	result, err := ute.eval.run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "classifier exploded")
	assert.NotNil(t, result)

	spans := ute.exporter.Flush()

	// The successful classifier still ran and contributed to the aggregated map.
	findSpanByName(t, spans, "ok-classifier").AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"ok-classifier": []any{
			map[string]any{"id": "yes", "label": "Yes"},
		},
	})

	// The broken classifier's span has Error status with an exception event.
	brokenSpan := findSpanByName(t, spans, "broken")
	assert.Equal(t, codes.Error, brokenSpan.Status().Code)
	events := brokenSpan.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)

	// The eval span carries the aggregated classifications and the
	// classifier_errors merged into the case metadata.
	evalSpan := findSpanByName(t, spans, "eval")
	evalSpan.AssertJSONAttrEquals("braintrust.classifications", map[string]any{
		"ok-classifier": []any{
			map[string]any{"id": "yes", "label": "Yes"},
		},
	})
	evalSpan.AssertJSONAttrEquals("braintrust.metadata", map[string]any{
		"caseKey": "caseVal",
		"classifier_errors": map[string]any{
			"broken": "classifier error: classifier \"broken\" failed: classifier exploded",
		},
	})
}

func TestNewEval_Classifier_EmptyIDIsError(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	classifier := &simpleClassifier{
		name: "category",
		classifications: Classifications{
			{ID: ""}, // empty ID is not allowed per spec
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, nil, []Classifier[testInput, testOutput]{classifier}, 1)

	_, err := ute.eval.run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ID")
}

func TestNewEval_ScorerAndClassifier_RunBoth(t *testing.T) {
	t.Parallel()

	cases := NewDataset([]Case[testInput, testOutput]{
		{Input: testInput{Value: "hi"}, Expected: testOutput{Result: "ok"}},
	})
	task := T(func(_ context.Context, _ testInput) (testOutput, error) {
		return testOutput{Result: "ok"}, nil
	})

	scorers := []Scorer[testInput, testOutput]{
		&simpleScorer{name: "accuracy", score: 1.0},
	}
	classifiers := []Classifier[testInput, testOutput]{
		&simpleClassifier{
			name: "category",
			classifications: Classifications{
				{ID: "greeting", Label: "Greeting"},
			},
		},
	}

	ute := newUnitTestEvalWithClassifiers(t, cases, task, scorers, classifiers, 1)

	_, err := ute.eval.run(context.Background())
	require.NoError(t, err)

	spans := ute.exporter.Flush()
	// task + accuracy + category + eval = 4 spans. Their order between
	// accuracy/category may interleave because scorers and classifiers run
	// in parallel goroutines, so look them up by name.
	require.Len(t, spans, 4)

	findSpanByName(t, spans, "accuracy").AssertJSONAttrEquals("braintrust.scores", map[string]any{
		"accuracy": 1.0,
	})
	findSpanByName(t, spans, "category").AssertJSONAttrEquals("braintrust.output_json", map[string]any{
		"category": []any{
			map[string]any{"id": "greeting", "label": "Greeting"},
		},
	})
}

func TestRun_RequiresScorerOrClassifier(t *testing.T) {
	t.Parallel()

	// Going through the public run() path: at least one of Scorers or
	// Classifiers must be non-empty.
	_, err := run[testInput, testOutput](
		context.Background(),
		Opts[testInput, testOutput]{
			Experiment: "exp",
			Dataset: NewDataset([]Case[testInput, testOutput]{
				{Input: testInput{Value: "hi"}},
			}),
			Task: T(func(_ context.Context, _ testInput) (testOutput, error) {
				return testOutput{Result: "ok"}, nil
			}),
			// no Scorers, no Classifiers
		},
		nil, nil, nil, "some-project",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one of Scorers or Classifiers")
}
