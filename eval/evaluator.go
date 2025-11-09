package eval

import (
	"context"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
)

// Evaluator provides a reusable way to run multiple evaluations with the same
// input and output types. This is useful when you need to run several evaluations
// in sequence with the same type signature, or use hosted prompts, scorers and datasets
// with automatic type conversion.
type Evaluator[I, R any] struct {
	session        *auth.Session
	config         *config.Config
	tracerProvider *trace.TracerProvider
}

// NewEvaluator creates a new evaluator with explicit dependencies.
// The type parameters I (input) and R (result/output) must be specified explicitly.
// Most users should use braintrust.NewEvaluator(client).
func NewEvaluator[I, R any](session *auth.Session, cfg *config.Config, tp *trace.TracerProvider) *Evaluator[I, R] {
	return &Evaluator[I, R]{
		session:        session,
		config:         cfg,
		tracerProvider: tp,
	}
}

// Functions returns an API for loading server-side Braintrust functions (tasks/prompts and scorers).
// Use Functions().Task() to load tasks/prompts and Functions().Scorer() to load scorers.
func (e *Evaluator[I, R]) Functions() *FunctionsAPI[I, R] {
	// Get endpoints from session (prefers logged-in info, falls back to opts)
	endpoints := e.session.Endpoints()

	// Create api.Client for function operations
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))

	return &FunctionsAPI[I, R]{
		api:         apiClient,
		projectName: e.config.DefaultProjectName,
	}
}

// Datasets returns a DatasetAPI for loading datasets with this evaluator's type parameters.
func (e *Evaluator[I, R]) Datasets() *DatasetAPI[I, R] {
	// Get endpoints from session (prefers logged-in info, falls back to opts)
	endpoints := e.session.Endpoints()

	// Create api.Client for dataset operations
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))

	return &DatasetAPI[I, R]{
		api: apiClient,
	}
}

// Run executes an evaluation using this evaluator's dependencies.
func (e *Evaluator[I, R]) Run(ctx context.Context, opts Opts[I, R]) (*Result, error) {
	return Run(ctx, opts, e.config, e.session, e.tracerProvider)
}
