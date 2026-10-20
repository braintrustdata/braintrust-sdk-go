package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// datasetInfo is a minimal interface for dataset metadata needed by registerExperiment.
type datasetInfo interface {
	ID() string
	Version() string
}

// projectRef decodes only the project fields used by evals.
type projectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type experimentInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
}

// registerExperiment creates or gets an experiment for the eval.
// It decodes only the metadata needed by the evaluator.
//
// projectID takes precedence over projectName. When it is set the project is
// used as-is; otherwise the project is created (or fetched) by name, which
// requires create permission. The Braintrust playground supplies an ID
// precisely so that a caller's token does not need that permission.
func registerExperiment(ctx context.Context, apiClient *openapi.Client, name string, projectName string, projectID string, tags []string, metadata map[string]interface{}, update bool, dataset datasetInfo) (*experimentInfo, error) {
	if name == "" {
		return nil, fmt.Errorf("experiment name is required")
	}

	// Extract dataset metadata
	datasetID := dataset.ID()
	datasetVersion := dataset.Version()

	if projectID == "" {
		// Validate project name (should already be resolved by caller)
		if projectName == "" {
			return nil, fmt.Errorf("project name is required (set via WithProject option or Opts.ProjectName)")
		}

		project, err := https.DecodeJSON[projectRef](apiClient.PostProject(ctx, openapi.CreateProject{Name: projectName})) //nolint:bodyclose // false positive: DecodeJSON closes the body
		if err != nil {
			return nil, fmt.Errorf("failed to create project: %w", err)
		}
		projectID = project.ID
	}
	if projectID == "" {
		return nil, fmt.Errorf("failed to register experiment: project ID is required")
	}

	// Encode SDK metadata directly to retain arbitrary JSON numbers and the
	// existing omission semantics (notably ensure_new when updating).
	body, err := json.Marshal(experiments.CreateParams{
		ProjectID:      projectID,
		Name:           name,
		EnsureNew:      !update,
		Tags:           tags,
		Metadata:       metadata,
		DatasetID:      datasetID,
		DatasetVersion: datasetVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode experiment: %w", err)
	}
	experiment, err := https.DecodeJSON[experimentInfo](apiClient.PostExperimentWithBody(ctx, "application/json", bytes.NewReader(body))) //nolint:bodyclose // false positive: DecodeJSON closes the body
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}
	return &experiment, nil
}
