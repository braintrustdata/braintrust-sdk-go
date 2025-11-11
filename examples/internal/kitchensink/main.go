// Package main demonstrates a comprehensive "kitchen sink" example that combines
// as much Braintrust SDK functionality as possible into a single executable.
//
// This example includes:
// - Tracing: OpenAI and Anthropic integrations with middleware
// - Attachments: Images from file, bytes, and URL
// - Evaluations:
//  1. Hardcoded dataset with local task/scorers
//  2. API-created dataset with hosted task/scorer (demonstrates function.invoke spans)
//
// To run this example:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	export OPENAI_API_KEY="your-openai-key"
//	export ANTHROPIC_API_KEY="your-anthropic-key"
//	go run main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/trace/attachment"
	traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {
	fmt.Println("=== Braintrust Kitchen Sink Example ===")

	// Validate required environment variables
	if err := validateEnv(); err != nil {
		log.Fatal(err)
	}

	// Initialize TracerProvider and Braintrust client
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

	// Get or create project
	project, err := getOrCreateProject(bt, "go-sdk-examples")
	if err != nil {
		log.Fatalf("Failed to get/create project: %v", err)
	}
	fmt.Printf("📁 Using project: %s (ID: %s)\n\n", project.Name, project.ID)

	// Create tracer and main span
	tracer := otel.Tracer("kitchensink-example")
	ctx, mainSpan := tracer.Start(context.Background(), "examples/internal/kitchensink/main.go")
	defer mainSpan.End()

	// === TRACING SECTION ===
	fmt.Println("🔍 Running tracing demonstrations...")
	runTracingDemos(ctx, tracer, tp)
	fmt.Println()

	// Force flush to ensure all traces are sent
	if err := tp.ForceFlush(context.Background()); err != nil {
		log.Printf("Warning: Failed to flush tracer provider: %v", err)
	}

	// Print main span permalink
	fmt.Printf("🔗 View traces: %s\n\n", bt.Permalink(mainSpan))

	// === EVAL SECTION ===
	fmt.Println("📊 Running evaluation demonstrations...")
	runEvalDemos(bt, project.ID)
	fmt.Println()

	// Force flush after evals to capture function.invoke spans
	if err := tp.ForceFlush(context.Background()); err != nil {
		log.Printf("Warning: Failed to flush tracer provider after evals: %v", err)
	}

	fmt.Println("✓ Kitchen sink example completed successfully!")
}

func validateEnv() error {
	required := []string{"BRAINTRUST_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
	var missing []string
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %v", missing)
	}
	return nil
}

func getOrCreateProject(bt *braintrust.Client, name string) (*projects.Project, error) {
	// Try to create the project; if it already exists, API will return existing one or error
	project, err := bt.API().Projects().Create(context.Background(), projects.CreateParams{
		Name: name,
	})
	if err != nil {
		// If creation failed, it might already exist
		// For simplicity in this example, we'll just return the error
		// In production, you'd want to list projects and find by name
		return nil, fmt.Errorf("failed to create project: %v", err)
	}
	return project, nil
}

func runTracingDemos(ctx context.Context, tracer oteltrace.Tracer, tp *trace.TracerProvider) {
	// 1. OpenAI integration
	fmt.Println("  → OpenAI chat completion")
	runOpenAIDemo(ctx, tracer)

	// 2. Anthropic integration
	fmt.Println("  → Anthropic message creation")
	runAnthropicDemo(ctx, tracer)

	// 3. Manual spans with attachments
	fmt.Println("  → Manual spans with attachments")
	runAttachmentDemos(ctx, tracer)
}

func runOpenAIDemo(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "llm.chat.openai")
	defer span.End()

	client := openai.NewClient(
		option.WithMiddleware(traceopenai.NewMiddleware()),
	)

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say 'Hello from OpenAI!' in exactly those words."),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		log.Printf("OpenAI error: %v", err)
		return
	}

	fmt.Printf("     OpenAI: %s\n", resp.Choices[0].Message.Content)
}

func runAnthropicDemo(ctx context.Context, tracer oteltrace.Tracer) {
	_, span := tracer.Start(ctx, "llm.chat.anthropic")
	defer span.End()

	client := anthropic.NewClient(
		anthropicoption.WithMiddleware(traceanthropic.NewMiddleware()),
	)

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.ModelClaude3_7SonnetLatest,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say 'Hello from Anthropic!' in exactly those words.")),
		},
		MaxTokens: 1024,
	})
	if err != nil {
		log.Printf("Anthropic error: %v", err)
		return
	}

	fmt.Printf("     Anthropic: %s\n", message.Content[0].Text)
}

func runAttachmentDemos(ctx context.Context, tracer oteltrace.Tracer) {
	// 1. Attachment from bytes
	func() {
		_, span := tracer.Start(ctx, "attachment.from_bytes")
		defer span.End()

		imageBytes := getTestImageBytes()
		att := attachment.FromBytes(attachment.ImagePNG, imageBytes)

		// Log attachment in a manual span
		attMsg, err := att.Base64Message()
		if err != nil {
			log.Printf("Failed to create attachment message: %v", err)
			return
		}

		messages := []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Test image from bytes"},
					attMsg,
				},
			},
		}
		messagesJSON, _ := json.Marshal(messages)
		span.SetAttributes(attribute.String("braintrust.input_json", string(messagesJSON)))
		span.SetAttributes(attribute.String("braintrust.output_json", `{"response": "Image received"}`))

		fmt.Printf("     Created attachment from bytes (%d bytes)\n", len(imageBytes))
	}()

	// 2. Attachment from file
	func() {
		_, span := tracer.Start(ctx, "attachment.from_file")
		defer span.End()

		tmpFile, err := createTestImage()
		if err != nil {
			log.Printf("Failed to create test image: %v", err)
			return
		}
		defer func() {
			_ = os.Remove(tmpFile)
		}()

		att, err := attachment.FromFile(attachment.ImagePNG, tmpFile)
		if err != nil {
			log.Printf("Failed to create attachment from file: %v", err)
			return
		}

		attMsg, err := att.Base64Message()
		if err != nil {
			log.Printf("Failed to create attachment message: %v", err)
			return
		}

		messages := []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Test image from file"},
					attMsg,
				},
			},
		}
		messagesJSON, _ := json.Marshal(messages)
		span.SetAttributes(attribute.String("braintrust.input_json", string(messagesJSON)))
		span.SetAttributes(attribute.String("braintrust.output_json", `{"response": "Image received"}`))

		fmt.Printf("     Created attachment from file\n")
	}()

	// 3. Attachment from URL
	func() {
		_, span := tracer.Start(ctx, "attachment.from_url")
		defer span.End()

		url := "https://avatars.githubusercontent.com/u/109710255?s=200&v=4"
		att, err := attachment.FromURL(url)
		if err != nil {
			log.Printf("Failed to fetch URL: %v", err)
			return
		}

		attMsg, err := att.Base64Message()
		if err != nil {
			log.Printf("Failed to create attachment message: %v", err)
			return
		}

		messages := []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Test image from URL"},
					attMsg,
				},
			},
		}
		messagesJSON, _ := json.Marshal(messages)
		span.SetAttributes(attribute.String("braintrust.input_json", string(messagesJSON)))
		span.SetAttributes(attribute.String("braintrust.output_json", `{"response": "Image received"}`))

		fmt.Printf("     Created attachment from URL\n")
	}()
}

func runEvalDemos(bt *braintrust.Client, projectID string) {
	// 1. Eval with hardcoded dataset and local task/scorers
	fmt.Println("  → Eval with hardcoded dataset and local task/scorers")
	runHardcodedDatasetEval(bt)

	// 2. Eval with API dataset and hosted task/scorer
	fmt.Println("  → Eval with API dataset and hosted task/scorer")
	runAPIDatasetWithHostedFunctions(bt, projectID)
}

func runHardcodedDatasetEval(bt *braintrust.Client) {
	evaluator := braintrust.NewEvaluator[string, string](bt)

	task := func(ctx context.Context, input string) (string, error) {
		// Simple task: return first word capitalized
		words := strings.Fields(input)
		if len(words) == 0 {
			return "", nil
		}
		return strings.ToUpper(words[0][:1]) + strings.ToLower(words[0][1:]), nil
	}

	_, err := evaluator.Run(context.Background(), eval.Opts[string, string]{
		Experiment: "kitchensink-hardcoded-eval",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "hello", Expected: "Hello"},
			{Input: "world", Expected: "World"},
			{Input: "braintrust", Expected: "Braintrust"},
		}),
		Task: eval.T(task),
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("exact_match", func(_ context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
				score := 0.0
				if taskResult.Output == taskResult.Expected {
					score = 1.0
				}
				return eval.S(score), nil
			}),
		},
	})
	if err != nil {
		log.Printf("Hardcoded eval error: %v", err)
	}
}

func runAPIDatasetWithHostedFunctions(bt *braintrust.Client, projectID string) {
	ctx := context.Background()
	evaluator := braintrust.NewEvaluator[string, string](bt)

	// 1. Create API dataset
	datasetID, err := getOrCreateGreeterDataset(bt, projectID)
	if err != nil {
		log.Fatalf("Failed to create dataset: %v", err)
	}

	// 2. Fetch dataset using evaluator.Datasets()
	cases, err := evaluator.Datasets().Get(ctx, datasetID)
	if err != nil {
		log.Fatalf("Failed to fetch dataset: %v", err)
	}

	// 3. Create hosted task prompt using bt.API().Functions()
	taskSlug := "sdk-greeter-prompt-195e"
	if err := createOrGetPrompt(bt, projectID, taskSlug); err != nil {
		log.Fatalf("Failed to create/get prompt: %v", err)
	}

	// 4. Load the hosted task using evaluator.Functions()
	task, err := evaluator.Functions().Task(ctx, eval.FunctionOpts{
		Slug: taskSlug,
	})
	if err != nil {
		log.Fatalf("Failed to load hosted task: %v", err)
	}

	// 5. Create hosted scorer using bt.API().Functions()
	scorerSlug := "fail-scorer-d879"
	if err := createOrGetScorer(bt, projectID, scorerSlug); err != nil {
		log.Fatalf("Failed to create/get scorer: %v", err)
	}

	// 6. Load the hosted scorer using evaluator.Functions()
	scorer, err := evaluator.Functions().Scorer(ctx, eval.FunctionOpts{
		Slug: scorerSlug,
	})
	if err != nil {
		log.Fatalf("Failed to load hosted scorer: %v", err)
	}

	// 7. Run eval with API dataset, hosted task, and hosted scorer
	_, err = evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "kitchensink-api-hosted-eval",
		Dataset:    cases,
		Task:       task,
		Scorers:    []eval.Scorer[string, string]{scorer},
	})
	if err != nil {
		log.Fatalf("API dataset with hosted functions eval failed: %v", err)
	}
}

func getOrCreateGreeterDataset(bt *braintrust.Client, projectID string) (string, error) {
	// Create a new dataset with timestamp for greeter examples
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	datasetInfo, err := bt.API().Datasets().Create(context.Background(), datasets.CreateParams{
		ProjectID:   projectID,
		Name:        "kitchensink-greeter-dataset-" + timestamp,
		Description: "Kitchen sink greeter dataset for hosted function testing",
	})
	if err != nil {
		return "", err
	}

	// Insert greeter test cases
	events := []datasets.Event{
		{
			Input:    "Alice",
			Expected: "Hello Alice",
		},
		{
			Input:    "Bob",
			Expected: "Hi Bob",
		},
		{
			Input:    "Charlie",
			Expected: "Hey Charlie",
		},
	}

	err = bt.API().Datasets().Insert(context.Background(), datasetInfo.ID, datasets.InsertParams{Events: events})
	if err != nil {
		return "", err
	}

	return datasetInfo.ID, nil
}

func createOrGetPrompt(bt *braintrust.Client, projectID, slug string) error {
	ctx := context.Background()

	// Check if prompt already exists
	existing, err := bt.API().Functions().Query(ctx, functionsapi.QueryParams{
		ProjectID: projectID,
		Slug:      slug,
		Limit:     1,
	})
	if err == nil && len(existing) > 0 {
		// Prompt already exists
		return nil
	}

	// Create the prompt
	_, err = bt.API().Functions().Create(ctx, functionsapi.CreateParams{
		ProjectID: projectID,
		Name:      "SDK Greeter Prompt",
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
						"content": "You are a friendly greeter. Respond with a greeting that includes the person's name.",
					},
					{
						"role":    "user",
						"content": "Greet {{input}}",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature": 0,
					"max_tokens":  50,
				},
			},
		},
		Description: "A simple greeter prompt for testing",
	})

	return err
}

func createOrGetScorer(bt *braintrust.Client, projectID, slug string) error {
	ctx := context.Background()

	// Check if scorer already exists
	existing, err := bt.API().Functions().Query(ctx, functionsapi.QueryParams{
		ProjectID: projectID,
		Slug:      slug,
		Limit:     1,
	})
	if err == nil && len(existing) > 0 {
		// Scorer already exists
		return nil
	}

	// Create the scorer (prompt-based scorer)
	_, err = bt.API().Functions().Create(ctx, functionsapi.CreateParams{
		ProjectID:    projectID,
		Name:         "Exact Match Scorer",
		Slug:         slug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"parser": map[string]any{
				"type":          "llm_classifier",
				"use_cot":       false,
				"choice_scores": map[string]any{"fail": 0.0, "pass": 1.0},
			},
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are a scorer that evaluates if the output matches the expected value.",
					},
					{
						"role":    "user",
						"content": "Output: {{output}}\nExpected: {{expected}}\n\nChoose 'pass' if they match exactly, 'fail' otherwise.",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature": 0,
					"max_tokens":  10,
				},
			},
		},
		Description: "A simple exact match scorer",
	})

	return err
}

// Helper functions for test images

func createTestImage() (string, error) {
	tmpFile, err := os.CreateTemp("", "test-image-*.png")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	_, err = tmpFile.Write(getTestImageBytes())
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

func getTestImageBytes() []byte {
	// 10x10 red square PNG
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x0a, 0x00, 0x00, 0x00, 0x0a,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x02, 0x50, 0x58, 0xea, 0x00, 0x00, 0x00,
		0x12, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xf8, 0xcf, 0xc0, 0x80,
		0x07, 0x31, 0x8c, 0x4a, 0x63, 0x43, 0x00, 0xb7, 0xca, 0x63, 0x9d, 0xd6,
		0xd5, 0xef, 0x74, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
