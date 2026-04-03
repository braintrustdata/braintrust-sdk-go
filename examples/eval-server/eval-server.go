// This example demonstrates running a remote eval server that exposes
// evaluators to the Braintrust UI. The Braintrust playground can then
// trigger evaluations against your locally running code.
//
// Start the server:
//
//	go run examples/eval-server/eval-server.go
//
// Then configure the endpoint (http://localhost:8300) in your Braintrust
// project settings under Remote evals.
package main

import (
	"context"
	"log"
	"strings"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/server"
)

func main() {
	// Create the eval server.
	// Use WithNoAuth() for local development; remove for production.
	// Use WithTracerProvider(tp) to share an OpenTelemetry TracerProvider so
	// user-instrumented spans (LLM clients, custom spans) appear in the same
	// trace as eval spans.
	srv := server.New(
		server.WithAddress("localhost:8300"),
		server.WithNoAuth(),
	)

	// Define a simple task: classify food items.
	classifyTask := eval.T(func(ctx context.Context, input string) (string, error) {
		input = strings.ToLower(input)
		switch {
		case strings.Contains(input, "apple") || strings.Contains(input, "banana"):
			return "fruit", nil
		case strings.Contains(input, "carrot") || strings.Contains(input, "lettuce"):
			return "vegetable", nil
		default:
			return "unknown", nil
		}
	})

	// Define scorers. Scorers run server-side as part of the evaluation and
	// their results are exported to Braintrust via OTLP spans.
	exactMatch := eval.NewScorer("exact_match",
		func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			if r.Output == r.Expected {
				return eval.S(1.0), nil
			}
			return eval.S(0.0), nil
		},
	)

	// Register the evaluator with the server.
	server.RegisterEval(srv, &eval.Eval[string, string]{
		Name:        "food-classifier",
		Task:        classifyTask,
		Scorers:     []eval.Scorer[string, string]{exactMatch},
		ProjectName: "go-sdk-examples",
	}, server.RegisterEvalOpts{
		Parameters: &server.Parameters{
			Schema: map[string]server.ParameterDef{
				"model": {
					Type:        "string",
					Default:     "rule-based",
					Description: "Classification model to use",
				},
			},
		},
	})

	log.Printf("Eval server starting on localhost:8300")
	log.Printf("Registered evaluators: food-classifier")
	log.Printf("Health check: http://localhost:8300/")
	log.Printf("List evals:   http://localhost:8300/list")
	log.Fatal(srv.Start())
}
