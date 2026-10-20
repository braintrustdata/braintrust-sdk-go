package experiments

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// InsertEvents inserts or merges experiment rows. Set ID to a stable row ID and
// IsMerge to true when updating an existing row across process boundaries.
//
// Deprecated: Use [openapi.ClientWithResponses.PostExperimentIdInsertWithResponse].
func (a *API) InsertEvents(ctx context.Context, experimentID string, events []Event) error {
	if experimentID == "" {
		return fmt.Errorf("experiment ID is required")
	}
	if len(events) == 0 {
		return nil
	}
	body, err := a.client.JSONBody(map[string]any{"events": events})
	if err != nil {
		return err
	}
	resp, err := a.client.PostExperimentIdInsertWithBody(ctx, experimentID, "application/json", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

// New creates a new Experiments API client
//
// Deprecated: Use [openapi.NewClientWithResponses].
func New(client *https.Client) *API {
	return &API{client: https.NewOpenAPIClient(client)}
}

// Create creates a new experiment. If there is an existing experiment in the project
// with the same name as the one specified in the request, will return the existing
// experiment unmodified (unless EnsureNew is true).
//
// Deprecated: Use [openapi.ClientWithResponses.PostExperimentWithResponse].
func (a *API) Create(ctx context.Context, params CreateParams) (*Experiment, error) {
	if params.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	body, err := a.client.JSONBody(params)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.PostExperimentWithBody(ctx, "application/json", body)
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
//
// Deprecated: Use [openapi.ClientWithResponses.PostExperimentWithResponse].
// Set ensure_new to the inverse of opts.Update to preserve registration behavior.
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
//
// Deprecated: Use [openapi.ClientWithResponses.GetExperimentWithResponse].
func (a *API) List(ctx context.Context, params ListParams) (*ListResponse, error) {
	queryParams := &openapi.GetExperimentParams{
		ProjectId:      https.String(params.ProjectID),
		ExperimentName: https.String(params.ExperimentName),
		OrgName:        https.String(params.OrgName),
		Limit:          https.Positive(params.Limit),
	}

	resp, err := a.client.GetExperiment(ctx, queryParams)
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
//
// Deprecated: Use [openapi.ClientWithResponses.GetExperimentIdWithResponse].
func (a *API) Get(ctx context.Context, experimentID string) (*Experiment, error) {
	if experimentID == "" {
		return nil, fmt.Errorf("experiment ID is required")
	}

	resp, err := a.client.GetExperimentId(ctx, experimentID)
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
//
// Deprecated: Use [openapi.ClientWithResponses.DeleteExperimentIdWithResponse].
func (a *API) Delete(ctx context.Context, experimentID string) error {
	if experimentID == "" {
		return fmt.Errorf("experiment ID is required")
	}

	resp, err := a.client.DeleteExperimentId(ctx, experimentID)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
