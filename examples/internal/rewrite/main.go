package main

import (
	"context"
	"fmt"
	"log"
	"reflect"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	// Create Braintrust client with the TracerProvider
	client, err := braintrust.New(tp,
		braintrust.WithProject("rewrite-test"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Failed to create Braintrust client: %v", err)
	}

	// Demonstrate manual tracing with two spans
	demonstrateManualTracing()

	// Demonstrate eval APIs
	exampleNewEvaluator(client)
}

func demonstrateManualTracing() {
	tracer := otel.Tracer("rewrite-example")
	ctx := context.Background()

	// Span 1: Parent operation
	_, span := tracer.Start(ctx, "parent_operation")
	defer span.End()
	span.SetAttributes(
		attribute.String("example.type", "parent"),
		attribute.Int("example.id", 1),
	)

	// Generate permalink
	_, _ = bttrace.Permalink(span)
}

// exactMatch is a simple scorer that checks if output matches expected.
// This is defined locally in the example to show how to create custom scorers.
func exactMatch[I, R any]() eval.Scorer[I, R] {
	return eval.NewScorer("exact_match", func(ctx context.Context, tr eval.TaskResult[I, R]) (eval.Scores, error) {
		s := 0.0
		if reflect.DeepEqual(tr.Output, tr.Expected) {
			s = 1.0
		}
		return eval.S(s), nil
	})
}

// exampleNewEvaluator demonstrates the braintrust.NewEvaluator() API for reusable evaluators.
func exampleNewEvaluator(client *braintrust.Client) {
	ctx := context.Background()

	// Create a reusable evaluator for string → string evaluations
	evaluator := braintrust.NewEvaluator[string, string](client)

	// Define a simple task: greeting generator
	task := eval.T(func(ctx context.Context, input string) (string, error) {
		tracer := otel.Tracer("rewrite-example")
		_, span := tracer.Start(ctx, "manual-span")
		defer span.End()
		span.SetAttributes(
			attribute.String("example.type", "manual"),
			attribute.Int("example.id", 1),
		)
		return fmt.Sprintf("Hello, %s!", input), nil
	})

	// Run first evaluation
	cases1 := eval.NewDataset([]eval.Case[string, string]{
		{Input: "World", Expected: "Hello, World!"},
		{Input: "Alice", Expected: "Hello, Alice!"},
	})

	_, err := evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "greeting-evaluator-1",
		Dataset:    cases1,
		Task:       task,
		Scorers: []eval.Scorer[string, string]{
			exactMatch[string, string](),
		},
	})

	if err != nil {
		log.Printf("Error running eval 1: %v", err)
	}

	// Run second evaluation with the same evaluator
	cases2 := eval.NewDataset([]eval.Case[string, string]{
		{Input: "Bob", Expected: "Hello, Bob!"},
		{Input: "Charlie", Expected: "Hello, Charlie!"},
	})

	_, err = evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "greeting-evaluator-2",
		Dataset:    cases2,
		Task:       task,
		Scorers: []eval.Scorer[string, string]{
			exactMatch[string, string](),
		},
	})
	if err != nil {
		log.Printf("Error running eval 2: %v", err)
	}
}
