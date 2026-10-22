package openapi_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/oapi-codegen/nullable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func TestGeneratedAutomationLifecycle(t *testing.T) {
	t.Parallel()
	key := os.Getenv("BRAINTRUST_API_KEY")
	if vcr.GetVCRMode() != vcr.ModeReplay {
		require.NotEmpty(t, key, "BRAINTRUST_API_KEY is required for recording")
	}
	client, err := openapi.NewClientWithResponses("https://api.braintrust.dev",
		openapi.WithHTTPClient(vcr.NewHTTPClient(t)),
		openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+key)
			return nil
		}))
	require.NoError(t, err)
	ctx := context.Background()
	project, err := client.PostProjectWithResponse(ctx, openapi.CreateProject{Name: "go-sdk-tests"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, project.StatusCode(), string(project.Body))
	require.NotNil(t, project.JSON200)

	var action openapi.CreateProjectAutomation_Config_0_Action
	require.NoError(t, action.FromCreateProjectAutomationConfig0Action0(openapi.CreateProjectAutomationConfig0Action0{
		Type: openapi.CreateProjectAutomationConfig0Action0TypeWebhook,
		Url:  "https://example.com/braintrust-hook",
	}))
	paused := openapi.AutomationStatusPaused
	var config openapi.CreateProjectAutomation_Config
	require.NoError(t, config.FromCreateProjectAutomationConfig0(openapi.CreateProjectAutomationConfig0{
		EventType: openapi.CreateProjectAutomationConfig0EventTypeLogs,
		Status:    &paused, BtqlFilter: "error IS NOT NULL", IntervalSeconds: 3600, Action: action,
	}))
	created, err := client.PostProjectAutomationWithResponse(ctx, openapi.CreateProjectAutomation{
		ProjectId:   project.JSON200.Id,
		Name:        "go-sdk-automation-test-" + rand.Text(),
		Description: nullable.NewNullableWithValue("Paused SDK integration test"),
		Config:      config,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, created.StatusCode(), string(created.Body))
	require.NotNil(t, created.JSON200)
	id := created.JSON200.Id.String()
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			response, err := client.DeleteProjectAutomationIdWithResponse(cleanupCtx, id)
			if assert.NoError(t, err, "clean up automation %s", id) {
				assert.Equal(t, http.StatusOK, response.StatusCode(), string(response.Body))
			}
		}
	})
	assert.Equal(t, project.JSON200.Id, created.JSON200.ProjectId)
	assert.Contains(t, created.JSON200.Name, "go-sdk-automation-test-")
	assertGeneratedAutomation(t, created.JSON200, "Paused SDK integration test", "error IS NOT NULL")

	fetched, err := client.GetProjectAutomationIdWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fetched.StatusCode(), string(fetched.Body))
	require.NotNil(t, fetched.JSON200)
	assert.Equal(t, created.JSON200.Id, fetched.JSON200.Id)
	assertGeneratedAutomation(t, fetched.JSON200, "Paused SDK integration test", "error IS NOT NULL")

	var patchAction openapi.PatchProjectAutomation_Config_0_Action
	require.NoError(t, patchAction.FromPatchProjectAutomationConfig0Action0(openapi.PatchProjectAutomationConfig0Action0{
		Type: openapi.PatchProjectAutomationConfig0Action0TypeWebhook,
		Url:  "https://example.com/braintrust-hook",
	}))
	filter := "error IS NOT NULL AND metadata.environment = 'production'"
	var patchConfig openapi.PatchProjectAutomation_Config
	require.NoError(t, patchConfig.FromPatchProjectAutomationConfig0(openapi.PatchProjectAutomationConfig0{
		EventType: openapi.PatchProjectAutomationConfig0EventTypeLogs,
		Status:    &paused, BtqlFilter: filter, IntervalSeconds: 3600, Action: patchAction,
	}))
	description := "Updated paused SDK integration test"
	updated, err := client.PatchProjectAutomationIdWithResponse(ctx, id, openapi.PatchProjectAutomation{
		Description: nullable.NewNullableWithValue(description), Config: &patchConfig,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updated.StatusCode(), string(updated.Body))
	require.NotNil(t, updated.JSON200)
	assert.Equal(t, created.JSON200.Name, updated.JSON200.Name, "omitted name is preserved")
	assertGeneratedAutomation(t, updated.JSON200, description, filter)

	fetched, err = client.GetProjectAutomationIdWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fetched.StatusCode(), string(fetched.Body))
	require.NotNil(t, fetched.JSON200)
	assertGeneratedAutomation(t, fetched.JSON200, description, filter)

	// Explicit empty description clears it, while omitted config/name survive.
	cleared, err := client.PatchProjectAutomationIdWithResponse(ctx, id, openapi.PatchProjectAutomation{
		Description: nullable.NewNullableWithValue(""),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cleared.StatusCode(), string(cleared.Body))
	require.NotNil(t, cleared.JSON200)
	assert.Equal(t, created.JSON200.Name, cleared.JSON200.Name)
	assertGeneratedAutomation(t, cleared.JSON200, "", filter)
	fetched, err = client.GetProjectAutomationIdWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fetched.StatusCode(), string(fetched.Body))
	require.NotNil(t, fetched.JSON200)
	assertGeneratedAutomation(t, fetched.JSON200, "", filter)

	removed, err := client.DeleteProjectAutomationIdWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, removed.StatusCode(), string(removed.Body))
	deleted = true
	missing, err := client.GetProjectAutomationIdWithResponse(ctx, id)
	// Generated clients return HTTP errors as responses, not Go errors.
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, missing.StatusCode())
	assert.Nil(t, missing.JSON200)
}

func assertGeneratedAutomation(t *testing.T, automation *openapi.ProjectAutomation, description, filter string) {
	t.Helper()
	assert.Equal(t, description, automation.Description.GetOrEmpty())
	config, err := automation.Config.AsProjectAutomationConfig0()
	require.NoError(t, err)
	assert.Equal(t, filter, config.BtqlFilter)
	require.NotNil(t, config.Status)
	assert.Equal(t, openapi.AutomationStatusPaused, *config.Status)
	assert.Equal(t, float32(3600), config.IntervalSeconds)
	action, err := config.Action.AsProjectAutomationConfig0Action0()
	require.NoError(t, err)
	assert.Equal(t, openapi.ProjectAutomationConfig0Action0TypeWebhook, action.Type)
	assert.Equal(t, "https://example.com/braintrust-hook", action.Url)
}
