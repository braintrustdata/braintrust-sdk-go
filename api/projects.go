package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
)

// Project represents a project from the API
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectsClient handles project-related API operations
type ProjectsClient struct {
	client *API
}

// Register creates or gets a project by name.
// If a project with the given name already exists, it returns that project.
func (p *ProjectsClient) Register(ctx context.Context, name string) (*Project, error) {
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}

	reqBody := map[string]interface{}{
		"name": name,
	}

	resp, err := p.client.doRequest(ctx, "POST", "/v1/project", reqBody)
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

// RegisterProject is a standalone helper that creates an api.Client internally.
// TODO: Remove this after completing API refactoring - use client.Projects().Register() instead.
func RegisterProject(ctx context.Context, cfg *config.Config, session *auth.Session, name string) (*Project, error) {
	apiURL := cfg.APIURL
	if apiURL == "" {
		if ok, info := session.Info(); ok && info.APIURL != "" {
			apiURL = info.APIURL
		} else {
			apiURL = "https://api.braintrust.dev"
		}
	}

	client, err := NewClient(cfg.APIKey, WithAPIURL(apiURL))
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	return client.Projects().Register(ctx, name)
}
