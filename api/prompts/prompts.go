// Package prompts provides read access to the prompts stored in Braintrust.
//
// New integrations should use github.com/braintrustdata/braintrust-sdk-go/openapi for REST
// operations, or braintrust.Client.LoadPrompt for renderable prompts.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// API provides methods for reading prompts.
//
// Deprecated: Use [openapi.Client] or [openapi.ClientWithResponses].
type API struct {
	client *https.OpenAPIClient
}

// New creates a new prompts API client.
//
// Deprecated: Use [openapi.NewClientWithResponses].
func New(client *https.Client) *API {
	return &API{client: https.NewOpenAPIClient(client)}
}

// Prompt is a prompt row as Braintrust stores it.
//
// PromptData is left as raw JSON so that this package stays pure transport: the
// prompt package decodes it into a renderable prompt. Keeping the dependency
// pointing that way means the prompt package can use this one.
type Prompt struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	XactID      string          `json:"_xact_id"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description,omitempty"`
	PromptData  json.RawMessage `json:"prompt_data"`
	Tags        []string        `json:"tags,omitempty"`
}

// QueryParams selects which prompts to return.
type QueryParams struct {
	// Project identity (either/or).
	ProjectName string
	ProjectID   string

	// Slug filters by prompt slug.
	Slug string

	// Version pins to a specific prompt version.
	Version string

	// Environment loads the prompt deployed to an environment (dev, staging,
	// production). Ignored when Version is set.
	Environment string

	// Limit caps the number of results.
	Limit int
}

// queryResponse is the list envelope the API returns.
type queryResponse struct {
	Objects []Prompt `json:"objects"`
}

// Query searches for prompts matching params.
//
// Deprecated: Use [openapi.ClientWithResponses.GetPromptWithResponse], or
// braintrust.Client.LoadPrompt to fetch and render a prompt.
func (a *API) Query(ctx context.Context, params QueryParams) ([]Prompt, error) {
	query := &openapi.GetPromptParams{
		ProjectName: https.String(params.ProjectName),
		ProjectId:   https.String(params.ProjectID),
		Slug:        https.String(params.Slug),
		Version:     https.String(params.Version),
		Limit:       https.Positive(params.Limit),
	}
	if params.Version == "" {
		// A pinned version takes precedence over the environment.
		query.Environment = https.String(params.Environment)
	}

	resp, err := a.client.GetPrompt(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return result.Objects, nil
}

// Get returns a single prompt by ID. Version and Environment are optional; a
// pinned version wins over an environment.
//
// Deprecated: Use [openapi.ClientWithResponses.GetPromptIdWithResponse], or
// braintrust.Client.LoadPrompt to fetch and render a prompt.
func (a *API) Get(ctx context.Context, id string, params QueryParams) (*Prompt, error) {
	if id == "" {
		return nil, fmt.Errorf("prompt ID is required")
	}

	query := &openapi.GetPromptIdParams{
		Version: https.String(params.Version),
	}
	if params.Version == "" {
		query.Environment = https.String(params.Environment)
	}

	resp, err := a.client.GetPromptId(ctx, id, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result Prompt
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &result, nil
}
