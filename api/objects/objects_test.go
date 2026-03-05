package objects

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

const integrationTestProject = "go-sdk-tests"

func TestObjects_Fetch_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset with events
	projectsAPI := projects.New(client)
	project, err := projectsAPI.Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	datasetsAPI := datasets.New(client)
	dataset, err := datasetsAPI.Create(ctx, datasets.CreateParams{
		ProjectID: project.ID,
		Name:      "test-objects-fetch",
	})
	require.NoError(t, err)
	defer func() { _ = datasetsAPI.Delete(ctx, dataset.ID) }()

	err = datasetsAPI.InsertEvents(ctx, dataset.ID, []datasets.Event{
		{Input: map[string]any{"q": "1"}, Expected: map[string]any{"a": "1"}},
		{Input: map[string]any{"q": "2"}, Expected: map[string]any{"a": "2"}},
	})
	require.NoError(t, err)

	// Fetch via the generic objects API (retry for eventual consistency)
	var rows []map[string]any
	for i := 0; i < 3; i++ {
		resp, err := api.Fetch(ctx, "dataset", dataset.ID, FetchParams{Limit: 10})
		require.NoError(t, err)
		require.NotNil(t, resp)

		rows = resp.Events
		if len(rows) == 0 {
			rows = resp.Rows
		}
		if len(rows) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, len(rows), 2)
}

func TestObjects_Fetch_Validation(t *testing.T) {
	t.Parallel()

	api := New(https.NewClient("test-key", "https://example.com", logger.Discard()))

	_, err := api.Fetch(context.Background(), "", "obj-1", FetchParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "object type is required")

	_, err = api.Fetch(context.Background(), "experiment", "", FetchParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "object ID is required")
}
