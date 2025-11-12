package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// TestTaskAPI_Get tests loading a task/prompt by slug
func TestTaskAPI_Get(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	apiClient := createIntegrationTestAPIClient(t)
	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.Name(t, "slug")

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a test function/prompt
	function, err := functions.Create(ctx, functionsapi.CreateParams{
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

	// Create FunctionsAPI
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Test: Task should return a TaskFunc
	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Verify it returns a TaskFunc[I, R]
	var _ = task

	// Test: Query should find the function
	foundFunctions, err := functions.Query(ctx, functionsapi.QueryParams{
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
	_, err = functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestTaskAPI_Get_EmptySlug tests error handling
func TestTaskAPI_Get_EmptySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get API credentials and create API client
	apiInfo := session.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL))
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on empty slug
	_, err := functionsAPI.Task(ctx, FunctionOpts{Slug: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestTaskAPI_Get_ReturnsCallableTask tests that returned TaskFunc is executable
func TestTaskAPI_Get_ReturnsCallableTask(t *testing.T) {
	t.Skip("TODO: Implement with real function")
}

// TestTaskAPI_TypeSafety verifies compile-time type safety
func TestTaskAPI_TypeSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get API credentials and create API client
	apiInfo := session.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL))
	// This should compile
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// The returned TaskFunc should have the correct type
	var _ = func() (TaskFunc[testDatasetInput, testDatasetOutput], error) {
		return functionsAPI.Task(ctx, FunctionOpts{Slug: "test-slug"})
	}

	// This is a compile-time check - if it compiles, the test passes
	assert.NotNil(t, functionsAPI)
}

// Helper functions

const integrationTestProject = "go-sdk-tests"

// createIntegrationTestAPIClient creates an API client for integration tests with VCR support.
// This enables tests to run without requiring BRAINTRUST_API_KEY by using recorded cassettes.
func createIntegrationTestAPIClient(t *testing.T) *api.API {
	t.Helper()

	// Get HTTPS client with VCR support
	client := vcr.GetHTTPSClient(t)

	// Create API client with the VCR-wrapped client
	return api.NewWithHTTPSClient(client)
}

// setupIntegrationTest creates a session and API client for integration tests.
// With VCR support, these tests can now run without BRAINTRUST_API_KEY by using recorded cassettes.
// Returns both the session (for auth) and a VCR-wrapped API client (for all API calls).
func setupIntegrationTest(t *testing.T) (*auth.Session, *api.API) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Get VCR-wrapped HTTPS client
	httpsClient := vcr.GetHTTPSClient(t)

	ctx := context.Background()
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey: vcr.GetAPIKeyForVCR(t),
		AppURL: "https://www.braintrust.dev",
		Logger: logger.Discard(),
		Client: httpsClient,
	})
	require.NoError(t, err)

	// Create VCR-wrapped API client
	apiClient := api.NewWithHTTPSClient(httpsClient)

	return session, apiClient
}
