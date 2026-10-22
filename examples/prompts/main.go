// This example shows the two ways to use a Braintrust prompt from Go, against
// the same prompt:
//
//  1. Render it locally: load the prompt, fill in its variables, and call the
//     model yourself. The prompt is data, and your code owns the request.
//  2. Invoke it server-side: hand Braintrust a slug and an input and let it run
//     the prompt against the model for you.
//
// To run this example:
//  1. Set BRAINTRUST_API_KEY (and OPENAI_API_KEY for the local half)
//  2. go run ./prompts
//
// The prompt is created on first run, so there is nothing to set up in the UI
// beforehand.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/oapi-codegen/nullable"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

const (
	projectName = "go-sdk-examples"
	promptSlug  = "sdk-go-greeter"
)

func main() {
	ctx := context.Background()

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(ctx) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject(projectName),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("initializing Braintrust: %v", err)
	}

	key := os.Getenv("BRAINTRUST_API_KEY")
	baseURL := os.Getenv("BRAINTRUST_API_URL")
	if baseURL == "" {
		baseURL = "https://api.braintrust.dev"
	}
	api, err := openapi.NewClientWithResponses(baseURL, openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	}))
	if err != nil {
		log.Fatalf("initializing REST client: %v", err)
	}

	// Both halves use this one prompt. Normally you would author it in the
	// Braintrust UI; here we create it so the example is self-contained.
	if err := ensurePrompt(ctx, api); err != nil {
		log.Fatalf("creating the prompt: %v", err)
	}

	renderLocally(ctx, bt, tp)
	invokeHosted(ctx, bt)
}

// renderLocally loads the prompt, renders it in-process, and calls OpenAI
// directly. The rendered call shows up in your own traces.
func renderLocally(ctx context.Context, bt *braintrust.Client, tp *trace.TracerProvider) {
	fmt.Println("== Rendered locally ==")

	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Println("Skipping: OPENAI_API_KEY is not set.")
		fmt.Println()
		return
	}

	// The loaded prompt carries its template, model and parameters, plus the
	// identity that links traces back to it.
	greeter, err := bt.LoadPrompt(ctx, prompt.LoadOpts{Slug: promptSlug})
	if err != nil {
		log.Fatalf("loading the prompt: %v", err)
	}

	client := openai.NewClient(
		option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
		option.WithMiddleware(traceopenai.NewMiddleware()),
	)
	tracer := tp.Tracer("prompts-example")

	for _, name := range []string{"Joe", "Jane"} {
		ctx, span := tracer.Start(ctx, "greet")

		// Fill in the {{name}} placeholder.
		built, err := greeter.Build(map[string]any{"name": name})
		if err != nil {
			log.Fatalf("building the prompt: %v", err)
		}

		// Records which prompt produced this call, so the trace links back to it.
		built.AnnotateSpan(span)

		// Built is provider-agnostic; use built.Map() for a client with no adapter.
		params, err := traceopenai.ChatCompletionParams(built)
		if err != nil {
			log.Fatalf("converting the prompt: %v", err)
		}

		resp, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			log.Fatalf("calling OpenAI: %v", err)
		}

		fmt.Printf("%s -> %s\n", name, resp.Choices[0].Message.Content)
		if link := bt.Permalink(span); link != "" {
			fmt.Printf("  view trace: %s\n", link)
		}
		span.End()
	}
	fmt.Println()
}

// invokeHosted runs the same prompt server-side: Braintrust renders it and
// calls the model, and the run is recorded as an experiment. No local model
// client or key is needed.
func invokeHosted(ctx context.Context, bt *braintrust.Client) {
	fmt.Println("== Invoked server-side ==")

	evaluator := braintrust.NewEvaluator[string, string](bt)

	task, err := evaluator.Functions().Task(ctx, eval.FunctionOpts{Slug: promptSlug})
	if err != nil {
		log.Fatalf("loading the hosted prompt: %v", err)
	}

	result, err := evaluator.Run(ctx, eval.Opts[string, string]{
		Experiment: "greeter-hosted",
		Task:       task,
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "Joe"},
			{Input: "Jane"},
		}),
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("non_empty", func(_ context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
				if r.Output == "" {
					return eval.S(0.0), nil
				}
				return eval.S(1.0), nil
			}),
		},
	})
	if err != nil {
		log.Fatalf("running the hosted prompt: %v", err)
	}

	fmt.Printf("Ran hosted prompt as experiment %q\n", result.Name())
	if link, err := result.Permalink(); err == nil && link != "" {
		fmt.Printf("View experiment: %s\n", link)
	}
}

// ensurePrompt creates the prompt both halves use. Creating a prompt with an
// existing slug replaces it, so running twice is fine.
func ensurePrompt(ctx context.Context, api *openapi.ClientWithResponses) error {
	project, err := api.PostProjectWithResponse(ctx, openapi.CreateProject{Name: projectName})
	if err != nil {
		return err
	}
	if project.StatusCode() != http.StatusOK || project.JSON200 == nil {
		return fmt.Errorf("creating project: HTTP %d: %s", project.StatusCode(), project.Body)
	}

	var functionData openapi.FunctionData
	if err := functionData.FromFunctionData0(openapi.FunctionData0{Type: "prompt"}); err != nil {
		return err
	}
	var systemContent openapi.ChatCompletionMessageParam_0_Content
	if err := systemContent.FromChatCompletionMessageParam0Content0("You greet people in one short, warm sentence."); err != nil {
		return err
	}
	var userContent openapi.ChatCompletionMessageParam_1_Content
	if err := userContent.FromChatCompletionMessageParam1Content0("Say hello to {{name}}."); err != nil {
		return err
	}
	var system, user openapi.ChatCompletionMessageParam
	if err := system.FromChatCompletionMessageParam0(openapi.ChatCompletionMessageParam0{Role: "system", Content: &systemContent}); err != nil {
		return err
	}
	if err := user.FromChatCompletionMessageParam1(openapi.ChatCompletionMessageParam1{Role: "user", Content: &userContent}); err != nil {
		return err
	}
	var block openapi.PromptBlockDataNullish
	if err := block.FromPromptBlockDataNullish0(openapi.PromptBlockDataNullish0{
		Type: "chat", Messages: []openapi.ChatCompletionMessageParam{system, user},
	}); err != nil {
		return err
	}
	model := "gpt-4o-mini"
	temperature, maxTokens := float32(0), float32(50)
	var params openapi.ModelParams
	if err := params.FromModelParams0(openapi.ModelParams0{Temperature: &temperature, MaxTokens: &maxTokens}); err != nil {
		return err
	}
	response, err := api.PostFunctionWithResponse(ctx, openapi.CreateFunction{
		ProjectId:    project.JSON200.Id,
		Name:         promptSlug,
		Slug:         promptSlug,
		FunctionData: functionData,
		PromptData: nullable.NewNullableWithValue(openapi.PromptDataNullish{
			Prompt:         nullable.NewNullableWithValue(block),
			Options:        nullable.NewNullableWithValue(openapi.PromptOptionsNullish{Model: &model, Params: &params}),
			TemplateFormat: nullable.NewNullableWithValue(openapi.PromptDataNullishTemplateFormat("mustache")),
		}),
	})
	if err != nil {
		return err
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return fmt.Errorf("creating prompt: HTTP %d: %s", response.StatusCode(), response.Body)
	}
	return nil
}
