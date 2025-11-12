package eval

import (
	"context"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
)

// datasetInfo is a minimal interface for dataset metadata needed by registerExperiment.
type datasetInfo interface {
	ID() string
	Version() string
}

// registerExperiment creates or gets an experiment for the eval.
// This is an internal helper that uses the api package.
// projectName must be already resolved (not empty) by the caller.
func registerExperiment(ctx context.Context, apiClient *api.API, name string, projectName string, tags []string, metadata map[string]interface{}, update bool, dataset datasetInfo) (*experiments.Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("experiment name is required")
	}

	// Validate project name (should already be resolved by caller)
	if projectName == "" {
		return nil, fmt.Errorf("project name is required (set via WithProject option or Opts.ProjectName)")
	}

	// Extract dataset metadata
	datasetID := dataset.ID()
	datasetVersion := dataset.Version()

	// Create the project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{
		Name: projectName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	// Register the experiment
	experiment, err := apiClient.Experiments().Register(ctx, name, project.ID, experiments.RegisterOpts{
		Tags:           tags,
		Metadata:       metadata,
		Update:         update,
		DatasetID:      datasetID,
		DatasetVersion: datasetVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}

	return experiment, nil
}
