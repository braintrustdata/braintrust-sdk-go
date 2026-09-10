// This example demonstrates running Braintrust evals under the bt CLI, so the
// Braintrust playground can trigger them against code on your own machine.
//
// Drive it from the playground:
//
//	bt eval --dev ./internal/eval-runner
//
// Run every eval from the command line:
//
//	bt eval ./internal/eval-runner
//
// Or just see what is registered, without contacting Braintrust:
//
//	go run ./internal/eval-runner
package main

import (
	"github.com/braintrustdata/braintrust-sdk-go/evalrunner"
)

func main() {
	// Create the runner. Use WithTracerProvider(tp) to include user-instrumented
	// spans (LLM clients, custom spans) in eval traces.
	r := evalrunner.New()

	// Register the eval. Add one RegisterEval call per eval; the runner looks
	// them up by name when bt asks for one. Everything else about the eval --
	// including its parameters -- is declared on the eval itself.
	evalrunner.RegisterEval(r, foodClassifier())

	// The second eval is driven by a prompt: its "summary_prompt" parameter
	// becomes a prompt picker in the playground, and whatever is selected there
	// is rendered by the task and sent to OpenAI. See summarizer_eval.go.
	evalrunner.RegisterEval(r, summarizer())

	// Main reads the environment bt set, dispatches, and exits. It never returns.
	evalrunner.Main(r)
}
