// Kitchen-sink example for classifiers. Exercises every supported pattern
// so we can validate the feature end-to-end against a real workspace.

package main

import (
	"context"
	"errors"
	"log"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("init braintrust: %v", err)
	}

	evaluator := braintrust.NewEvaluator[string, string](bt)

	// 1. Single-label classifier. Empty Name on the Classification
	//    defaults to the classifier's name ("category").
	singleLabel := eval.NewClassifier("category", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		if strings.Contains(strings.ToLower(r.Input), "hello") {
			return eval.Classifications{{ID: "greeting"}}, nil
		}
		return eval.Classifications{{ID: "other"}}, nil
	})

	// 2. Multi-label classification: several items under the same name.
	multiLabel := eval.NewClassifier("tone", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		var out eval.Classifications
		lower := strings.ToLower(r.Input)
		if strings.Contains(lower, "please") {
			out = append(out, eval.Classification{ID: "polite", Label: "Polite"})
		}
		if strings.Contains(lower, "immediately") || strings.Contains(lower, "now") {
			out = append(out, eval.Classification{ID: "urgent", Label: "Urgent"})
		}
		if len(out) == 0 {
			out = append(out, eval.Classification{ID: "neutral", Label: "Neutral"})
		}
		return out, nil
	})

	// 3. Classifier returning no classifications for some inputs.
	emptyAllowed := eval.NewClassifier("flag", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		if strings.Contains(strings.ToLower(r.Output), "error") {
			return eval.Classifications{{ID: "contains_error"}}, nil
		}
		return nil, nil
	})

	// 4. Classifier with rich metadata payloads.
	withMetadata := eval.NewClassifier("length_bucket", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Classifications, error) {
		words := len(strings.Fields(r.Output))
		bucket := "short"
		switch {
		case words > 50:
			bucket = "long"
		case words > 10:
			bucket = "medium"
		}
		return eval.Classifications{{
			ID:    bucket,
			Label: strings.Title(bucket), //nolint:staticcheck
			Metadata: map[string]any{
				"words":      words,
				"characters": len(r.Output),
			},
		}}, nil
	})

	// 5. A classifier that fails — confirms non-fatal error handling and
	//    that classifier_errors lands in the eval span's metadata.
	broken := eval.NewClassifier("broken", func(_ context.Context, _ eval.TaskResult[string, string]) (eval.Classifications, error) {
		return nil, errors.New("intentional failure")
	})

	// 6. A regular scorer running in parallel with the classifiers, to
	//    confirm both passes happen concurrently per case.
	exactMatch := eval.NewScorer("exact_match", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
		if r.Output == r.Expected {
			return eval.S(1.0), nil
		}
		return eval.S(0.0), nil
	})

	log.Println("Running classifiers kitchen-sink eval...")
	result, err := evaluator.Run(context.Background(), eval.Opts[string, string]{
		Experiment: "go-sdk-internal-classifiers",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "Hello there", Expected: "Hi"},
			{Input: "Please reply immediately.", Expected: "Sure"},
			{Input: "trigger an error message", Expected: "ok"},
		}),
		Task: eval.T(func(_ context.Context, input string) (string, error) {
			return "Acknowledged: " + input, nil
		}),
		Scorers:     []eval.Scorer[string, string]{exactMatch},
		Classifiers: []eval.Classifier[string, string]{singleLabel, multiLabel, emptyAllowed, withMetadata, broken},
		Parallelism: 2,
	})
	if err != nil {
		log.Printf("eval finished with errors (expected from 'broken'): %v", err)
	}
	if result != nil {
		log.Printf("experiment: %s", result.Name())
	}
}
