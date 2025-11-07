// Package datasets provides operations for managing Braintrust datasets.
package datasets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// New creates a new datasets API client.
func New(client *https.Client) *API {
	return &API{client: client}
}

// Create creates a new dataset.
func (a *API) Create(ctx context.Context, params CreateParams) (*Dataset, error) {
	if params.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	if params.Name == "" {
		return nil, fmt.Errorf("dataset name is required")
	}

	resp, err := a.client.POST(ctx, "/v1/dataset", params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Dataset
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// Insert inserts events into a dataset.
func (a *API) Insert(ctx context.Context, datasetID string, params InsertParams) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is required")
	}

	resp, err := a.client.POST(ctx, "/v1/dataset/"+datasetID+"/insert", params)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// InsertEvents is a convenience function that inserts events into a dataset.
// It wraps the events in InsertParams for you.
func (a *API) InsertEvents(ctx context.Context, datasetID string, events []Event) error {
	return a.Insert(ctx, datasetID, InsertParams{Events: events})
}

// Delete deletes a dataset.
func (a *API) Delete(ctx context.Context, datasetID string) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is required")
	}

	resp, err := a.client.DELETE(ctx, "/v1/dataset/"+datasetID)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// Fetch retrieves a single page of events from a dataset with optional cursor pagination.
func (a *API) Fetch(ctx context.Context, datasetID string, cursor string, limit int) (*FetchResponse, error) {
	if datasetID == "" {
		return nil, fmt.Errorf("dataset ID is required")
	}

	reqBody := map[string]interface{}{
		"limit": limit,
	}
	if cursor != "" {
		reqBody["cursor"] = cursor
	}

	resp, err := a.client.POST(ctx, "/v1/dataset/"+datasetID+"/fetch", reqBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result FetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// Query searches for datasets by name, version, or other criteria.
func (a *API) Query(ctx context.Context, params QueryParams) (*QueryResponse, error) {
	// Build query parameters
	queryParams := url.Values{}

	if params.ID != "" {
		queryParams.Set("id", params.ID)
	}
	if params.Name != "" {
		queryParams.Set("dataset_name", params.Name)
	}
	if params.Version != "" {
		queryParams.Set("version", params.Version)
	}
	if params.ProjectID != "" {
		queryParams.Set("project_id", params.ProjectID)
	}
	if params.ProjectName != "" {
		queryParams.Set("project_name", params.ProjectName)
	}
	if params.Limit > 0 {
		queryParams.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.StartingAfter != "" {
		queryParams.Set("starting_after", params.StartingAfter)
	}
	if params.EndingBefore != "" {
		queryParams.Set("ending_before", params.EndingBefore)
	}

	resp, err := a.client.GET(ctx, "/v1/dataset", queryParams)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result QueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}
