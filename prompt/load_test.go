package prompt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

// The prompt these tests load is created by the api/prompts tests; see
// api/prompts/prompts_test.go.
const (
	testProject = "go-sdk-tests"
	testSlug    = "sdk-go-load-prompt"
)

func testAPI(t *testing.T) *api.API {
	t.Helper()
	return api.NewWithHTTPSClient(vcr.GetHTTPSClient(t))
}

func TestLoad_BySlug(t *testing.T) {
	p, err := prompt.Load(context.Background(), testAPI(t), prompt.LoadOpts{
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

	found, err := prompt.Load(context.Background(), client, prompt.LoadOpts{
		Slug:    testSlug,
		Project: testProject,
	})
	require.NoError(t, err)

	p, err := prompt.Load(context.Background(), client, prompt.LoadOpts{ID: found.ID})
	require.NoError(t, err)
	assert.Equal(t, found.ID, p.ID)
	assert.Equal(t, "gpt-4o-mini", p.Model())
}

func TestLoad_NotFound(t *testing.T) {
	_, err := prompt.Load(context.Background(), testAPI(t), prompt.LoadOpts{
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
		_, err := prompt.Load(ctx, nil, prompt.LoadOpts{Slug: "x", Project: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "API client")
	})

	t.Run("needs a slug or an ID", func(t *testing.T) {
		_, err := prompt.Load(ctx, &api.API{}, prompt.LoadOpts{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Slug")
	})

	t.Run("needs a project", func(t *testing.T) {
		_, err := prompt.Load(ctx, &api.API{}, prompt.LoadOpts{Slug: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project")
	})
}
