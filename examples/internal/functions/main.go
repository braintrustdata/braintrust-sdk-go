package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/oapi-codegen/nullable"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
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
func createSentimentDataset(ctx context.Context, apiClient *openapi.ClientWithResponses) (string, error) {
	project, err := apiClient.PostProjectWithResponse(ctx, openapi.CreateProject{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		return "", fmt.Errorf("failed to create project: HTTP %d: %s", project.StatusCode(), project.Body)
	}

	dataset, err := apiClient.PostDatasetWithResponse(ctx, openapi.CreateDataset{
		ProjectId:   project.JSON200.Id,
		Name:        "sentiment-test-dataset",
		Description: nullable.NewNullableWithValue("Test dataset for sentiment analysis with Functions() API example"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %w", err)
	}
	if dataset.StatusCode() != http.StatusOK || dataset.JSON200 == nil {
		return "", fmt.Errorf("failed to create dataset: HTTP %d: %s", dataset.StatusCode(), dataset.Body)
	}

	// Insert test cases with various sentiments
	events := []openapi.InsertDatasetEvent{
		{
			Input:    nullable.NewNullableWithValue[any](SentimentInput{Text: "I love this product! It's amazing!"}),
			Expected: nullable.NewNullableWithValue[any](SentimentOutput{Sentiment: "positive"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](SentimentInput{Text: "This is terrible. Very disappointed."}),
			Expected: nullable.NewNullableWithValue[any](SentimentOutput{Sentiment: "negative"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](SentimentInput{Text: "It's okay, nothing special."}),
			Expected: nullable.NewNullableWithValue[any](SentimentOutput{Sentiment: "neutral"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](SentimentInput{Text: "Absolutely wonderful experience!"}),
			Expected: nullable.NewNullableWithValue[any](SentimentOutput{Sentiment: "positive"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](SentimentInput{Text: "Worst purchase ever."}),
			Expected: nullable.NewNullableWithValue[any](SentimentOutput{Sentiment: "negative"}),
		},
	}

	inserted, err := apiClient.PostDatasetIdInsertWithResponse(ctx, dataset.JSON200.Id.String(), openapi.InsertDatasetEventRequest{Events: events})
	if err != nil {
		return "", fmt.Errorf("failed to insert events: %w", err)
	}
	if inserted.StatusCode() != http.StatusOK || inserted.JSON200 == nil {
		return "", fmt.Errorf("failed to insert events: HTTP %d: %s", inserted.StatusCode(), inserted.Body)
	}

	return dataset.JSON200.Id.String(), nil
}

// createSentimentTask creates a prompt function for sentiment classification
func createSentimentTask(ctx context.Context, apiClient *openapi.ClientWithResponses, slug string) error {
	promptData, err := sentimentPromptData(
		"You are a sentiment analyzer. Classify the sentiment of the given text as 'positive', 'negative', or 'neutral'. Respond with valid JSON in the format: {\"sentiment\": \"<classification>\"}",
		"{{input.text}}", 20,
	)
	if err != nil {
		return err
	}
	return createSentimentPrompt(ctx, apiClient, openapi.CreateFunction{
		Name:         "Sentiment Classifier",
		Slug:         slug,
		FunctionType: nullable.NewNullableWithValue(openapi.FunctionTypeEnumNullishTask),
		PromptData:   nullable.NewNullableWithValue(promptData),
	})
}

// createSentimentScorer creates a scorer function to evaluate sentiment accuracy.
func createSentimentScorer(ctx context.Context, apiClient *openapi.ClientWithResponses, slug string) error {
	promptData, err := sentimentPromptData(
		"You are an evaluator that checks if sentiment classifications are correct. Return a score of 1 if the output sentiment matches the expected sentiment, 0 otherwise. Respond with valid JSON in the format: {\"score\": <0 or 1>, \"name\": \"sentiment_match\"}",
		"Expected sentiment: {{expected.sentiment}}\nActual sentiment: {{output.sentiment}}", 50,
	)
	if err != nil {
		return err
	}
	return createSentimentPrompt(ctx, apiClient, openapi.CreateFunction{
		Name:         "Sentiment Accuracy Scorer",
		Slug:         slug,
		FunctionType: nullable.NewNullableWithValue(openapi.FunctionTypeEnumNullishScorer),
		PromptData:   nullable.NewNullableWithValue(promptData),
	})
}

// sentimentPromptData builds the chat prompt shared by this example's task and scorer.
func sentimentPromptData(system, user string, maxTokens float32) (openapi.PromptDataNullish, error) {
	var systemContent openapi.ChatCompletionMessageParam_0_Content
	if err := systemContent.FromChatCompletionMessageParam0Content0(system); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	var userContent openapi.ChatCompletionMessageParam_1_Content
	if err := userContent.FromChatCompletionMessageParam1Content0(user); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	var systemMessage, userMessage openapi.ChatCompletionMessageParam
	if err := systemMessage.FromChatCompletionMessageParam0(openapi.ChatCompletionMessageParam0{
		Role: openapi.ChatCompletionMessageParam0RoleSystem, Content: &systemContent,
	}); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	if err := userMessage.FromChatCompletionMessageParam1(openapi.ChatCompletionMessageParam1{
		Role: openapi.ChatCompletionMessageParam1RoleUser, Content: &userContent,
	}); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	var prompt openapi.PromptBlockDataNullish
	if err := prompt.FromPromptBlockDataNullish0(openapi.PromptBlockDataNullish0{
		Type:     openapi.PromptBlockDataNullish0TypeChat,
		Messages: []openapi.ChatCompletionMessageParam{systemMessage, userMessage},
	}); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	var responseFormat openapi.ResponseFormatNullish
	if err := responseFormat.FromResponseFormatNullish0(openapi.ResponseFormatNullish0{
		Type: openapi.ResponseFormatNullish0TypeJsonObject,
	}); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	var params openapi.ModelParams
	temperature := float32(0)
	if err := params.FromModelParams0(openapi.ModelParams0{
		Temperature:    &temperature,
		MaxTokens:      &maxTokens,
		ResponseFormat: nullable.NewNullableWithValue(responseFormat),
	}); err != nil {
		return openapi.PromptDataNullish{}, err
	}
	model := "gpt-4o-mini"
	return openapi.PromptDataNullish{
		Prompt:  nullable.NewNullableWithValue(prompt),
		Options: nullable.NewNullableWithValue(openapi.PromptOptionsNullish{Model: &model, Params: &params}),
	}, nil
}

func createSentimentPrompt(ctx context.Context, apiClient *openapi.ClientWithResponses, body openapi.CreateFunction) error {
	project, err := apiClient.PostProjectWithResponse(ctx, openapi.CreateProject{Name: "go-sdk-examples"})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		return fmt.Errorf("failed to create project: HTTP %d: %s", project.StatusCode(), project.Body)
	}
	body.ProjectId = project.JSON200.Id
	if err := deleteSentimentFunction(ctx, apiClient, body.Slug); err != nil {
		return err
	}
	if err := body.FunctionData.FromFunctionData0(openapi.FunctionData0{Type: openapi.FunctionData0TypePrompt}); err != nil {
		return err
	}
	created, err := apiClient.PostFunctionWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", body.Name, err)
	}
	if created.StatusCode() != http.StatusOK || created.JSON200 == nil {
		return fmt.Errorf("failed to create %s: HTTP %d: %s", body.Name, created.StatusCode(), created.Body)
	}
	return nil
}

func deleteSentimentFunction(ctx context.Context, apiClient *openapi.ClientWithResponses, slug string) error {
	projectName, limit := "go-sdk-examples", 1
	existing, err := apiClient.GetFunctionWithResponse(ctx, &openapi.GetFunctionParams{
		ProjectName: &projectName,
		Slug:        &slug,
		Limit:       &limit,
	})
	if err != nil {
		return fmt.Errorf("failed to query %s: %w", slug, err)
	}
	if existing.StatusCode() != http.StatusOK || existing.JSON200 == nil {
		return fmt.Errorf("failed to query %s: HTTP %d: %s", slug, existing.StatusCode(), existing.Body)
	}
	if len(existing.JSON200.Objects) == 0 {
		return nil
	}
	deleted, err := apiClient.DeleteFunctionIdWithResponse(ctx, existing.JSON200.Objects[0].Id.String())
	if err != nil {
		return fmt.Errorf("failed to delete %s: %w", slug, err)
	}
	if deleted.StatusCode() != http.StatusOK || deleted.JSON200 == nil {
		return fmt.Errorf("failed to delete %s: HTTP %d: %s", slug, deleted.StatusCode(), deleted.Body)
	}
	return nil
}

// cleanup removes test resources.
func cleanup(ctx context.Context, apiClient *openapi.ClientWithResponses, datasetID, taskSlug, scorerSlug string) error {
	deleted, err := apiClient.DeleteDatasetIdWithResponse(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("dataset cleanup skipped: %w", err)
	}
	if deleted.StatusCode() != http.StatusOK || deleted.JSON200 == nil {
		return fmt.Errorf("dataset cleanup skipped: HTTP %d: %s", deleted.StatusCode(), deleted.Body)
	}
	if err := deleteSentimentFunction(ctx, apiClient, taskSlug); err != nil {
		return err
	}
	return deleteSentimentFunction(ctx, apiClient, scorerSlug)
}
