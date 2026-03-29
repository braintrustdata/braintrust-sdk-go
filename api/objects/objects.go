package objects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides methods for generic object operations.
type API struct {
	client *https.Client
}

// New creates a new objects API client.
func New(client *https.Client) *API {
	return &API{client: client}
}

// Fetch retrieves rows from a given object type and ID.
func (a *API) Fetch(ctx context.Context, objectType, objectID string, params FetchParams) (*FetchResponse, error) {
	if objectType == "" {
		return nil, fmt.Errorf("object type is required")
	}
	if objectID == "" {
		return nil, fmt.Errorf("object ID is required")
	}

	path := fmt.Sprintf("/v1/%s/%s/fetch", objectType, objectID)
	resp, err := a.client.POST(ctx, path, params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out FetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}
	return &out, nil
}
