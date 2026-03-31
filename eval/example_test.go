package eval_test

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// Example demonstrates how to run a basic evaluation.
func Example() {
	ctx := context.Background()

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Create Braintrust client (reads BRAINTRUST_API_KEY from environment)
	client, err := braintrust.New(tp, braintrust.WithProject("test-project"))
	if err != nil {
		log.Fatal(err)
	}

	// Create an evaluator with string input and output types
	evaluator := braintrust.NewEvaluator[string, string](client)

	// Define a simple task that adds exclamation marks
	task := eval.T(func(ctx context.Context, input string) (string, error) {
		return input + "!", nil
	})

	// Create test cases
	dataset := eval.NewDataset([]eval.Case[string, string]{
		{Input: "hello", Expected: "hello!"},
		{Input: "world", Expected: "world!"},
	})

	// Create a scorer
	scorer := eval.NewScorer("exact-match", func(ctx context.Context, result eval.TaskResult[string, string]) (eval.Scores, error) {
		if result.Output == result.Expected {
			return eval.S(1.0), nil
		}
		return eval.S(0.0), nil
	})

	// Run the evaluation
	result, err := evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "example-eval",
		Dataset:    dataset,
		Task:       task,
		Scorers:    []eval.Scorer[string, string]{scorer},
		Quiet:      true,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Evaluation complete: %s\n", result.Name())
}

// Example_evalDefinition demonstrates how to define a reusable evaluation
// that can be run locally or registered with a remote eval server.
func Example_evalDefinition() {
	ctx := context.Background()

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Create Braintrust client
	client, err := braintrust.New(tp, braintrust.WithProject("test-project"))
	if err != nil {
		log.Fatal(err)
	}

	// Define a reusable eval (task + scorers + project)
	classify := &eval.Eval[string, string]{
		Name: "classify",
		Task: eval.T(func(ctx context.Context, input string) (string, error) {
			return input + "!", nil
		}),
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("exact-match", func(ctx context.Context, result eval.TaskResult[string, string]) (eval.Scores, error) {
				if result.Output == result.Expected {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
		ProjectName: "test-project",
	}

	// Run the eval with a specific dataset
	evaluator := braintrust.NewEvaluator[string, string](client)
	result, err := evaluator.RunEval(ctx, classify, eval.RunOpts[string, string]{
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "hello", Expected: "hello!"},
		}),
		Quiet: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Evaluation complete: %s\n", result.Name())
}
