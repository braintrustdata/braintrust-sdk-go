// This example shows how to run an eval against a dataset downloaded
// from braintrust.dev.

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
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
func initializeDataset(ctx context.Context, apiClient *openapi.ClientWithResponses, projectID uuid.UUID) (string, error) {
	// Create a dataset with timestamp to make it unique
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	datasetInfo, err := apiClient.PostDatasetWithResponse(ctx, openapi.CreateDataset{
		ProjectId:   projectID,
		Name:        "Sample Struct Dataset " + timestamp,
		Description: nullable.NewNullableWithValue("A sample dataset for demonstrating struct-based evaluation"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %v", err)
	}
	if datasetInfo.StatusCode() != http.StatusOK || datasetInfo.JSON200 == nil {
		return "", fmt.Errorf("failed to create dataset: HTTP %d: %s", datasetInfo.StatusCode(), datasetInfo.Body)
	}

	// Insert some sample data into the dataset
	sampleEvents := []openapi.InsertDatasetEvent{
		{
			Input: nullable.NewNullableWithValue[any](map[string]interface{}{
				"text":     "hello world",
				"context":  "Basic capitalization",
				"language": "en",
			}),
			Expected: nullable.NewNullableWithValue[any](map[string]interface{}{
				"response": "Hello World",
			}),
			Tags: nullable.NewNullableWithValue([]string{"basic", "english"}),
		},
		{
			Input: nullable.NewNullableWithValue[any](map[string]interface{}{
				"text":     "braintrust is awesome",
				"context":  "Company name capitalization",
				"language": "en",
			}),
			Expected: nullable.NewNullableWithValue[any](map[string]interface{}{
				"response": "Braintrust Is Awesome",
			}),
			Tags: nullable.NewNullableWithValue([]string{"company", "english"}),
		},
		{
			Input: nullable.NewNullableWithValue[any](map[string]interface{}{
				"text":     "artificial intelligence",
				"context":  "Technical term capitalization",
				"language": "en",
			}),
			Expected: nullable.NewNullableWithValue[any](map[string]interface{}{
				"response": "Artificial Intelligence",
			}),
			Tags: nullable.NewNullableWithValue([]string{"technical", "english"}),
		},
	}

	inserted, err := apiClient.PostDatasetIdInsertWithResponse(ctx, datasetInfo.JSON200.Id.String(), openapi.InsertDatasetEventRequest{Events: sampleEvents})
	if err != nil {
		return "", fmt.Errorf("failed to insert events: %w", err)
	}
	if inserted.StatusCode() != http.StatusOK || inserted.JSON200 == nil {
		return "", fmt.Errorf("failed to insert events: HTTP %d: %s", inserted.StatusCode(), inserted.Body)
	}

	return datasetInfo.JSON200.Id.String(), nil
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

	ctx := context.Background()
	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if apiKey == "" {
		log.Fatal("BRAINTRUST_API_KEY is required")
	}
	baseURL := os.Getenv("BRAINTRUST_API_URL")
	if baseURL == "" {
		baseURL = "https://api.braintrust.dev"
	}
	apiClient, err := openapi.NewClientWithResponses(baseURL,
		openapi.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+apiKey)
			return nil
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create REST client: %v", err)
	}

	// First, create a project
	project, err := apiClient.PostProjectWithResponse(ctx, openapi.CreateProject{
		Name: "go-sdk-examples",
	})
	if err != nil {
		log.Fatalf("Failed to create project: %v", err)
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		log.Fatalf("Failed to create project: HTTP %d: %s", project.StatusCode(), project.Body)
	}

	// Initialize dataset
	datasetID, err := initializeDataset(ctx, apiClient, project.JSON200.Id)
	if err != nil {
		log.Fatalf("Failed to initialize dataset: %v", err)
	}

	evaluator := braintrust.NewEvaluator[QuestionInput, AnswerExpected](bt)

	// Fetch the dataset cases
	cases, err := evaluator.Datasets().Get(context.Background(), datasetID)
	if err != nil {
		log.Fatalf("Failed to get dataset: %v", err)
	}

	_, err = evaluator.Run(context.Background(), eval.Opts[QuestionInput, AnswerExpected]{
		Experiment: "Capitalization Task Demo",
		Dataset:    cases, // Use fetched dataset
		Task: eval.T(func(ctx context.Context, input QuestionInput) (AnswerExpected, error) {
			// Simple example: capitalize the first letter of each word

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
