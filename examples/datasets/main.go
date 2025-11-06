// This example shows how to run an eval against a dataset downloaded
// from braintrust.dev.

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// QuestionInput represents the input structure for a question
type QuestionInput struct {
	Text     string `json:"text"`
	Context  string `json:"context"`
	Language string `json:"language"`
}

// AnswerExpected represents the expected output structure
type AnswerExpected struct {
	Response string `json:"response"`
}

// initializeDataset creates a dataset with sample data and returns the dataset ID
func initializeDataset(bt *braintrust.Client, projectID string) (string, error) {
	// Create a dataset with timestamp to make it unique
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	datasetInfo, err := bt.API().Datasets().Create(context.Background(), datasets.CreateParams{
		ProjectID:   projectID,
		Name:        "Sample Struct Dataset " + timestamp,
		Description: "A sample dataset for demonstrating struct-based evaluation",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %v", err)
	}
	fmt.Printf("📊 Created dataset: %s (ID: %s)\n", datasetInfo.Name, datasetInfo.ID)

	// Insert some sample data into the dataset
	sampleEvents := []datasets.Event{
		{
			Input: map[string]interface{}{
				"text":     "hello world",
				"context":  "Basic capitalization",
				"language": "en",
			},
			Expected: map[string]interface{}{
				"response": "Hello World",
			},
			Tags: []string{"basic", "english"},
		},
		{
			Input: map[string]interface{}{
				"text":     "braintrust is awesome",
				"context":  "Company name capitalization",
				"language": "en",
			},
			Expected: map[string]interface{}{
				"response": "Braintrust Is Awesome",
			},
			Tags: []string{"company", "english"},
		},
		{
			Input: map[string]interface{}{
				"text":     "artificial intelligence",
				"context":  "Technical term capitalization",
				"language": "en",
			},
			Expected: map[string]interface{}{
				"response": "Artificial Intelligence",
			},
			Tags: []string{"technical", "english"},
		},
	}

	err = bt.API().Datasets().Insert(context.Background(), datasetInfo.ID, datasets.InsertParams{Events: sampleEvents})
	if err != nil {
		return "", fmt.Errorf("failed to insert events: %v", err)
	}
	fmt.Printf("📝 Inserted %d events\n", len(sampleEvents))

	return datasetInfo.ID, nil
}

func main() {
	// Initialize OpenTelemetry tracing for Braintrust
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("Failed to initialize Braintrust: %v", err)
	}

	// First, create a project
	project, err := bt.API().Projects().Create(context.Background(), projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		log.Fatalf("Failed to create project: %v", err)
	}
	fmt.Printf("📁 Created project: %s (ID: %s)\n", project.Name, project.ID)

	// Initialize dataset
	datasetID, err := initializeDataset(bt, project.ID)
	if err != nil {
		log.Fatalf("Failed to initialize dataset: %v", err)
	}

	fmt.Println("\n🚀 Running evaluation (limiting to 2 of 3 rows)...")
	evaluator := braintrust.NewEvaluator[QuestionInput, AnswerExpected](bt)

	// Fetch the dataset cases
	cases, err := evaluator.Datasets().Get(context.Background(), datasetID)
	if err != nil {
		log.Fatalf("Failed to get dataset: %v", err)
	}

	_, err = evaluator.Run(context.Background(), eval.Opts[QuestionInput, AnswerExpected]{
		Experiment: "Capitalization Task Demo",
		Cases:      cases, // Use fetched cases
		Task: eval.T(func(ctx context.Context, input QuestionInput) (AnswerExpected, error) {
			// Simple example: capitalize the first letter of each word
			fmt.Printf("🔄 Processing text: '%s' (context: %s, language: %s)\n",
				input.Text, input.Context, input.Language)

			// Simple capitalization logic - capitalize first letter of each word
			words := strings.Fields(input.Text)
			capitalizedWords := make([]string, len(words))

			for i, word := range words {
				if len(word) > 0 {
					capitalizedWords[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
				} else {
					capitalizedWords[i] = word
				}
			}

			response := strings.Join(capitalizedWords, " ")

			return AnswerExpected{
				Response: response,
			}, nil
		}),
		Scorers: []eval.Scorer[QuestionInput, AnswerExpected]{
			eval.NewScorer("equals", func(ctx context.Context, taskResult eval.TaskResult[QuestionInput, AnswerExpected]) (eval.Scores, error) {
				if taskResult.Output.Response == taskResult.Expected.Response {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
	})
	if err != nil {
		log.Fatalf("Evaluation failed: %v", err)
	}
}
