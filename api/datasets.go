package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// DatasetEvent represents a single event/record in a dataset
type DatasetEvent struct {
	ID       string      `json:"id,omitempty"`
	Input    interface{} `json:"input"`
	Expected interface{} `json:"expected,omitempty"`
	Metadata interface{} `json:"metadata,omitempty"`
	Tags     []string    `json:"tags,omitempty"`
}

// DatasetRequest represents the request payload for creating a dataset
type DatasetRequest struct {
	ProjectID   string                 `json:"project_id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Dataset represents a dataset from the API
type Dataset struct {
	ID          string                 `json:"id"`
	ProjectID   string                 `json:"project_id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// FetchResponse represents a paginated response from the fetch endpoint
type FetchResponse struct {
	Events []json.RawMessage `json:"events"`
	Cursor string            `json:"cursor"`
}

// QueryResponse represents the response from querying datasets
type QueryResponse struct {
	Objects []Dataset `json:"objects"`
}

// DatasetsClient handles dataset-related API operations
type DatasetsClient struct {
	client *API
}

// Create creates a new dataset via the Braintrust API
func (d *DatasetsClient) Create(ctx context.Context, req DatasetRequest) (*Dataset, error) {
	resp, err := d.client.doRequest(ctx, "POST", "/v1/dataset", req)
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

// Insert inserts events into a dataset
func (d *DatasetsClient) Insert(ctx context.Context, datasetID string, events []DatasetEvent) error {
	reqBody := map[string]interface{}{
		"events": events,
	}

	resp, err := d.client.doRequest(ctx, "POST", "/v1/dataset/"+datasetID+"/insert", reqBody)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// Delete deletes a dataset
func (d *DatasetsClient) Delete(ctx context.Context, datasetID string) error {
	resp, err := d.client.doRequest(ctx, "DELETE", "/v1/dataset/"+datasetID, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// Fetch retrieves a single page of events from a dataset with optional cursor pagination
func (d *DatasetsClient) Fetch(ctx context.Context, datasetID string, cursor string, limit int) (*FetchResponse, error) {
	reqBody := map[string]interface{}{
		"limit": limit,
	}
	if cursor != "" {
		reqBody["cursor"] = cursor
	}

	resp, err := d.client.doRequest(ctx, "POST", "/v1/dataset/"+datasetID+"/fetch", reqBody)
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

// Query searches for datasets by name, version, or other criteria
func (d *DatasetsClient) Query(ctx context.Context, params map[string]string) (*QueryResponse, error) {
	// Convert map to url.Values
	urlParams := url.Values{}
	for k, v := range params {
		urlParams.Add(k, v)
	}

	resp, err := d.client.doRequestWithParams(ctx, "GET", "/v1/dataset", nil, urlParams)
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
