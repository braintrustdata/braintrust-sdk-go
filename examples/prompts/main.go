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
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
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

	// Both halves use this one prompt. Normally you would author it in the
	// Braintrust UI; here we create it so the example is self-contained.
	if err := ensurePrompt(ctx, bt); err != nil {
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
func ensurePrompt(ctx context.Context, bt *braintrust.Client) error {
	api := bt.API()

	project, err := api.Projects().Create(ctx, projects.CreateParams{Name: projectName})
	if err != nil {
		return err
	}

	_, err = api.Functions().Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         promptSlug,
		Slug:         promptSlug,
		FunctionData: map[string]any{"type": "prompt"},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "system", "content": "You greet people in one short, warm sentence."},
					{"role": "user", "content": "Say hello to {{name}}."},
				},
			},
			"options": map[string]any{
				"model":  "gpt-4o-mini",
				"params": map[string]any{"temperature": 0, "max_tokens": 50},
			},
			"template_format": "mustache",
		},
	})
	return err
}
