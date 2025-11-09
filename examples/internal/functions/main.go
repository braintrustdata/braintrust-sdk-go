package main

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// SentimentInput represents input for sentiment analysis
type SentimentInput struct {
	Text string `json:"text"`
}

// SentimentOutput represents the sentiment classification result
type SentimentOutput struct {
	Sentiment string `json:"sentiment"` // "positive", "negative", or "neutral"
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

	// Initialize Braintrust client
	client, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
	)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	apiClient := client.API()

	// Step 1: Create a dataset with sentiment analysis test cases
	fmt.Println("=== Step 1: Creating dataset ===")
	datasetID, err := createSentimentDataset(ctx, apiClient)
	if err != nil {
		log.Fatalf("Failed to create dataset: %v", err)
	}
	fmt.Printf("✓ Created dataset: %s\n\n", datasetID)

	// Step 2: Create a task/prompt function for sentiment classification
	fmt.Println("=== Step 2: Creating task function ===")
	taskSlug := "sentiment-classifier-task"
	if err := createSentimentTask(ctx, apiClient, taskSlug); err != nil {
		log.Fatalf("Failed to create task: %v", err)
	}
	fmt.Printf("✓ Created task: %s\n\n", taskSlug)

	// Step 3: Create a scorer function to evaluate sentiment accuracy
	fmt.Println("=== Step 3: Creating scorer function ===")
	scorerSlug := "sentiment-accuracy-scorer"
	if err := createSentimentScorer(ctx, apiClient, scorerSlug); err != nil {
		log.Fatalf("Failed to create scorer: %v", err)
	}
	fmt.Printf("✓ Created scorer: %s\n\n", scorerSlug)

	// Step 4: Run evaluation using the Functions() API
	fmt.Println("=== Step 4: Running evaluation with Functions() API ===")
	evaluator := braintrust.NewEvaluator[SentimentInput, SentimentOutput](client)

	// Load dataset
	cases, err := evaluator.Datasets().Get(ctx, datasetID)
	if err != nil {
		log.Fatalf("Failed to load dataset: %v", err)
	}

	// Load task using Functions() API with FunctionOpts
	task, err := evaluator.Functions().Task(ctx, eval.FunctionOpts{
		Slug: taskSlug,
		// Project: "other-project",  // Optional: override project
		// Version: "abc123",          // Optional: pin to specific version
		// Environment: "production",  // Optional: specify environment
	})
	if err != nil {
		log.Fatalf("Failed to load task: %v", err)
	}
	fmt.Println("✓ Loaded task function")

	// Load scorer using Functions() API with FunctionOpts
	scorer, err := evaluator.Functions().Scorer(ctx, eval.FunctionOpts{
		Slug: scorerSlug,
	})
	if err != nil {
		log.Fatalf("Failed to load scorer: %v", err)
	}
	fmt.Println("✓ Loaded scorer function")

	// Run the evaluation
	result, err := evaluator.Run(ctx, eval.Opts[SentimentInput, SentimentOutput]{
		Experiment:  "sentiment-analysis-eval",
		Task:        task,
		Dataset:     cases,
		Scorers:     []eval.Scorer[SentimentInput, SentimentOutput]{scorer},
		Parallelism: 2,
	})
	if err != nil {
		log.Fatalf("Failed to run evaluation: %v", err)
	}

	fmt.Printf("\n✓ Evaluation complete!\n")
	fmt.Printf("  View results at: %s\n\n", result)

	// Step 5: Demonstrate using functions from different projects/environments
	fmt.Println("=== Step 5: Demo - Loading functions with different options ===")

	// Example 1: Load from a different project
	fmt.Println("Example: Loading task from different project")
	_, err = evaluator.Functions().Task(ctx, eval.FunctionOpts{
		Slug:    taskSlug,
		Project: "my-other-project",
	})
	if err != nil {
		fmt.Printf("  (Expected error - project doesn't exist): %v\n", err)
	}

	// Example 2: Pin to specific version
	fmt.Println("Example: Pinning to specific version")
	_, err = evaluator.Functions().Task(ctx, eval.FunctionOpts{
		Slug:    taskSlug,
		Version: "5878bd218351fb8e",
	})
	if err != nil {
		fmt.Printf("  (Expected error - version doesn't exist): %v\n", err)
	}

	// Example 3: Load from staging environment
	fmt.Println("Example: Loading from staging environment")
	_, err = evaluator.Functions().Task(ctx, eval.FunctionOpts{
		Slug:        taskSlug,
		Environment: "staging",
	})
	if err != nil {
		fmt.Printf("  (Expected error - environment not configured): %v\n\n", err)
	}

	// Step 6: Cleanup
	fmt.Println("=== Step 6: Cleaning up ===")
	if err := cleanup(ctx, apiClient, datasetID, taskSlug, scorerSlug); err != nil {
		fmt.Printf("⚠ Cleanup note: %v\n", err)
	} else {
		fmt.Println("✓ Cleanup complete")
	}
}

// createSentimentDataset creates a dataset with sentiment analysis test cases
func createSentimentDataset(ctx context.Context, apiClient *api.API) (string, error) {
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}

	dataset, err := apiClient.Datasets().Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        "sentiment-test-dataset",
		Description: "Test dataset for sentiment analysis with Functions() API example",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %w", err)
	}

	// Insert test cases with various sentiments
	events := []datasets.Event{
		{
			Input:    SentimentInput{Text: "I love this product! It's amazing!"},
			Expected: SentimentOutput{Sentiment: "positive"},
		},
		{
			Input:    SentimentInput{Text: "This is terrible. Very disappointed."},
			Expected: SentimentOutput{Sentiment: "negative"},
		},
		{
			Input:    SentimentInput{Text: "It's okay, nothing special."},
			Expected: SentimentOutput{Sentiment: "neutral"},
		},
		{
			Input:    SentimentInput{Text: "Absolutely wonderful experience!"},
			Expected: SentimentOutput{Sentiment: "positive"},
		},
		{
			Input:    SentimentInput{Text: "Worst purchase ever."},
			Expected: SentimentOutput{Sentiment: "negative"},
		},
	}

	if err := apiClient.Datasets().InsertEvents(ctx, dataset.ID, events); err != nil {
		return "", fmt.Errorf("failed to insert events: %w", err)
	}

	return dataset.ID, nil
}

// createSentimentTask creates a prompt function for sentiment classification
func createSentimentTask(ctx context.Context, apiClient *api.API, slug string) error {
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// Delete existing function if it exists
	functions := apiClient.Functions()
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: "go-sdk-examples",
		Slug:        slug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create sentiment classification prompt
	_, err = functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "Sentiment Classifier",
		Slug:         slug,
		FunctionType: "task",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are a sentiment analyzer. Classify the sentiment of the given text as 'positive', 'negative', or 'neutral'. Respond with valid JSON in the format: {\"sentiment\": \"<classification>\"}",
					},
					{
						"role":    "user",
						"content": "{{input.text}}",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature":     0,
					"max_tokens":      20,
					"response_format": map[string]any{"type": "json_object"},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	return nil
}

// createSentimentScorer creates a scorer function to evaluate sentiment accuracy
func createSentimentScorer(ctx context.Context, apiClient *api.API, slug string) error {
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// Delete existing function if it exists
	functions := apiClient.Functions()
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: "go-sdk-examples",
		Slug:        slug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create scorer prompt that checks if sentiment matches expected
	_, err = functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "Sentiment Accuracy Scorer",
		Slug:         slug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are an evaluator that checks if sentiment classifications are correct. Return a score of 1 if the output sentiment matches the expected sentiment, 0 otherwise. Respond with valid JSON in the format: {\"score\": <0 or 1>, \"name\": \"sentiment_match\"}",
					},
					{
						"role":    "user",
						"content": "Expected sentiment: {{expected.sentiment}}\nActual sentiment: {{output.sentiment}}",
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
		return fmt.Errorf("failed to create scorer: %w", err)
	}

	return nil
}

// cleanup removes test resources
func cleanup(ctx context.Context, apiClient *api.API, datasetID, taskSlug, scorerSlug string) error {
	// Delete dataset
	if err := apiClient.Datasets().Delete(ctx, datasetID); err != nil {
		return fmt.Errorf("dataset cleanup skipped (this is normal): %w", err)
	}

	// Delete task function
	functions := apiClient.Functions()
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: "go-sdk-examples",
		Slug:        taskSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Delete scorer function
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: "go-sdk-examples",
		Slug:        scorerSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	return nil
}
