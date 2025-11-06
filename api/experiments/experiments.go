package experiments

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// New creates a new Experiments API client
func New(client *https.Client) *API {
	return &API{client: client}
}

// Create creates a new experiment. If there is an existing experiment in the project
// with the same name as the one specified in the request, will return the existing
// experiment unmodified (unless EnsureNew is true).
func (a *API) Create(ctx context.Context, params CreateParams) (*Experiment, error) {
	if params.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	resp, err := a.client.POST(ctx, "/v1/experiment", params)
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

// Register creates or gets an experiment by name within a project.
// This is a convenience wrapper around Create for backward compatibility.
// If an experiment with the given name already exists and Update is true, it returns that experiment.
// If Update is false (or via EnsureNew), it creates a new experiment.
func (a *API) Register(ctx context.Context, name, projectID string, opts RegisterOpts) (*Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("experiment name is required")
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	return a.Create(ctx, CreateParams{
		ProjectID:      projectID,
		Name:           name,
		EnsureNew:      !opts.Update, // When Update=true, allow reusing existing experiment
		Tags:           opts.Tags,
		Metadata:       opts.Metadata,
		DatasetID:      opts.DatasetID,
		DatasetVersion: opts.DatasetVersion,
	})
}

// List returns a list of experiments filtered by the given parameters.
func (a *API) List(ctx context.Context, params ListParams) (*ListResponse, error) {
	queryParams := make(map[string]string)

	if params.ProjectID != "" {
		queryParams["project_id"] = params.ProjectID
	}
	if params.ExperimentName != "" {
		queryParams["experiment_name"] = params.ExperimentName
	}
	if params.OrgName != "" {
		queryParams["org_name"] = params.OrgName
	}
	if params.Limit > 0 {
		queryParams["limit"] = strconv.Itoa(params.Limit)
	}

	resp, err := a.client.GET(ctx, "/v1/experiment", queryParams)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// Get retrieves an experiment by its ID.
func (a *API) Get(ctx context.Context, experimentID string) (*Experiment, error) {
	if experimentID == "" {
		return nil, fmt.Errorf("experiment ID is required")
	}

	resp, err := a.client.GET(ctx, "/v1/experiment/"+experimentID, nil)
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

// Delete deletes an experiment by its ID.
func (a *API) Delete(ctx context.Context, experimentID string) error {
	if experimentID == "" {
		return fmt.Errorf("experiment ID is required")
	}

	resp, err := a.client.DELETE(ctx, "/v1/experiment/"+experimentID)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
