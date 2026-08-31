package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/api/prompts"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const integrationTestProject = "go-sdk-tests"

// testPromptSlug is the prompt these tests read. It is created by
// createTestPrompt, so the tests do not depend on pre-existing server state.
const testPromptSlug = "sdk-go-load-prompt"

// createTestPrompt creates (or replaces) the prompt the read tests query for,
// and returns the project holding it.
func createTestPrompt(t *testing.T, client *https.Client) *projects.Project {
	t.Helper()
	ctx := context.Background()

	project, err := projects.New(client).Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	_, err = functions.New(client).Create(ctx, functions.CreateParams{
		ProjectID:    project.ID,
		Name:         testPromptSlug,
		Slug:         testPromptSlug,
		FunctionType: "prompt",
		FunctionData: map[string]any{"type": "prompt"},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "system", "content": "You greet people warmly."},
					{"role": "user", "content": "Say hello to {{name}}."},
				},
			},
			"options": map[string]any{
				"model":  "gpt-4o-mini",
				"params": map[string]any{"temperature": 0},
			},
			"template_format": "mustache",
		},
	})
	require.NoError(t, err)

	return project
}

func TestPrompts_QueryBySlug(t *testing.T) {
	ctx := context.Background()
	client := vcr.GetHTTPSClient(t)
	createTestPrompt(t, client)

	rows, err := prompts.New(client).Query(ctx, prompts.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testPromptSlug,
		Limit:       1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row := rows[0]
	assert.Equal(t, testPromptSlug, row.Slug)
	assert.NotEmpty(t, row.ID)
	assert.NotEmpty(t, row.ProjectID)
	assert.NotEmpty(t, row.XactID, "the version is needed to link traces back to the prompt")

	// This package is pure transport: it hands back prompt_data as raw JSON and
	// the prompt package turns it into something renderable (see
	// prompt/load_test.go). All that matters here is that the payload arrives.
	assert.Contains(t, string(row.PromptData), "Say hello to {{name}}.")
	assert.Contains(t, string(row.PromptData), "gpt-4o-mini")
}

func TestPrompts_GetByID(t *testing.T) {
	ctx := context.Background()
	client := vcr.GetHTTPSClient(t)
	createTestPrompt(t, client)

	api := prompts.New(client)
	rows, err := api.Query(ctx, prompts.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testPromptSlug,
		Limit:       1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row, err := api.Get(ctx, rows[0].ID, prompts.QueryParams{})
	require.NoError(t, err)
	assert.Equal(t, rows[0].ID, row.ID)
	assert.NotEmpty(t, row.PromptData)
}

func TestPrompts_GetRequiresID(t *testing.T) {
	_, err := prompts.New(nil).Get(context.Background(), "", prompts.QueryParams{})
	require.Error(t, err)
}

func TestPrompts_QueryUnknownSlugIsEmpty(t *testing.T) {
	client := vcr.GetHTTPSClient(t)

	rows, err := prompts.New(client).Query(context.Background(), prompts.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        "sdk-go-no-such-prompt",
	})
	require.NoError(t, err)
	assert.Empty(t, rows)
}
