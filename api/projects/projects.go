package projects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// API provides operations for managing Braintrust projects.
//
// Deprecated: Use [openapi.Client] or [openapi.ClientWithResponses].
type API struct {
	client *https.OpenAPIClient
}

// New creates a new projects API client.
//
// Deprecated: Use [openapi.NewClientWithResponses].
func New(client *https.Client) *API {
	return &API{client: https.NewOpenAPIClient(client)}
}

// Create creates a new project or returns an existing one if a project with
// the same name already exists. This operation is idempotent.
//
// Example:
//
//	project, err := client.Projects().Create(ctx, projects.CreateParams{
//	    Name: "my-project",
//	})
//
// Deprecated: Use [openapi.ClientWithResponses.PostProjectWithResponse].
func (a *API) Create(ctx context.Context, params CreateParams) (*Project, error) {
	if params.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}

	body, err := a.client.JSONBody(params)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.PostProjectWithBody(ctx, "application/json", body)
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
//
// Deprecated: Use [openapi.ClientWithResponses.GetProjectIdWithResponse].
func (a *API) Get(ctx context.Context, id string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	resp, err := a.client.GetProjectId(ctx, id)
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
//
// Deprecated: Use [openapi.ClientWithResponses.GetProjectWithResponse].
func (a *API) List(ctx context.Context, params ListParams) (*ListResponse, error) {
	queryParams := &openapi.GetProjectParams{
		Limit: https.Positive(params.Limit),
	}
	// The pinned specification omits the supported org_id project filter.
	resp, err := a.client.GetProject(ctx, queryParams, https.Query("org_id", params.OrgID))
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
//
// Deprecated: Use [openapi.ClientWithResponses.DeleteProjectIdWithResponse].
func (a *API) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("project ID is required")
	}

	resp, err := a.client.DeleteProjectId(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
