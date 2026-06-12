// This example demonstrates running each eval case multiple times with TrialCount.
// This is useful for nondeterministic tasks where you want repeated measurements per input.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
	)
	if err != nil {
		log.Fatalf("Error creating Braintrust client: %v", err)
	}

	var taskRuns int
	task := func(ctx context.Context, input string) (string, error) {
		taskRuns++
		return strings.ToUpper(input), nil
	}

	scorer := eval.NewScorer("exact_match", func(ctx context.Context, result eval.TaskResult[string, string]) (eval.Scores, error) {
		if result.Output == result.Expected {
			return eval.S(1), nil
		}
		return eval.S(0), nil
	})

	cases := []eval.Case[string, string]{
		{Input: "hello", Expected: "HELLO"},                // Uses global TrialCount (2)
		{Input: "world", Expected: "WORLD", TrialCount: 3}, // Overrides global TrialCount
	}

	evaluator := braintrust.NewEvaluator[string, string](bt)
	result, err := evaluator.Run(context.Background(), eval.Opts[string, string]{
		Experiment: fmt.Sprintf("trial-count-demo-%d", time.Now().Unix()),
		Dataset:    eval.NewDataset(cases),
		Task:       eval.T(task),
		Scorers:    []eval.Scorer[string, string]{scorer},
		TrialCount: 2,
	})
	if err != nil {
		log.Fatalf("Eval failed: %v", err)
	}

	permalink, _ := result.Permalink()
	log.Printf("Eval complete after %d task runs: %s", taskRuns, permalink)
}
