package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

// summarizer builds an eval driven by a prompt parameter.
//
// The "summary_prompt" parameter becomes a prompt picker in the playground.
// Whatever prompt is chosen there arrives as a *prompt.Prompt, gets rendered
// with the case input, and drives a real OpenAI call. Editing the prompt in the
// playground and re-running changes what the model is asked, with no code
// change.
//
// The default declared in main.go is what runs when nothing is chosen -- from
// `bt eval` on the command line, for instance.
func summarizer() *eval.Eval[string, string] {
	summarize := eval.TaskWithHooks(func(ctx context.Context, input string, hooks *eval.TaskHooks) (string, error) {
		p, ok := hooks.Parameters.Prompt("summary_prompt")
		if !ok {
			return "", errors.New("summary_prompt is not a prompt")
		}

		// Render the template. The variables here are what {{...}} placeholders
		// in the prompt can refer to.
		built, err := p.Build(map[string]any{"input": input})
		if err != nil {
			return "", fmt.Errorf("building prompt: %w", err)
		}

		// Records which prompt (and which variables) produced this call, so the
		// trace in Braintrust links back to the prompt.
		built.AnnotateSpan(hooks.TaskSpan)

		params, err := traceopenai.ChatCompletionParams(built)
		if err != nil {
			return "", err
		}

		resp, err := llm.Chat.Completions.New(ctx, params)
		if err != nil {
			return "", fmt.Errorf("calling OpenAI: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("OpenAI returned no choices")
		}

		return strings.TrimSpace(resp.Choices[0].Message.Content), nil
	})

	// Scoring a summary properly needs a model; these two are deliberately
	// simple so the example stays about prompts.
	nonEmpty := eval.NewScorer("non_empty",
		func(_ context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			if strings.TrimSpace(r.Output) == "" {
				return eval.S(0.0), nil
			}
			return eval.S(1.0), nil
		},
	)

	oneSentence := eval.NewScorer("one_sentence",
		func(_ context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
			if strings.Count(r.Output, ".") > 1 {
				return eval.S(0.0), nil
			}
			return eval.S(1.0), nil
		},
	)

	return &eval.Eval[string, string]{
		Name: "summarizer",
		Task: summarize,

		// The "summary_prompt" parameter becomes a prompt picker in the
		// playground. Whatever prompt is selected there reaches the task via
		// hooks.Parameters, falling back to the default declared here -- which
		// is written in Go but reaches Braintrust as ordinary prompt data.
		ParameterSchema: eval.ParameterSchema{
			"summary_prompt": {
				Type:        eval.ParameterTypePrompt,
				Description: "Prompt used to summarize each input",
				Default: prompt.Definition{
					Model: "gpt-4o-mini",
					Messages: []prompt.Message{
						prompt.System("You summarize text in exactly one short sentence."),
						prompt.User("Summarize this:\n\n{{input}}"),
					},
					Params: map[string]any{"temperature": 0, "max_tokens": 100},
				},
			},
		},

		Scorers:     []eval.Scorer[string, string]{nonEmpty, oneSentence},
		ProjectName: "go-sdk-examples",
		Dataset: eval.NewDataset([]eval.Case[string, string]{
			{Input: "The city council voted 7-2 to fund a new bike lane network downtown, " +
				"with construction starting in the spring."},
			{Input: "Researchers found that the fungus spreads through root systems, " +
				"which explains why entire groves die within a single season."},
		}),
	}
}

// llm is the traced OpenAI client shared by every case. The middleware puts each
// model call in the same trace as the eval's task span.
var llm = openai.NewClient(
	option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	option.WithMiddleware(traceopenai.NewMiddleware()),
)
