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

	// Configure the generated REST client independently of the tracing client.
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

	// Step 1: Create a prompt function for answering questions
	promptSlug := "qa-answer-prompt"
	if err := createPrompt(ctx, apiClient, promptSlug); err != nil {
		log.Fatalf("Failed to create prompt: %v", err)
	}

	// Step 2: Create a dataset with some test data
	datasetID, err := createDataset(ctx, apiClient)
	if err != nil {
		log.Fatalf("Failed to create dataset: %v", err)
	}

	// Step 3: Run an evaluation using the dataset and prompt
	evaluator := braintrust.NewEvaluator[QuestionInput, AnswerOutput](client)

	// Load dataset using the new DatasetAPI
	cases, err := evaluator.Datasets().Get(ctx, datasetID)
	if err != nil {
		log.Fatalf("Failed to load dataset: %v", err)
	}

	// Load the prompt as a task
	task, err := evaluator.Functions().Task(ctx, eval.FunctionOpts{Slug: promptSlug})
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
		Dataset:    cases,
		Task:       task,
		Scorers:    []eval.Scorer[QuestionInput, AnswerOutput]{exactMatchScorer},
	})
	if err != nil {
		log.Fatalf("Failed to run evaluation: %v", err)
	}

	fmt.Printf("Evaluation complete! View results at: %s\n", result)

	// Step 4: Cleanup - delete the test dataset
	deleted, err := apiClient.DeleteDatasetIdWithResponse(ctx, datasetID)
	if err != nil {
		// The dataset can be manually deleted from the Braintrust UI if needed.
		log.Printf("Note: Dataset cleanup skipped: %v", err)
	} else if deleted.StatusCode() != http.StatusOK || deleted.JSON200 == nil {
		log.Printf("Note: Dataset cleanup skipped: HTTP %d: %s", deleted.StatusCode(), deleted.Body)
	}
}

// createPrompt creates a prompt function for answering questions
func createPrompt(ctx context.Context, apiClient *openapi.ClientWithResponses, slug string) error {
	// First, get or create the project.
	project, err := apiClient.PostProjectWithResponse(ctx, openapi.CreateProject{Name: "go-sdk-examples"})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		return fmt.Errorf("failed to create project: HTTP %d: %s", project.StatusCode(), project.Body)
	}

	// Check if the prompt already exists and delete it.
	projectName, limit := "go-sdk-examples", 1
	existing, err := apiClient.GetFunctionWithResponse(ctx, &openapi.GetFunctionParams{
		ProjectName: &projectName,
		Slug:        &slug,
		Limit:       &limit,
	})
	if err != nil {
		return fmt.Errorf("failed to query prompt: %w", err)
	}
	if existing.StatusCode() != http.StatusOK || existing.JSON200 == nil {
		return fmt.Errorf("failed to query prompt: HTTP %d: %s", existing.StatusCode(), existing.Body)
	}
	if len(existing.JSON200.Objects) > 0 {
		deleted, err := apiClient.DeleteFunctionIdWithResponse(ctx, existing.JSON200.Objects[0].Id.String())
		if err != nil {
			return fmt.Errorf("failed to delete prompt: %w", err)
		}
		if deleted.StatusCode() != http.StatusOK || deleted.JSON200 == nil {
			return fmt.Errorf("failed to delete prompt: HTTP %d: %s", deleted.StatusCode(), deleted.Body)
		}
	}

	// Build the generated union models using their typed setters.
	var functionData openapi.FunctionData
	if err := functionData.FromFunctionData0(openapi.FunctionData0{Type: openapi.FunctionData0TypePrompt}); err != nil {
		return err
	}
	var systemContent openapi.ChatCompletionMessageParam_0_Content
	if err := systemContent.FromChatCompletionMessageParam0Content0("You are a helpful assistant that answers questions accurately and concisely. You must respond with valid JSON in the format: {\"answer\": \"your answer here\"}"); err != nil {
		return err
	}
	var userContent openapi.ChatCompletionMessageParam_1_Content
	if err := userContent.FromChatCompletionMessageParam1Content0("{{input.question}}"); err != nil {
		return err
	}
	var systemMessage, userMessage openapi.ChatCompletionMessageParam
	if err := systemMessage.FromChatCompletionMessageParam0(openapi.ChatCompletionMessageParam0{
		Role: openapi.ChatCompletionMessageParam0RoleSystem, Content: &systemContent,
	}); err != nil {
		return err
	}
	if err := userMessage.FromChatCompletionMessageParam1(openapi.ChatCompletionMessageParam1{
		Role: openapi.ChatCompletionMessageParam1RoleUser, Content: &userContent,
	}); err != nil {
		return err
	}
	var prompt openapi.PromptBlockDataNullish
	if err := prompt.FromPromptBlockDataNullish0(openapi.PromptBlockDataNullish0{
		Type:     openapi.PromptBlockDataNullish0TypeChat,
		Messages: []openapi.ChatCompletionMessageParam{systemMessage, userMessage},
	}); err != nil {
		return err
	}
	var responseFormat openapi.ResponseFormatNullish
	if err := responseFormat.FromResponseFormatNullish0(openapi.ResponseFormatNullish0{
		Type: openapi.ResponseFormatNullish0TypeJsonObject,
	}); err != nil {
		return err
	}
	var params openapi.ModelParams
	temperature, maxTokens := float32(0), float32(50)
	if err := params.FromModelParams0(openapi.ModelParams0{
		Temperature:    &temperature,
		MaxTokens:      &maxTokens,
		ResponseFormat: nullable.NewNullableWithValue(responseFormat),
	}); err != nil {
		return err
	}
	model := "gpt-4o-mini"
	created, err := apiClient.PostFunctionWithResponse(ctx, openapi.CreateFunction{
		ProjectId:    project.JSON200.Id,
		Name:         "QA Answer Prompt",
		Slug:         slug,
		FunctionData: functionData,
		PromptData: nullable.NewNullableWithValue(openapi.PromptDataNullish{
			Prompt:  nullable.NewNullableWithValue(prompt),
			Options: nullable.NewNullableWithValue(openapi.PromptOptionsNullish{Model: &model, Params: &params}),
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create prompt: %w", err)
	}
	if created.StatusCode() != http.StatusOK || created.JSON200 == nil {
		return fmt.Errorf("failed to create prompt: HTTP %d: %s", created.StatusCode(), created.Body)
	}
	return nil
}

// createDataset creates a test dataset and returns its ID
func createDataset(ctx context.Context, apiClient *openapi.ClientWithResponses) (string, error) {
	// First, get or create the project
	project, err := apiClient.PostProjectWithResponse(ctx, openapi.CreateProject{
		Name: "go-sdk-examples",
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		return "", fmt.Errorf("failed to create project: HTTP %d: %s", project.StatusCode(), project.Body)
	}

	// Create the dataset
	dataset, err := apiClient.PostDatasetWithResponse(ctx, openapi.CreateDataset{
		ProjectId:   project.JSON200.Id,
		Name:        "qa-test-dataset",
		Description: nullable.NewNullableWithValue("Test dataset for DatasetAPI example"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create dataset: %w", err)
	}
	if dataset.StatusCode() != http.StatusOK || dataset.JSON200 == nil {
		return "", fmt.Errorf("failed to create dataset: HTTP %d: %s", dataset.StatusCode(), dataset.Body)
	}

	// Insert test data
	events := []openapi.InsertDatasetEvent{
		{
			Input:    nullable.NewNullableWithValue[any](map[string]any{"question": "What is 2 + 2?"}),
			Expected: nullable.NewNullableWithValue[any](map[string]any{"answer": "4"}),
			Tags:     nullable.NewNullableWithValue([]string{"math", "easy"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](map[string]any{"question": "What is the capital of France?"}),
			Expected: nullable.NewNullableWithValue[any](map[string]any{"answer": "Paris"}),
			Tags:     nullable.NewNullableWithValue([]string{"geography", "easy"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](map[string]any{"question": "What is the square root of 144?"}),
			Expected: nullable.NewNullableWithValue[any](map[string]any{"answer": "12"}),
			Tags:     nullable.NewNullableWithValue([]string{"math", "medium"}),
		},
		{
			Input:    nullable.NewNullableWithValue[any](map[string]any{"question": "Who wrote Romeo and Juliet?"}),
			Expected: nullable.NewNullableWithValue[any](map[string]any{"answer": "William Shakespeare"}),
			Tags:     nullable.NewNullableWithValue([]string{"literature", "easy"}),
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
