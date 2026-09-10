// Package prompts provides read access to the prompts stored in Braintrust.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides methods for reading prompts.
type API struct {
	client *https.Client
}

// New creates a new prompts API client.
func New(client *https.Client) *API {
	return &API{client: client}
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
func (a *API) Query(ctx context.Context, params QueryParams) ([]Prompt, error) {
	query := make(map[string]string)

	if params.ProjectName != "" {
		query["project_name"] = params.ProjectName
	}
	if params.ProjectID != "" {
		query["project_id"] = params.ProjectID
	}
	if params.Slug != "" {
		query["slug"] = params.Slug
	}
	if params.Version != "" {
		query["version"] = params.Version
	} else if params.Environment != "" {
		// version and environment are mutually exclusive; a pinned version wins,
		// matching the other SDKs.
		query["environment"] = params.Environment
	}
	if params.Limit > 0 {
		query["limit"] = fmt.Sprintf("%d", params.Limit)
	}

	resp, err := a.client.GET(ctx, "/v1/prompt", query)
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
func (a *API) Get(ctx context.Context, id string, params QueryParams) (*Prompt, error) {
	if id == "" {
		return nil, fmt.Errorf("prompt ID is required")
	}

	query := make(map[string]string)
	if params.Version != "" {
		query["version"] = params.Version
	} else if params.Environment != "" {
		query["environment"] = params.Environment
	}

	resp, err := a.client.GET(ctx, "/v1/prompt/"+id, query)
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
