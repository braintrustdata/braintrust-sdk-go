package eval

import bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"

// Eval is a reusable evaluation definition: the task, scorers, and project
// that together define what this eval does. An Eval can be run directly via
// [Evaluator.RunEval] or registered with a remote eval server.
//
// Example:
//
//	classify := &eval.Eval[string, string]{
//	    Name:        "classify",
//	    Task:        classifyTask,
//	    Scorers:     []eval.Scorer[string, string]{exactMatch},
//	    ProjectName: "my-project",
//	}
//
//	// Run locally
//	evaluator := braintrust.NewEvaluator[string, string](client)
//	result, err := evaluator.RunEval(ctx, classify, eval.RunOpts[string, string]{
//	    Dataset: myDataset,
//	})
type Eval[I, R any] struct {
	// Name is the eval name. Used as the default experiment name and as
	// the registration key when registered with a remote eval server.
	Name string

	// Task is the function under evaluation.
	Task TaskFunc[I, R]

	// Scorers are the scoring functions applied to each task result.
	Scorers []Scorer[I, R]

	// ProjectName is the Braintrust project for this eval.
	// Optional; falls back to the Evaluator's default project.
	ProjectName string
}

// RunOpts configures a single evaluation run. These vary per invocation;
// the [Eval] definition stays the same.
type RunOpts[I, R any] struct {
	// Experiment overrides the experiment name. Defaults to [Eval.Name].
	Experiment string

	// ProjectName overrides the project name. Defaults to [Eval.ProjectName].
	ProjectName string

	// Dataset is the test cases to evaluate against. Required.
	Dataset Dataset[I, R]

	// Tags to apply to the experiment.
	Tags []string

	// Metadata to attach to the experiment.
	Metadata Metadata

	// Update appends to an existing experiment when true (default: false).
	Update bool

	// Parallelism is the number of goroutines (default: 1).
	Parallelism int

	// Quiet suppresses result output (default: false).
	Quiet bool

	// OnCaseComplete is called after each case completes (task + scorers).
	// It is called from worker goroutines and must be safe for concurrent use.
	// Optional — nil means no callback.
	OnCaseComplete func(CaseProgress)

	// SpanParent overrides the parent attribute set on eval spans.
	// When zero, the default "experiment_id:<id>" parent is used.
	// The remote eval server sets this to link spans to a playground context.
	SpanParent bttrace.Parent

	// Generation is propagated from the parent context (e.g. a Braintrust playground
	// invocation) and injected into braintrust.span_attributes on every span.
	// The Braintrust backend uses it to link eval spans back to the triggering context.
	Generation any
}

// mergeOpts combines an Eval definition with RunOpts into an Opts for
// backward-compatible delegation to the existing run() function.
func mergeOpts[I, R any](ev *Eval[I, R], ro RunOpts[I, R]) Opts[I, R] {
	experiment := ro.Experiment
	if experiment == "" {
		experiment = ev.Name
	}

	projectName := ro.ProjectName
	if projectName == "" {
		projectName = ev.ProjectName
	}

	return Opts[I, R]{
		Experiment:     experiment,
		Dataset:        ro.Dataset,
		Task:           ev.Task,
		Scorers:        ev.Scorers,
		ProjectName:    projectName,
		Tags:           ro.Tags,
		Metadata:       ro.Metadata,
		Update:         ro.Update,
		Parallelism:    ro.Parallelism,
		Quiet:          ro.Quiet,
		OnCaseComplete: ro.OnCaseComplete,
		SpanParent:     ro.SpanParent,
		Generation:     ro.Generation,
	}
}
