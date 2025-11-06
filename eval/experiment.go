package eval

import (
	"context"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
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

	endpoints := session.Endpoints()
	c, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	// Create the project
	project, err := c.Projects().Create(ctx, projects.CreateParams{
		Name: projectName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	// Register the experiment
	experiment, err := c.Experiments().Register(ctx, name, project.ID, api.RegisterExperimentOpts{
		Tags:     tags,
		Metadata: metadata,
		Update:   update,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}

	return experiment, nil
}
