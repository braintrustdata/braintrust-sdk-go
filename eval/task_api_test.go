package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// TestTaskAPI_Get tests loading a task/prompt by slug
func TestTaskAPI_Get(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Register(ctx, integrationTestProject)
	require.NoError(t, err)

	testSlug := "test-task-api-get"

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a test function/prompt
	function, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID:    project.ID,
		Name:         "Test Task",
		Slug:         testSlug,
		FunctionType: "prompt",
		FunctionData: map[string]any{
			"type": "prompt",
			"prompt": map[string]any{
				"type": "completion",
				"messages": []map[string]any{
					{
						"role":    "user",
						"content": "Test prompt",
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, function)

	// Defer cleanup
	defer func() {
		_ = functions.Delete(ctx, function.ID)
	}()

	// Create TaskAPI
	taskAPI := &TaskAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Test: Get should return a TaskFunc
	task, err := taskAPI.Get(ctx, testSlug)
	require.NoError(t, err)
	require.NotNil(t, task)

	// Verify it returns a TaskFunc[I, R]
	var _ = task

	// Test: Query should find the function
	foundFunctions, err := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
	})
	require.NoError(t, err)
	require.Len(t, foundFunctions, 1)
	assert.Equal(t, testSlug, foundFunctions[0].Slug)

	// Test: Delete the function
	err = functions.Delete(ctx, function.ID)
	require.NoError(t, err)

	// Test: Verify it's deleted - should not be found
	_, err = taskAPI.Get(ctx, testSlug)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestTaskAPI_Get_EmptySlug tests error handling
func TestTaskAPI_Get_EmptySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	taskAPI := &TaskAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on empty slug
	_, err = taskAPI.Get(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestTaskAPI_Get_ReturnsCallableTask tests that returned TaskFunc is executable
func TestTaskAPI_Get_ReturnsCallableTask(t *testing.T) {
	t.Skip("TODO: Implement with real function")
}

// TestTaskAPI_Query tests querying tasks with options
func TestTaskAPI_Query(t *testing.T) {
	t.Skip("TODO: Implement Query method")
}

// TestTaskAPI_TypeSafety verifies compile-time type safety
func TestTaskAPI_TypeSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// This should compile
	taskAPI := &TaskAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// The returned TaskFunc should have the correct type
	var _ = func() (TaskFunc[testDatasetInput, testDatasetOutput], error) {
		return taskAPI.Get(ctx, "test-slug")
	}

	// This is a compile-time check - if it compiles, the test passes
	assert.NotNil(t, taskAPI)
}

// Helper functions

const integrationTestProject = "go-sdk-tests"

// createIntegrationTestSession creates a real session for integration tests.
// It loads config from environment variables and fails if BRAINTRUST_API_KEY is not set.
func createIntegrationTestSession(t *testing.T) *auth.Session {
	t.Helper()

	// Load config from environment
	cfg := config.FromEnv()
	if cfg.APIKey == "" {
		t.Fatal("BRAINTRUST_API_KEY not set, cannot run integration test")
	}

	ctx := context.Background()
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey:  cfg.APIKey,
		AppURL:  cfg.AppURL,
		APIURL:  cfg.APIURL,
		OrgName: cfg.OrgName,
		Logger:  logger.Discard(),
	})
	require.NoError(t, err)

	return session
}
