package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/api/prompts"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

func compatibilityClient(t *testing.T) *api.API {
	t.Helper()
	return api.NewWithHTTPSClient(vcr.GetHTTPSClient(t))
}

func TestCompatibilityValidation(t *testing.T) {
	// Legacy validation still rejects missing IDs before using a transport.
	_, err := api.NewWithHTTPSClient(nil).Projects().Get(context.Background(), "")
	require.Error(t, err)
	_, err = api.NewWithHTTPSClient(nil).Prompts().Get(context.Background(), "", prompts.QueryParams{})
	require.Error(t, err)
}

func TestCompatibilityCRUDPassthrough(t *testing.T) {
	client := compatibilityClient(t)
	ctx := context.Background()
	created, err := client.Projects().Create(ctx, projects.CreateParams{Name: "go-sdk-compat-lifecycle-test"})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	got, err := client.Projects().Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created, got)
	// The legacy list has no name filter. Request only the newest project, which
	// is the one just created, so the cassette never records other projects.
	listed, err := client.Projects().List(ctx, projects.ListParams{OrgID: created.OrgID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, listed.Objects, 1)
	require.Equal(t, created.ID, listed.Objects[0].ID)
	require.NoError(t, client.Projects().Delete(ctx, created.ID))
}

func TestCompatibilityRegisterAndErrors(t *testing.T) {
	t.Run("register convenience", func(t *testing.T) {
		client := compatibilityClient(t)
		ctx := context.Background()
		project, err := client.Projects().Create(ctx, projects.CreateParams{Name: "go-sdk-tests"})
		require.NoError(t, err)
		experiment, err := client.Experiments().Register(ctx, "test-experiment-register", project.ID, experiments.RegisterOpts{
			Tags: []string{"test"}, Metadata: map[string]any{"purpose": "testing"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, experiment.ID)
		require.Equal(t, project.ID, experiment.ProjectID)
		require.Contains(t, experiment.Name, "test-experiment-register")
	})
	t.Run("server error", func(t *testing.T) {
		client := compatibilityClient(t)
		_, err := client.Projects().Get(context.Background(), "non-existent-project-id-12345")
		var httpError *https.HTTPError
		require.ErrorAs(t, err, &httpError)
		require.Equal(t, http.StatusBadRequest, httpError.StatusCode)
		require.Contains(t, httpError.Body, `"validation": "uuid"`)
	})
}
