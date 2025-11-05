package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// Experiment represents an experiment from the API
type Experiment struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	ProjectID string                 `json:"project_id"`
	Tags      []string               `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ExperimentRequest represents the request payload for creating an experiment
type ExperimentRequest struct {
	ProjectID string                 `json:"project_id"`
	Name      string                 `json:"name"`
	EnsureNew bool                   `json:"ensure_new"`
	Tags      []string               `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// RegisterExperimentOpts contains optional parameters for registering an experiment
type RegisterExperimentOpts struct {
	Tags     []string
	Metadata map[string]interface{}
	Update   bool // If true, allow reusing existing experiment instead of creating new one
}

// ExperimentsClient handles experiment-related API operations
type ExperimentsClient struct {
	client *API
}

// Register creates or gets an experiment by name within a project.
// If an experiment with the given name already exists and Update is true, it returns that experiment.
// If Update is false (or via EnsureNew), it creates a new experiment.
func (e *ExperimentsClient) Register(ctx context.Context, name, projectID string, opts RegisterExperimentOpts) (*Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("experiment name is required")
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	reqBody := ExperimentRequest{
		ProjectID: projectID,
		Name:      name,
		EnsureNew: !opts.Update, // When Update=true, allow reusing existing experiment
		Tags:      opts.Tags,
		Metadata:  opts.Metadata,
	}

	resp, err := e.client.doRequest(ctx, "POST", "/v1/experiment", reqBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Experiment
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}
