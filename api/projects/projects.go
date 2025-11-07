package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides operations for managing Braintrust projects.
type API struct {
	client *https.Client
}

// New creates a new projects API client.
func New(client *https.Client) *API {
	return &API{client: client}
}

// Create creates a new project or returns an existing one if a project with
// the same name already exists. This operation is idempotent.
//
// Example:
//
//	project, err := client.Projects().Create(ctx, projects.CreateParams{
//	    Name: "my-project",
//	})
func (a *API) Create(ctx context.Context, params CreateParams) (*Project, error) {
	if params.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}

	resp, err := a.client.POST(ctx, "/v1/project", params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Project
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// Get retrieves a project by ID.
//
// Example:
//
//	project, err := client.Projects().Get(ctx, "proj_123")
func (a *API) Get(ctx context.Context, id string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	path := fmt.Sprintf("/v1/project/%s", id)
	resp, err := a.client.GET(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Project
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}

// List retrieves a list of projects with optional filtering.
//
// Example:
//
//	projects, err := client.Projects().List(ctx, projects.ListParams{
//	    Limit: 10,
//	})
func (a *API) List(ctx context.Context, params ListParams) (*ListResponse, error) {
	queryParams := url.Values{}

	if params.OrgID != "" {
		queryParams.Set("org_id", params.OrgID)
	}
	if params.Limit > 0 {
		queryParams.Set("limit", strconv.Itoa(params.Limit))
	}

	resp, err := a.client.GET(ctx, "/v1/project", queryParams)
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

// Delete deletes a project by ID.
//
// Example:
//
//	err := client.Projects().Delete(ctx, "proj_123")
func (a *API) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("project ID is required")
	}

	path := fmt.Sprintf("/v1/project/%s", id)
	resp, err := a.client.DELETE(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
