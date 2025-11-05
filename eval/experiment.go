package eval

import (
	"context"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
)

// registerExperiment creates or gets an experiment for the eval.
// This is an internal helper that uses the api package.
func registerExperiment(ctx context.Context, cfg *config.Config, session *auth.Session, name string, tags []string, metadata map[string]interface{}, update bool) (*api.Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("experiment name is required")
	}

	// First get or create the project
	projectName := cfg.DefaultProjectName
	if projectName == "" {
		return nil, fmt.Errorf("project name is required (set via WithProject option)")
	}

	// Create API client using session endpoints (prefers logged-in info, falls back to opts)
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	// Register the project
	project, err := apiClient.Projects().Register(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("failed to register project: %w", err)
	}

	// Register the experiment
	experiment, err := apiClient.Experiments().Register(ctx, name, project.ID, api.RegisterExperimentOpts{
		Tags:     tags,
		Metadata: metadata,
		Update:   update,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}

	return experiment, nil
}
