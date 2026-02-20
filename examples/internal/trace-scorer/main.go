package main

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	ctx := context.Background()

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx) //nolint:errcheck
	otel.SetTracerProvider(tp)

	client, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("failed to create Braintrust client: %v", err)
	}

	evaluator := braintrust.NewEvaluator[string, string](client)

	task := eval.T(func(ctx context.Context, input string) (string, error) {
		tracer := otel.Tracer("trace-scorer-example")
		_, span := tracer.Start(ctx, "task-work")
		defer span.End()
		span.SetAttributes(
			attribute.String("span_attributes.type", "custom"),
			attribute.String("example.input", input),
		)
		return "hello " + input, nil
	})

	traceAwareScorer := eval.NewScorer("trace_aware", func(ctx context.Context, tr eval.TaskResult[string, string]) (eval.Scores, error) {
		if tr.Trace == nil {
			return eval.Scores{{
				Name:  "trace_aware",
				Score: 0,
				Metadata: map[string]any{
					"error": "trace is nil",
				},
			}}, nil
		}

		allSpans := tr.Trace.GetSpans(nil)
		customSpans := tr.Trace.GetSpans([]string{"custom"})
		thread := tr.Trace.GetThread()

		log.Printf("trace info: spans=%d custom_spans=%d thread=%d", len(allSpans), len(customSpans), len(thread))

		score := 0.0
		if len(allSpans) > 0 {
			score = 1.0
		}

		return eval.Scores{{
			Name:  "trace_aware",
			Score: score,
			Metadata: map[string]any{
				"span_count":        len(allSpans),
				"custom_span_count": len(customSpans),
				"thread_count":      len(thread),
			},
		}}, nil
	})

	_, err = evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "internal-trace-scorer-demo",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "world", Expected: "hello world"},
			{Input: "team", Expected: "hello team"},
		}),
		Task:    task,
		Scorers: []eval.Scorer[string, string]{traceAwareScorer},
	})
	if err != nil {
		log.Fatalf("eval failed: %v", err)
	}

	log.Println("trace-scorer example completed")
}
