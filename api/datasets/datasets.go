// Package datasets provides operations for managing Braintrust datasets.
//
// New REST integrations should use github.com/braintrustdata/braintrust-sdk-go/openapi instead.
package datasets

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// New creates a new datasets API client.
//
// Deprecated: Use [openapi.NewClientWithResponses].
func New(client *https.Client) *API {
	return &API{client: https.NewOpenAPIClient(client)}
}

// Create creates a new dataset.
//
// Deprecated: Use [openapi.ClientWithResponses.PostDatasetWithResponse].
func (a *API) Create(ctx context.Context, params CreateParams) (*Dataset, error) {
	if params.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	if params.Name == "" {
		return nil, fmt.Errorf("dataset name is required")
	}

	body, err := a.client.JSONBody(params)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.PostDatasetWithBody(ctx, "application/json", body)
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
//
// Deprecated: Use [openapi.ClientWithResponses.PostDatasetIdInsertWithResponse].
func (a *API) Insert(ctx context.Context, datasetID string, params InsertParams) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is required")
	}

	body, err := a.client.JSONBody(params)
	if err != nil {
		return err
	}
	resp, err := a.client.PostDatasetIdInsertWithBody(ctx, datasetID, "application/json", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// InsertEvents is a convenience function that inserts events into a dataset.
// It wraps the events in InsertParams for you.
//
// Deprecated: Use [openapi.ClientWithResponses.PostDatasetIdInsertWithResponse]
// with the events in [openapi.InsertDatasetEventRequest].
func (a *API) InsertEvents(ctx context.Context, datasetID string, events []Event) error {
	return a.Insert(ctx, datasetID, InsertParams{Events: events})
}

// Delete deletes a dataset.
//
// Deprecated: Use [openapi.ClientWithResponses.DeleteDatasetIdWithResponse].
func (a *API) Delete(ctx context.Context, datasetID string) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is required")
	}

	resp, err := a.client.DeleteDatasetId(ctx, datasetID)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// Fetch retrieves a single page of events from a dataset with optional cursor pagination.
//
// Deprecated: Use [openapi.ClientWithResponses.PostDatasetIdFetchWithResponse].
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

	body, err := a.client.JSONBody(reqBody)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.PostDatasetIdFetchWithBody(ctx, datasetID, "application/json", body)
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
//
// Deprecated: Use [openapi.ClientWithResponses.GetDatasetWithResponse].
func (a *API) Query(ctx context.Context, params QueryParams) (*QueryResponse, error) {
	queryParams := &openapi.GetDatasetParams{
		DatasetName:   https.String(params.Name),
		ProjectId:     https.String(params.ProjectID),
		ProjectName:   https.String(params.ProjectName),
		Limit:         https.Positive(params.Limit),
		StartingAfter: https.String(params.StartingAfter),
		EndingBefore:  https.String(params.EndingBefore),
	}
	// The spec omits the existing singular id and version filters.
	resp, err := a.client.GetDataset(ctx, queryParams,
		https.Query("id", params.ID),
		https.Query("version", params.Version),
	)
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
