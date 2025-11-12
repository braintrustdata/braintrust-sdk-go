package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	temporal "github.com/braintrustdata/braintrust-sdk-go/examples/temporal"
)

func main() {
	// Initialize Braintrust tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalln("Unable to initialize Braintrust tracing:", err)
	}

	// Set the tracer provider globally
	otel.SetTracerProvider(tp)

	// Configure propagators for distributed tracing
	// Required to propagate braintrust.parent across process boundaries
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Get tracer for Temporal
	tracer := otel.Tracer("temporal-braintrust")

	// Create OpenTelemetry interceptor for Temporal
	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{
		Tracer: tracer,
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal tracing interceptor:", err)
	}

	// Create Temporal client with OpenTelemetry interceptor
	c, err := client.Dial(client.Options{
		HostPort:     client.DefaultHostPort,
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	defer c.Close()

	// Task function that executes a workflow
	task := func(ctx context.Context, input int) (string, error) {
		workflowOptions := client.StartWorkflowOptions{
			ID:        fmt.Sprintf("w-%d", input),
			TaskQueue: temporal.TaskQueue,
		}

		we, err := c.ExecuteWorkflow(ctx, workflowOptions, temporal.ProcessWorkflow, input)
		if err != nil {
			return "", fmt.Errorf("failed to execute workflow: %w", err)
		}

		var result string
		err = we.Get(ctx, &result)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow result: %w", err)
		}

		return result, nil
	}

	// Run Braintrust eval
	evaluator := braintrust.NewEvaluator[int, string](bt)
	_, err = evaluator.Run(context.Background(), eval.Opts[int, string]{
		Experiment: "temporal-distributed-tracing",
		Dataset: eval.NewDataset([]eval.Case[int, string]{
			{Input: 10, Expected: "processed number: 10"},
			{Input: 20, Expected: "processed number: 20"},
			{Input: 30, Expected: "processed number: 30"},
		}),
		Task: eval.T(task),
		Scorers: []eval.Scorer[int, string]{
			eval.NewScorer("exact_match", func(_ context.Context, taskResult eval.TaskResult[int, string]) (eval.Scores, error) {
				score := 0.0
				if taskResult.Expected == taskResult.Output {
					score = 1.0
				}
				return eval.S(score), nil
			}),
		},
	})

	if err != nil {
		log.Fatalf("Eval failed: %v", err)
	}
}
