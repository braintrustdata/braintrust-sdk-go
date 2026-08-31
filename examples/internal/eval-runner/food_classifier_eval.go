package main

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// foodClassifier builds the eval this example exposes.
//
// The file name matters: bt discovers Go evals by looking for `*_eval.go`, the
// same suffix convention Go itself uses for `_test.go`. Without a file named
// this way you would have to say `bt eval --language go ./internal/eval-runner`.
func foodClassifier() *eval.Eval[string, string] {
	// TaskWithHooks lets the task read the "model" parameter the user selects in
	// the playground. Two strategies are supported:
	//
	//   - "rule-based" (default): lenient substring matching.
	//   - "strict":               only an exact, single-word match counts.
	//
	// Selecting a different model in the playground changes how inputs classify.
	classify := eval.TaskWithHooks(func(ctx context.Context, input string, hooks *eval.TaskHooks) (string, error) {
		model := hooks.Parameters.String("model")

		// Record the model on the task span so it's visible in the trace.
		hooks.TaskSpan.SetAttributes(attribute.String("model", model))

		normalized := strings.ToLower(strings.TrimSpace(input))
		if model == "strict" {
			return classifyStrict(normalized), nil
		}
		return classifyRuleBased(normalized), nil
	})

	exactMatch := eval.NewScorer("exact_match",
		func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			if r.Output == r.Expected {
				return eval.S(1.0), nil
			}
			return eval.S(0.0), nil
		},
	)

	validCategory := eval.NewScorer("valid_category",
		func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			switch r.Output {
			case "fruit", "vegetable", "grain", "protein", "dairy", "unknown":
				return eval.S(1.0), nil
			default:
				return eval.S(0.0), nil
			}
		},
	)

	// The Dataset here is what `bt eval` uses; a playground run supplies its own
	// dataset instead, which takes precedence.
	//
	// The last case is a deliberate miss: the task answers "unknown" for grilled
	// chicken, so it scores exact_match = 0 but valid_category = 1, showing what
	// a non-perfect run looks like.
	return &eval.Eval[string, string]{
		Name: "food-classifier",
		Task: classify,

		// The "model" parameter becomes a model-picker control in the
		// playground; the value selected there reaches the task above via
		// hooks.Parameters, falling back to the default declared here.
		ParameterSchema: eval.ParameterSchema{
			"model": {
				Type:        eval.ParameterTypeModel,
				Default:     "rule-based",
				Description: "Classification strategy: rule-based (lenient) or strict",
			},
		},

		Scorers:     []eval.Scorer[string, string]{exactMatch, validCategory},
		ProjectName: "go-sdk-examples",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "A crisp red apple", Expected: "fruit"},
			{Input: "Fresh banana", Expected: "fruit"},
			{Input: "Crunchy carrot sticks", Expected: "vegetable"},
			{Input: "Romaine lettuce", Expected: "vegetable"},
			{Input: "Grilled chicken breast", Expected: "protein"},
		}),
	}
}

// classifyRuleBased matches on substrings, so descriptive phrases still classify
// (e.g. "a crisp red apple" -> fruit).
func classifyRuleBased(input string) string {
	switch {
	case strings.Contains(input, "apple") || strings.Contains(input, "banana"):
		return "fruit"
	case strings.Contains(input, "carrot") || strings.Contains(input, "lettuce"):
		return "vegetable"
	default:
		return "unknown"
	}
}

// classifyStrict only classifies an exact, single-word input, so descriptive
// phrases fall through to "unknown" — a deliberately stricter strategy.
func classifyStrict(input string) string {
	switch input {
	case "apple", "banana":
		return "fruit"
	case "carrot", "lettuce":
		return "vegetable"
	default:
		return "unknown"
	}
}
