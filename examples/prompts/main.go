// This example demonstrates using a Braintrust prompt in an evaluation.
//
// To run this example:
//  1. Set BRAINTRUST_API_KEY environment variable
//  2. Create a prompt in Braintrust with slug "sdk-greeter-prompt-195e" in the "go-sdk-examples" project
//  3. Run: go run examples/prompts/main.go
package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	fmt.Println("Example: Using a Braintrust prompt in an evaluation")
	fmt.Println("Note: This example requires a prompt with slug 'sdk-greeter-prompt-195e' to exist in the 'go-sdk-examples' project")
	fmt.Println()

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Error initializing Braintrust: %v", err)
	}

	// Create evaluator
	evaluator := braintrust.NewEvaluator[string, string](bt)

	ctx := context.Background()

	// Example dataset with test cases
	cases := []eval.Case[string, string]{
		{
			Input:    "Joe",
			Expected: "Hi Joe",
		},
		{
			Input:    "Jane",
			Expected: "Hello Jane",
		},
		{
			Input:    "Bob",
			Expected: "Hi Bob",
		},
	}

	// Get hosted task/prompt
	task, err := evaluator.Tasks().Get(ctx, "sdk-greeter-prompt-195e")
	if err != nil {
		log.Fatalf("Failed to get task: %v", err)
	}

	// Run evaluation using the hosted prompt
	_, err = evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "greeter-test",
		Dataset:    eval.NewCases(cases),
		Task:       task,

		// Add scorers - simple equals scorer
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("equals", func(ctx context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
				if taskResult.Output == taskResult.Expected {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
	})

	if err != nil {
		log.Fatalf("Eval failed: %v", err)
	}
}
