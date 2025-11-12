package eval

import (
	"context"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
)

// Evaluator provides a reusable way to run multiple evaluations with the same
// input and output types. This is useful when you need to run several evaluations
// in sequence with the same type signature, or use hosted prompts, scorers and datasets
// with automatic type conversion.
type Evaluator[I, R any] struct {
	session            *auth.Session
	defaultProjectName string
	tracerProvider     *trace.TracerProvider
	api                *api.API
}

// NewEvaluator creates a new evaluator with explicit dependencies.
// The type parameters I (input) and R (result/output) must be specified explicitly.
// Users create Evaluators with braintrust.NewEvaluator.
func NewEvaluator[I, R any](s *auth.Session, tp *trace.TracerProvider, api *api.API, project string) *Evaluator[I, R] {
	return &Evaluator[I, R]{
		session:            s,
		defaultProjectName: project,
		tracerProvider:     tp,
		api:                api,
	}
}

// Functions is used to execute hosted Braintrust functions (e.g. hosted tasks and hosted scorers) as part of an eval. As
// long as I and R are JSON-serializable, FunctionsAPI will automatically convert the input and output to and from JSON.
func (e *Evaluator[I, R]) Functions() *FunctionsAPI[I, R] {
	return &FunctionsAPI[I, R]{
		api:         e.api,
		projectName: e.defaultProjectName,
	}
}

// Datasets is used to access Datasets API for loading datasets with this evaluator's type parameters.
func (e *Evaluator[I, R]) Datasets() *DatasetAPI[I, R] {
	return &DatasetAPI[I, R]{
		api: e.api,
	}
}

// Run executes an evaluation using this evaluator's dependencies.
func (e *Evaluator[I, R]) Run(ctx context.Context, opts Opts[I, R]) (*Result, error) {
	return run(ctx, opts, e.session, e.tracerProvider, e.api, e.defaultProjectName)
}
