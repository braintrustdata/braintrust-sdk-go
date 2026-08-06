// This example demonstrates running a remote eval server that exposes
// evaluators to the Braintrust UI. The Braintrust playground can then
// trigger evaluations against your locally running code.
//
// Start the server:
//
//	go run examples/internal/eval-server/main.go
//
// Then configure the endpoint (http://localhost:8300) in your Braintrust
// project settings under Remote evals.
package main

import (
	"context"
	"log"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/server"
)

func main() {
	// Create the eval server.
	// Use WithNoAuth() for local development; remove for production.
	// Use WithTracerProvider(tp) to include user-instrumented spans (LLM clients,
	// custom spans) in eval traces.
	srv := server.New(
		server.WithAddress("localhost:8300"),
		server.WithNoAuth(),
	)

	// Define the task with TaskWithHooks so it can read the "model" parameter
	// the user selects in the playground. Two modes are supported:
	//
	//   - "rule-based" (default): lenient substring matching.
	//   - "strict":               only an exact, single-word match counts.
	//
	// Selecting a different model in the playground changes how inputs classify.
	classifyTask := eval.TaskWithHooks(func(ctx context.Context, input string, hooks *eval.TaskHooks) (string, error) {
		model := hooks.Parameters.String("model")

		// Record the model on the task span so it's visible in the trace.
		hooks.TaskSpan.SetAttributes(attribute.String("model", model))

		normalized := strings.ToLower(strings.TrimSpace(input))
		if model == "strict" {
			return classifyStrict(normalized), nil
		}
		return classifyRuleBased(normalized), nil
	})

	// Define a scorer: exact match.
	exactMatch := eval.NewScorer("exact_match",
		func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			if r.Output == r.Expected {
				return eval.S(1.0), nil
			}
			return eval.S(0.0), nil
		},
	)

	// Define a scorer: checks output is a valid food category.
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

	// Define the eval.
	foodClassifier := &eval.Eval[string, string]{
		Name:        "food-classifier",
		Task:        classifyTask,
		Scorers:     []eval.Scorer[string, string]{exactMatch, validCategory},
		ProjectName: "go-sdk-examples",
	}

	// Register with the server. The "model" parameter is a model-picker control
	// in the playground; its selected value reaches the task via hooks.Parameters.
	server.RegisterEval(srv, foodClassifier, server.RegisterEvalOpts{
		Parameters: &server.Parameters{
			Schema: map[string]server.ParameterDef{
				"model": {
					Type:        "model",
					Default:     "rule-based",
					Description: "Classification strategy: rule-based (lenient) or strict",
				},
			},
		},
	},
	)

	log.Printf("Eval server starting on localhost:8300")
	log.Printf("Registered evaluators: food-classifier")
	log.Printf("Health check: http://localhost:8300/")
	log.Printf("List evals:   http://localhost:8300/list")
	log.Fatal(srv.Start())
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
