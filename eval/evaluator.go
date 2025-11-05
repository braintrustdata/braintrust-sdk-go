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
// in sequence with the same type signature, or use hosted prompts, scorers and dataasets
// with automatirc type conversion.
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

// Datasets returns a DatasetAPI for loading datasets with this evaluator's type parameters.
func (e *Evaluator[I, R]) Datasets() *DatasetAPI[I, R] {
	// Get endpoints from session (prefers logged-in info, falls back to opts)
	endpoints := e.session.Endpoints()

	// Create api.Client for dataset operations
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	if err != nil {
		// This shouldn't happen since session is validated, but handle it anyway
		panic("failed to create API client: " + err.Error())
	}

	return &DatasetAPI[I, R]{
		apiClient: apiClient,
	}
}

// Tasks returns a TaskAPI for loading tasks/prompts with this evaluator's type parameters.
func (e *Evaluator[I, R]) Tasks() *TaskAPI[I, R] {
	// Get endpoints from session (prefers logged-in info, falls back to opts)
	endpoints := e.session.Endpoints()

	// Create api.API for task operations
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	if err != nil {
		// This shouldn't happen since session is validated, but handle it anyway
		panic("failed to create API client: " + err.Error())
	}

	return &TaskAPI[I, R]{
		api:         apiClient,
		projectName: e.config.DefaultProjectName,
	}
}

// Scorers returns a ScorerAPI for loading scorers with this evaluator's type parameters.
func (e *Evaluator[I, R]) Scorers() *ScorerAPI[I, R] {
	// Get endpoints from session (prefers logged-in info, falls back to opts)
	endpoints := e.session.Endpoints()

	// Create api.API for scorer operations
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	if err != nil {
		// This shouldn't happen since session is validated, but handle it anyway
		panic("failed to create API client: " + err.Error())
	}

	return &ScorerAPI[I, R]{
		api:         apiClient,
		projectName: e.config.DefaultProjectName,
	}
}

// Run executes an evaluation using this evaluator's dependencies.
func (e *Evaluator[I, R]) Run(ctx context.Context, opts Opts[I, R]) (*Result, error) {
	return Run(ctx, opts, e.config, e.session, e.tracerProvider)
}
