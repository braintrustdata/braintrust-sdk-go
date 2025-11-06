package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// QuestionInput represents a question
type QuestionInput struct {
	Question string `json:"question"`
}

// AnswerOutput represents an answer
type AnswerOutput struct {
	Answer string `json:"answer"`
}

func main() {
	ctx := context.Background()

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() {
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()

	client, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
	)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Get API client for dataset operations
	apiClient := client.API()

	// Step 1: Create a prompt function for answering questions
	fmt.Println("=== Step 1: Creating prompt ===")
	promptSlug := "qa-answer-prompt"
	if err := createPrompt(ctx, apiClient, promptSlug); err != nil {
		log.Fatalf("Failed to create prompt: %v", err)
	}
	fmt.Printf("Created prompt: %s\n\n", promptSlug)

	// Step 2: Create a dataset with some test data
	fmt.Println("=== Step 2: Creating dataset ===")
	datasetID, err := createDataset(ctx, apiClient)
	if err != nil {
		log.Fatalf("Failed to create dataset: %v", err)
	}
	fmt.Printf("Created dataset: %s\n\n", datasetID)

	// Step 3: Run an evaluation using the dataset and prompt
	fmt.Println("=== Step 3: Running evaluation ===")
	evaluator := braintrust.NewEvaluator[QuestionInput, AnswerOutput](client)

	// Load dataset using the new DatasetAPI
	cases, err := evaluator.Datasets().Get(ctx, datasetID)
	if err != nil {
		log.Fatalf("Failed to load dataset: %v", err)
	}

	// Load the prompt as a task
	task, err := evaluator.Tasks().Get(ctx, promptSlug)
	if err != nil {
		log.Fatalf("Failed to load prompt: %v", err)
	}

	// Define an exact match scorer
	exactMatchScorer := eval.NewScorer("exact_match", func(ctx context.Context, result eval.TaskResult[QuestionInput, AnswerOutput]) (eval.Scores, error) {
		if result.Expected.Answer == result.Output.Answer {
			return eval.S(1.0), nil
		}
		return eval.S(0.0), nil
	})

	// Run the evaluation
	result, err := evaluator.Run(ctx, eval.Opts[QuestionInput, AnswerOutput]{
		Experiment: "qa-dataset-example",
		Cases:      cases,
		Task:       task,
		Scorers:    []eval.Scorer[QuestionInput, AnswerOutput]{exactMatchScorer},
	})
	if err != nil {
		log.Fatalf("Failed to run evaluation: %v", err)
	}

	fmt.Printf("Evaluation complete! View results at: %s\n\n", result)

	// Step 4: Cleanup - delete the test dataset
	fmt.Println("=== Step 4: Cleaning up ===")
	if err := apiClient.Datasets().Delete(ctx, datasetID); err != nil {
		// Note: Dataset deletion may fail due to permissions or timing
		// The dataset can be manually deleted from the Braintrust UI if needed
		fmt.Printf("Note: Dataset cleanup skipped (this is normal): %v\n", err)
	} else {
		fmt.Println("Dataset deleted successfully")
	}
}

// createPrompt creates a prompt function for answering questions
func createPrompt(ctx context.Context, apiClient *api.API, slug string) error {
	// First, get or create the project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// Check if the prompt already exists and delete it
	functions := apiClient.Functions()
	if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: "go-sdk-examples",
		Slug:        slug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that answers questions
	// The prompt will receive the question as input and should return an answer
	_, err = functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID: project.ID,
		Name:      "QA Answer Prompt",
		Slug:      slug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are a helpful assistant that answers questions accurately and concisely. You must respond with valid JSON in the format: {\"answer\": \"your answer here\"}",
					},
					{
						"role":    "user",
						"content": "{{input.question}}",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature":     0,
					"max_tokens":      50,
					"response_format": map[string]any{"type": "json_object"},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create prompt: %w", err)
	}

	return nil
}

// createDataset creates a test dataset and returns its ID
func createDataset(ctx context.Context, apiClient *api.API) (string, error) {
	// First, get or create the project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}

	// Create the dataset
	dataset, err := apiClient.Datasets().Create(ctx, api.DatasetRequest{
		ProjectID:   project.ID,
		Name:        "qa-test-dataset",
		Description: "Test dataset for DatasetAPI example",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %w", err)
	}

	// Insert test data
	events := []api.DatasetEvent{
		{
			Input: map[string]interface{}{
				"question": "What is 2 + 2?",
			},
			Expected: map[string]interface{}{
				"answer": "4",
			},
			Tags: []string{"math", "easy"},
		},
		{
			Input: map[string]interface{}{
				"question": "What is the capital of France?",
			},
			Expected: map[string]interface{}{
				"answer": "Paris",
			},
			Tags: []string{"geography", "easy"},
		},
		{
			Input: map[string]interface{}{
				"question": "What is the square root of 144?",
			},
			Expected: map[string]interface{}{
				"answer": "12",
			},
			Tags: []string{"math", "medium"},
		},
		{
			Input: map[string]interface{}{
				"question": "Who wrote Romeo and Juliet?",
			},
			Expected: map[string]interface{}{
				"answer": "William Shakespeare",
			},
			Tags: []string{"literature", "easy"},
		},
	}

	if err := apiClient.Datasets().Insert(ctx, dataset.ID, events); err != nil {
		return "", fmt.Errorf("failed to insert events: %w", err)
	}

	return dataset.ID, nil
}
