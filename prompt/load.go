package prompt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// LoadOpts selects the prompt [LoadWithClient] fetches.
type LoadOpts struct {
	// Slug is the prompt's slug. Required unless ID is set.
	Slug string

	// Project is the name of the project holding the prompt. Ignored when ID or
	// ProjectID is set. [braintrust.Client.LoadPrompt] fills this in with the
	// client's project when it is empty.
	Project string

	// ProjectID identifies the project by ID instead of by name.
	ProjectID string

	// ID loads a prompt by its Braintrust ID, ignoring Slug and project.
	ID string

	// Version pins to a specific prompt version. Defaults to the latest.
	Version string

	// Environment loads the prompt deployed to an environment, for example
	// "production". Ignored when Version is set.
	Environment string
}

// LoadWithClient fetches a prompt from Braintrust and returns it ready to render.
//
// Most callers want [braintrust.Client.LoadPrompt], which supplies the API
// client and the default project. Use LoadWithClient directly when you already
// hold a configured [openapi.Client].
//
// The prompt is fetched every time; nothing is cached.
func LoadWithClient(ctx context.Context, client *openapi.Client, opts LoadOpts) (*Prompt, error) {
	if client == nil {
		return nil, errors.New("loading a prompt needs an API client")
	}
	if opts.ID == "" && opts.Slug == "" {
		return nil, errors.New("loading a prompt needs a Slug or an ID")
	}

	environment := opts.Environment
	if opts.Version != "" {
		environment = ""
	}

	if opts.ID != "" {
		row, err := https.DecodeJSON[storedPrompt](client.GetPromptId(ctx, opts.ID, &openapi.GetPromptIdParams{
			Version:     https.String(opts.Version),
			Environment: https.String(environment),
		}))
		if err != nil {
			return nil, fmt.Errorf("loading prompt %q: %w", opts.ID, err)
		}
		return fromRow(row)
	}

	if opts.Project == "" && opts.ProjectID == "" {
		return nil, fmt.Errorf("loading prompt %q needs a project", opts.Slug)
	}

	// ProjectID is authoritative: when it is set, don't also send a (possibly
	// stale) project name, which the API would treat as an additional filter.
	projectName := opts.Project
	if opts.ProjectID != "" {
		projectName = ""
	}

	rows, err := https.DecodeJSON[struct {
		Objects []storedPrompt `json:"objects"`
	}](client.GetPrompt(ctx, &openapi.GetPromptParams{
		ProjectName: https.String(projectName),
		ProjectId:   https.String(opts.ProjectID),
		Slug:        https.String(opts.Slug),
		Version:     https.String(opts.Version),
		Environment: https.String(environment),
		Limit:       https.Positive(1),
	}))
	if err != nil {
		return nil, fmt.Errorf("loading prompt %q: %w", opts.Slug, err)
	}
	if len(rows.Objects) == 0 {
		where := opts.Project
		if where == "" {
			where = opts.ProjectID
		}
		return nil, fmt.Errorf("prompt %q not found in project %q", opts.Slug, where)
	}

	return fromRow(rows.Objects[0])
}

// storedPrompt preserves prompt data as JSON until it is decoded into the SDK's
// renderable domain types, without a lossy generated-model round trip.
type storedPrompt struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	XactID     string          `json:"_xact_id"`
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	PromptData json.RawMessage `json:"prompt_data"`
}

// fromRow turns a stored prompt row into a renderable prompt.
func fromRow(row storedPrompt) (*Prompt, error) {
	p := &Prompt{
		ID:        row.ID,
		Name:      row.Name,
		Slug:      row.Slug,
		ProjectID: row.ProjectID,
		Version:   row.XactID,
	}

	if len(row.PromptData) > 0 {
		if err := json.Unmarshal(row.PromptData, &p.Data); err != nil {
			return nil, fmt.Errorf("prompt %q: reading prompt data: %w", row.Slug, err)
		}
	}

	return p, nil
}
