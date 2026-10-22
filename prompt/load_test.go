package prompt_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

// The load tests provision this fixture independently of other packages.
const (
	testProject = "go-sdk-tests"
	testSlug    = "sdk-go-load-prompt"
)

func testAPI(t *testing.T) *openapi.Client {
	t.Helper()
	client := https.NewGenerated(vcr.GetHTTPSClient(t))
	ensureTestPrompt(t, client)
	return client
}

func ensureTestPrompt(t *testing.T, client *openapi.Client) {
	t.Helper()
	ctx := context.Background()
	responses := &openapi.ClientWithResponses{ClientInterface: client}
	project, err := responses.PostProjectWithResponse(ctx, openapi.CreateProject{Name: testProject})
	require.NoError(t, err)
	require.NotNil(t, project.JSON200, "register project: %s", project.Body)

	existing, err := responses.GetPromptWithResponse(ctx, &openapi.GetPromptParams{
		ProjectId: https.String(project.JSON200.Id.String()),
		Slug:      https.String(testSlug),
		Limit:     https.Positive(1),
	})
	require.NoError(t, err)
	require.NotNil(t, existing.JSON200, "find prompt fixture: %s", existing.Body)
	if len(existing.JSON200.Objects) == 0 {
		body, err := json.Marshal(map[string]any{
			"project_id": project.JSON200.Id,
			"name":       "SDK Go load prompt",
			"slug":       testSlug,
			"prompt_data": map[string]any{
				"prompt": map[string]any{
					"type": "chat",
					"messages": []map[string]string{
						{"role": "system", "content": "You are a helpful assistant."},
						{"role": "user", "content": "Say hello to {{name}}."},
					},
				},
				"options": map[string]any{"model": "gpt-4o-mini"},
			},
		})
		require.NoError(t, err)
		created, err := responses.PostPromptWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.NotNil(t, created.JSON200, "create prompt fixture: %s", created.Body)
	}
}

func TestLoad_BySlug(t *testing.T) {
	p, err := prompt.LoadWithClient(context.Background(), testAPI(t), prompt.LoadOpts{
		Slug:    testSlug,
		Project: testProject,
	})
	require.NoError(t, err)

	assert.Equal(t, testSlug, p.Slug)
	assert.NotEmpty(t, p.ID)
	assert.NotEmpty(t, p.Version)
	assert.Equal(t, "gpt-4o-mini", p.Model())

	// A loaded prompt is immediately renderable, and carries the identity that
	// links a model call back to Braintrust.
	built, err := p.Build(map[string]any{"name": "Joe"})
	require.NoError(t, err)
	require.Len(t, built.Messages, 2)
	assert.Equal(t, "Say hello to Joe.", built.Messages[1].Content.String())
	require.NotNil(t, built.Metadata)
	assert.Equal(t, p.ID, built.Metadata.ID)
	assert.Equal(t, p.Version, built.Metadata.Version)
}

func TestLoad_ByID(t *testing.T) {
	client := testAPI(t)

	found, err := prompt.LoadWithClient(context.Background(), client, prompt.LoadOpts{
		Slug:    testSlug,
		Project: testProject,
	})
	require.NoError(t, err)

	p, err := prompt.LoadWithClient(context.Background(), client, prompt.LoadOpts{ID: found.ID})
	require.NoError(t, err)
	assert.Equal(t, found.ID, p.ID)
	assert.Equal(t, "gpt-4o-mini", p.Model())
}

func TestLoad_ProjectIDWinsOverStaleProjectName(t *testing.T) {
	client := testAPI(t)

	// Resolve the prompt's real project ID via a slug lookup.
	found, err := prompt.LoadWithClient(context.Background(), client, prompt.LoadOpts{
		Slug:    testSlug,
		Project: testProject,
	})
	require.NoError(t, err)
	require.NotEmpty(t, found.ProjectID)

	// Loading by that ID plus a bogus project name must still find it: per the
	// documented contract, ProjectID is authoritative and the name is dropped.
	p, err := prompt.LoadWithClient(context.Background(), client, prompt.LoadOpts{
		Slug:      testSlug,
		ProjectID: found.ProjectID,
		Project:   "this-project-name-does-not-exist",
	})
	require.NoError(t, err)
	assert.Equal(t, found.ID, p.ID)
}

func TestLoad_NotFound(t *testing.T) {
	_, err := prompt.LoadWithClient(context.Background(), testAPI(t), prompt.LoadOpts{
		Slug:    "sdk-go-no-such-prompt",
		Project: testProject,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sdk-go-no-such-prompt")
	assert.Contains(t, err.Error(), testProject)
}

func TestLoad_Validation(t *testing.T) {
	ctx := context.Background()

	t.Run("needs a client", func(t *testing.T) {
		_, err := prompt.LoadWithClient(ctx, nil, prompt.LoadOpts{Slug: "x", Project: "p"})
		require.Error(t, err)
	})

	t.Run("needs a slug or an ID", func(t *testing.T) {
		_, err := prompt.LoadWithClient(ctx, &openapi.Client{}, prompt.LoadOpts{})
		require.Error(t, err)
	})

	t.Run("needs a project", func(t *testing.T) {
		_, err := prompt.LoadWithClient(ctx, &openapi.Client{}, prompt.LoadOpts{Slug: "x"})
		require.Error(t, err)
	})
}
