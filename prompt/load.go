package prompt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	promptsapi "github.com/braintrustdata/braintrust-sdk-go/api/prompts"
)

// LoadOpts selects the prompt [Load] fetches.
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

// Load fetches a prompt from Braintrust and returns it ready to render.
//
// Most callers want [braintrust.Client.LoadPrompt], which supplies the API
// client and the default project. Use Load directly when you already hold an
// API client:
//
//	p, err := prompt.Load(ctx, client.API(), prompt.LoadOpts{Slug: "summarizer"})
//	if err != nil {
//		return err
//	}
//	built, err := p.Build(map[string]any{"input": article})
//
// The prompt is fetched every time; nothing is cached.
func Load(ctx context.Context, client *api.API, opts LoadOpts) (*Prompt, error) {
	if client == nil {
		return nil, errors.New("loading a prompt needs an API client")
	}
	if opts.ID == "" && opts.Slug == "" {
		return nil, errors.New("loading a prompt needs a Slug or an ID")
	}

	prompts := client.Prompts()

	if opts.ID != "" {
		row, err := prompts.Get(ctx, opts.ID, promptsapi.QueryParams{
			Version:     opts.Version,
			Environment: opts.Environment,
		})
		if err != nil {
			return nil, fmt.Errorf("loading prompt %q: %w", opts.ID, err)
		}
		return fromRow(*row)
	}

	if opts.Project == "" && opts.ProjectID == "" {
		return nil, fmt.Errorf("loading prompt %q needs a project", opts.Slug)
	}

	rows, err := prompts.Query(ctx, promptsapi.QueryParams{
		ProjectName: opts.Project,
		ProjectID:   opts.ProjectID,
		Slug:        opts.Slug,
		Version:     opts.Version,
		Environment: opts.Environment,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("loading prompt %q: %w", opts.Slug, err)
	}
	if len(rows) == 0 {
		where := opts.Project
		if where == "" {
			where = opts.ProjectID
		}
		return nil, fmt.Errorf("prompt %q not found in project %q", opts.Slug, where)
	}

	return fromRow(rows[0])
}

// fromRow turns a stored prompt row into a renderable prompt.
func fromRow(row promptsapi.Prompt) (*Prompt, error) {
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
