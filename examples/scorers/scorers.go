// This example shows how to use http scorers (e.g. scorers defined on
// braintrust.dev) and code scorers.

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	log.Println("🧪 Testing Online Scorers")

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Braintrust: %v", err)
	}

	// Create evaluator
	evaluator := braintrust.NewEvaluator[int, int](bt)

	// Helper function for absolute value
	abs := func(x int) int {
		if x < 0 {
			return -x
		}
		return x
	}

	// Build scorers list with local scorers
	scorers := []eval.Scorer[int, int]{
		// Simple equals scorer
		eval.NewScorer("equals", func(ctx context.Context, taskResult eval.TaskResult[int, int]) (eval.Scores, error) {
			if taskResult.Output == taskResult.Expected {
				return eval.S(1.0), nil
			}
			return eval.S(0.0), nil
		}),
		// an example of a scorer that returns a single number
		eval.NewScorer[int, int]("random", func(ctx context.Context, taskResult eval.TaskResult[int, int]) (eval.Scores, error) {
			score := rand.Float64()
			return eval.S(score), nil
		}),
		// an example of a scorer that returns more than one score
		eval.NewScorer[int, int]("list", func(ctx context.Context, taskResult eval.TaskResult[int, int]) (eval.Scores, error) {
			return eval.Scores{
				{Name: "poor", Score: 0},
				{Name: "average", Score: 0.5},
				{Name: "excellent", Score: 1},
			}, nil
		}),
		// an example of a scorer that returns a score with metadata
		eval.NewScorer[int, int]("scorer_with_metadata", func(ctx context.Context, taskResult eval.TaskResult[int, int]) (eval.Scores, error) {
			diff := taskResult.Output - taskResult.Expected
			accuracy := 1.0
			if diff != 0 {
				accuracy = 1.0 / (1.0 + float64(abs(diff)))
			}

			return eval.Scores{
				{
					Name:  "scorer_with_metadata",
					Score: accuracy,
					Metadata: map[string]any{
						"input":      taskResult.Input,
						"expected":   taskResult.Expected,
						"result":     taskResult.Output,
						"difference": diff,
						"is_exact":   diff == 0,
						"error_rate": float64(abs(diff)) / float64(taskResult.Expected),
					},
				},
			}, nil
		}),
	}

	// Try to get online scorer - add if available
	onlineScorer, err := evaluator.Scorers().Get(context.Background(), "fail-scorer-d879")
	if err != nil {
		log.Printf("⚠️ Online scorer not available: %v", err)
		log.Println("📊 Running with local scorers only...")
	} else {
		log.Println("✅ Online scorer available, adding to list")
		scorers = append(scorers, onlineScorer)
	}

	log.Println("🚀 Running evaluation...")
	_, err = evaluator.Run(context.Background(), eval.Opts[int, int]{
		Experiment: "go-sdk-examples",
		Dataset: eval.NewDataset([]eval.Case[int, int]{
			{Input: 5, Expected: 10},
			{Input: 3, Expected: 6},
			{Input: 7, Expected: 14},
		}),
		Task: eval.T(func(ctx context.Context, input int) (int, error) {
			return input * 2, nil
		}),
		Scorers: scorers,
	})
	if err != nil {
		log.Printf("⚠️ Eval completed with errors: %v", err)
	} else {
		log.Println("✅ Eval completed successfully")
	}

	fmt.Println("Done! Check the Braintrust UI to see results.")
}
