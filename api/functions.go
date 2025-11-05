package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

// Function represents a Braintrust function (prompt, tool, or scorer).
type Function struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	FunctionType string `json:"function_type"`
	Description  string `json:"description,omitempty"`
}

// FunctionQueryOpts contains options for querying functions.
type FunctionQueryOpts struct {
	// Project identity (either/or)
	ProjectName string // Filter by project name
	ProjectID   string // Filter by specific project ID

	// Function identity (either/or)
	Slug         string // Filter by function slug
	FunctionName string // Filter by function name

	// Query modifiers
	Version     string // Specific function version
	Environment string // Environment to load (dev/staging/production)
	Limit       int    // Max results (default: no limit)
}

// FunctionCreateRequest represents the request payload for creating a function.
type FunctionCreateRequest struct {
	ProjectID    string         `json:"project_id"`
	Name         string         `json:"name"`
	Slug         string         `json:"slug"`
	FunctionType string         `json:"function_type,omitempty"`
	FunctionData map[string]any `json:"function_data"`
	PromptData   map[string]any `json:"prompt_data,omitempty"`
	Description  string         `json:"description,omitempty"`
}

// FunctionInvokeRequest represents the request payload for invoking a function.
type FunctionInvokeRequest struct {
	Input any `json:"input"`
}

// FunctionInvokeResponse represents the response from invoking a function.
type FunctionInvokeResponse struct {
	Output any `json:"output"`
}

// FunctionsClient handles function-related API operations.
type FunctionsClient struct {
	client *API
}

// Query searches for functions matching the given options.
// Returns a list of functions that match the criteria.
func (f *FunctionsClient) Query(ctx context.Context, opts FunctionQueryOpts) ([]Function, error) {
	// Build query parameters
	params := url.Values{}

	if opts.ProjectName != "" {
		params.Add("project_name", opts.ProjectName)
	}
	if opts.ProjectID != "" {
		params.Add("project_id", opts.ProjectID)
	}
	if opts.Slug != "" {
		params.Add("slug", opts.Slug)
	}
	if opts.FunctionName != "" {
		params.Add("function_name", opts.FunctionName)
	}
	if opts.Version != "" {
		params.Add("version", opts.Version)
	}
	if opts.Environment != "" {
		params.Add("environment", opts.Environment)
	}
	if opts.Limit > 0 {
		params.Add("limit", fmt.Sprintf("%d", opts.Limit))
	}

	resp, err := f.client.doRequestWithParams(ctx, "GET", "/v1/function", nil, params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Objects []Function `json:"objects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return result.Objects, nil
}

// Create creates a new function.
func (f *FunctionsClient) Create(ctx context.Context, req FunctionCreateRequest) (*Function, error) {
	if req.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	resp, err := f.client.doRequest(ctx, "POST", "/v1/function", req)
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
func (f *FunctionsClient) Invoke(ctx context.Context, functionID string, input any) (any, error) {
	if functionID == "" {
		return nil, fmt.Errorf("function ID is required")
	}

	req := FunctionInvokeRequest{
		Input: input,
	}

	path := fmt.Sprintf("/v1/function/%s/invoke", functionID)
	resp, err := f.client.doRequest(ctx, "POST", path, req)
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
func (f *FunctionsClient) Delete(ctx context.Context, functionID string) error {
	if functionID == "" {
		return fmt.Errorf("function ID is required")
	}

	path := fmt.Sprintf("/v1/function/%s", functionID)
	resp, err := f.client.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
