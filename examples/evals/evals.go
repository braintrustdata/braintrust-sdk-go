// This example demonstrates basic evals with Braintrust.

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {

	log.Println("Starting eval")

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

	client := openai.NewClient(
		option.WithMiddleware(traceopenai.NewMiddleware(traceopenai.WithTracerProvider(tp))),
	)

	getFoodType := func(ctx context.Context, food string) (string, error) {
		fmt.Println("getFoodType", food)
		input := fmt.Sprintf("What kind of food is %s?", food)
		params := responses.ResponseNewParams{
			Input:        responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
			Model:        openai.ChatModelGPT4oMini,
			Instructions: openai.String("Return a one word answer."),
		}
		resp, err := client.Responses.New(ctx, params)
		if err != nil {
			return "", err
		}
		return resp.OutputText(), nil
	}

	evaluator := braintrust.NewEvaluator[string, string](bt)
	_, err = evaluator.Run(context.Background(), eval.Opts[string, string]{
		Experiment: "go-eval-example",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "strawberry", Expected: "fruit", Metadata: map[string]interface{}{"color": "red"}},
			{Input: "asparagus", Expected: "vegetable", Metadata: map[string]interface{}{"color": "green"}},
			{Input: "apple", Expected: "fruit", Metadata: map[string]interface{}{"color": "red"}},
			{Input: "banana", Expected: "fruit", Metadata: map[string]interface{}{"color": "yellow"}},
		}),
		Task: eval.T(getFoodType),
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("fruit_scorer", func(_ context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
				v := 0.0
				if taskResult.Output == "fruit" {
					v = 1.0
				}
				return eval.S(v), nil
			}),
			eval.NewScorer("vegetable_scorer", func(_ context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
				v := 0.0
				if taskResult.Output == "vegetable" {
					v = 1.0
				}
				return eval.S(v), nil
			}),
		},
		Tags: []string{"example", "food-classifier", "gpt-4o-mini"},
		Metadata: map[string]interface{}{
			"model":       "gpt-4o-mini",
			"description": "Classifies food items as fruit or vegetable",
		},
		Parallelism: 5,
	})
	if err != nil {
		log.Fatalf("Error running eval: %v", err)
	}
}
