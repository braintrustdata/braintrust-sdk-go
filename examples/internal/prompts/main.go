// This example shows how to use a Braintrust prompt from Go: load it by slug,
// render it with variables, and send it to an LLM you call yourself.
//
// This is the local counterpart to examples/prompts, which asks Braintrust to
// invoke a hosted prompt server-side. Here the prompt is data: your code
// renders it and owns the model call, so it appears in your traces like any
// other request.
//
// To run this example:
//  1. Set BRAINTRUST_API_KEY and OPENAI_API_KEY
//  2. go run ./internal/prompts
//
// The prompt it loads is created on first run, so there is nothing to set up in
// the UI beforehand.
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
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

const (
	projectName = "go-sdk-examples"
	promptSlug  = "sdk-go-local-greeter"
)

func main() {
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Println("Skipping: OPENAI_API_KEY is not set.")
		return
	}

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

	// Make sure the prompt this example loads exists. Normally you would author
	// it in the Braintrust UI instead.
	if err := ensurePrompt(ctx, bt); err != nil {
		log.Fatalf("creating the prompt: %v", err)
	}

	// Load it. The returned prompt carries its template, model and parameters,
	// plus the identity that links traces back to it.
	greeter, err := bt.LoadPrompt(ctx, prompt.LoadOpts{Slug: promptSlug})
	if err != nil {
		log.Fatalf("loading the prompt: %v", err)
	}
	fmt.Printf("Loaded prompt %q (model %s, version %s)\n\n", greeter.Slug, greeter.Model(), greeter.Version)

	client := openai.NewClient(
		option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
		option.WithMiddleware(traceopenai.NewMiddleware()),
	)

	tracer := tp.Tracer("prompts-example")

	for _, name := range []string{"Joe", "Jane"} {
		ctx, span := tracer.Start(ctx, "greet")

		// Render the template. Placeholders like {{name}} are filled from here.
		built, err := greeter.Build(map[string]any{"name": name})
		if err != nil {
			log.Fatalf("building the prompt: %v", err)
		}

		// Record which prompt produced this call, so the trace links back to it.
		built.AnnotateSpan(span)

		// Convert to an OpenAI request. Built is provider-agnostic; use
		// built.Map() for a client with no adapter.
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
			fmt.Printf("View trace: %s\n\n", link)
		}
		span.End()
	}
}

// ensurePrompt creates the prompt this example loads, so the example runs
// without any setup in the UI. Creating a prompt with an existing slug replaces
// it, so running twice is fine.
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
