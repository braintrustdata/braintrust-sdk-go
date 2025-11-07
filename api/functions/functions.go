package functions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// New creates a new functions API client.
func New(client *https.Client) *API {
	return &API{client: client}
}

// Query searches for functions matching the given options.
// Returns a list of functions that match the criteria.
func (a *API) Query(ctx context.Context, params QueryParams) ([]Function, error) {
	// Build query parameters
	queryParams := url.Values{}

	if params.ProjectName != "" {
		queryParams.Set("project_name", params.ProjectName)
	}
	if params.ProjectID != "" {
		queryParams.Set("project_id", params.ProjectID)
	}
	if params.Slug != "" {
		queryParams.Set("slug", params.Slug)
	}
	if params.FunctionName != "" {
		queryParams.Set("function_name", params.FunctionName)
	}
	if params.Version != "" {
		queryParams.Set("version", params.Version)
	}
	if params.Environment != "" {
		queryParams.Set("environment", params.Environment)
	}
	if params.Limit > 0 {
		queryParams.Set("limit", fmt.Sprintf("%d", params.Limit))
	}

	resp, err := a.client.GET(ctx, "/v1/function", queryParams)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result QueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return result.Objects, nil
}

// Create creates a new function.
func (a *API) Create(ctx context.Context, params CreateParams) (*Function, error) {
	if params.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if params.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	resp, err := a.client.POST(ctx, "/v1/function", params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Function
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// Invoke calls a function with the given input and returns the output.
func (a *API) Invoke(ctx context.Context, functionID string, input any) (any, error) {
	if functionID == "" {
		return nil, fmt.Errorf("function ID is required")
	}

	req := InvokeParams{
		Input: input,
	}

	path := fmt.Sprintf("/v1/function/%s/invoke", functionID)
	resp, err := a.client.POST(ctx, path, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Read the entire response body so we can parse it multiple ways
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse response - try as object first, then as raw value
	var response map[string]any
	if err := json.Unmarshal(body, &response); err == nil {
		// Response is an object, extract output field if present
		if output, ok := response["output"]; ok {
			return output, nil
		}
		// If no output field, return the whole object
		return response, nil
	}

	// Response is not an object, try parsing as raw JSON value (string, number, etc.)
	var output any
	if err := json.Unmarshal(body, &output); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return output, nil
}

// Delete deletes a function by ID.
func (a *API) Delete(ctx context.Context, functionID string) error {
	if functionID == "" {
		return fmt.Errorf("function ID is required")
	}

	path := fmt.Sprintf("/v1/function/%s", functionID)
	resp, err := a.client.DELETE(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
