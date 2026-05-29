// This example shows how to use classifiers to categorize task outputs
// alongside (or instead of) numeric scorers.

package main

import (
	"context"
	"log"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	log.Println("🏷  Classifiers Example")

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

	evaluator := braintrust.NewEvaluator[string, string](bt)

	// Single-label classifier: returns one category per input.
	intent := eval.NewClassifier("intent", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		switch {
		case regexp.MustCompile(`(?i)thank`).MatchString(r.Input):
			return eval.Classifications{{ID: "praise", Label: "Praise"}}, nil
		case regexp.MustCompile(`(?i)password|reset`).MatchString(r.Input):
			return eval.Classifications{{ID: "how_to", Label: "How To"}}, nil
		case regexp.MustCompile(`(?i)damaged|refund`).MatchString(r.Input):
			return eval.Classifications{{ID: "complaint", Label: "Complaint"}}, nil
		default:
			return eval.Classifications{{ID: "other"}}, nil
		}
	})

	// Multi-label classifier: returns several classifications per input,
	// all under the same name. The platform groups them together.
	tone := eval.NewClassifier("tone", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		var out eval.Classifications
		if strings.Contains(strings.ToLower(r.Input), "immediately") {
			out = append(out, eval.Classification{ID: "urgent", Label: "Urgent"})
		}
		if strings.Contains(strings.ToLower(r.Input), "please") {
			out = append(out, eval.Classification{ID: "polite", Label: "Polite"})
		}
		return out, nil
	})

	// Classifier with metadata: enriches each classification with structured
	// context (word count, etc.).
	responseQuality := eval.NewClassifier("response_quality", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		words := len(strings.Fields(r.Output))
		var id string
		switch {
		case strings.TrimSpace(r.Output) == "":
			id = "no_response"
		case words < 5:
			id = "too_short"
		default:
			id = "informational"
		}
		return eval.Classifications{{
			ID:    id,
			Label: strings.Title(strings.ReplaceAll(id, "_", " ")), //nolint:staticcheck
			Metadata: map[string]any{
				"word_count": words,
			},
		}}, nil
	})

	// Classifiers can run alongside scorers in the same eval.
	exactMatch := eval.NewScorer("exact_match", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
		if r.Output == r.Expected {
			return eval.S(1.0), nil
		}
		return eval.S(0.0), nil
	})

	log.Println("🚀 Running evaluation...")
	_, err = evaluator.Run(context.Background(), eval.Opts[string, string]{
		Experiment: "go-sdk-examples-classifiers",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "Thanks for the great support!", Expected: "praise"},
			{Input: "Please reset my password immediately.", Expected: "how_to"},
			{Input: "My order arrived damaged, I want a refund.", Expected: "complaint"},
		}),
		Task: eval.T(func(_ context.Context, input string) (string, error) {
			return "Thank you for reaching out. We'll look into it shortly.", nil
		}),
		Scorers:     []eval.Scorer[string, string]{exactMatch},
		Classifiers: []eval.Classifier[string, string]{intent, tone, responseQuality},
	})
	if err != nil {
		log.Printf("⚠️  Eval completed with errors: %v", err)
	} else {
		log.Println("✅ Eval completed successfully")
	}
}
