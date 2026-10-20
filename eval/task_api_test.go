package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// TestTaskAPI_Get tests loading a task/prompt by slug
func TestTaskAPI_Get(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	apiClient := createIntegrationTestAPIClient(t)

	// Register project
	project, err := decodeFixtureResponse(apiClient.PostProject(ctx, openapi.CreateProject{Name: integrationTestProject}))
	require.NoError(t, err)

	testSlug := tests.Name(t, "slug")

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := decodeFixtureObjects(apiClient.GetFunction(ctx, &openapi.GetFunctionParams{ProjectName: https.String(integrationTestProject), Slug: https.String(testSlug), Limit: https.Positive(1)})); len(existing) > 0 {
		_ = closeFixtureResponse(apiClient.DeleteFunctionId(ctx, existing[0].ID))
	}

	// Create a test function/prompt
	function, err := decodeFixtureResponse(apiClient.PostFunctionWithBody(ctx, "application/json", fixtureBody(t, map[string]any{"project_id": project.ID, "name": "Test Task", "slug": testSlug, "function_type": "prompt", "function_data": map[string]any{
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
	}})))
	require.NoError(t, err)
	require.NotNil(t, function)

	// Defer cleanup
	defer func() {
		_ = closeFixtureResponse(apiClient.DeleteFunctionId(ctx, function.ID))
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
	foundFunctions, err := decodeFixtureObjects(apiClient.GetFunction(ctx, &openapi.GetFunctionParams{ProjectName: https.String(integrationTestProject), Slug: https.String(testSlug)}))
	require.NoError(t, err)
	require.Len(t, foundFunctions, 1)
	assert.Equal(t, testSlug, foundFunctions[0].Slug)

	// Test: Delete the function
	err = closeFixtureResponse(apiClient.DeleteFunctionId(ctx, function.ID))
	require.NoError(t, err)

	// Test: Verify it's deleted - should not be found
	_, err = functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.Error(t, err)

}

// TestTaskAPI_Get_EmptySlug tests error handling
func TestTaskAPI_Get_EmptySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Get API credentials and create API client

	apiClient := &openapi.Client{}
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on empty slug
	_, err := functionsAPI.Task(ctx, FunctionOpts{Slug: ""})
	require.Error(t, err)

}

// TestTaskAPI_Get_ReturnsCallableTask tests that returned TaskFunc is executable
func TestTaskAPI_Get_ReturnsCallableTask(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	apiClient := createIntegrationTestAPIClient(t)

	// Register project
	project, err := decodeFixtureResponse(apiClient.PostProject(ctx, openapi.CreateProject{Name: integrationTestProject}))
	require.NoError(t, err)

	testSlug := tests.Name(t, "callable")

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := decodeFixtureObjects(apiClient.GetFunction(ctx, &openapi.GetFunctionParams{ProjectName: https.String(integrationTestProject), Slug: https.String(testSlug), Limit: https.Positive(1)})); len(existing) > 0 {
		_ = closeFixtureResponse(apiClient.DeleteFunctionId(ctx, existing[0].ID))
	}

	// Create a test function that echoes back the input question
	function, err := decodeFixtureResponse(apiClient.PostFunctionWithBody(ctx, "application/json", fixtureBody(t, map[string]any{
		"project_id": project.ID,
		"name":       "Echo Task",
		"slug":       testSlug,
		"function_data": map[string]any{
			"type": "prompt",
		},
		"prompt_data": map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "system", "content": `Return a JSON object with an "answer" field containing the user's message exactly. Do not answer the question.`},
					{"role": "user", "content": "{{input.question}}"},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature":     0,
					"max_tokens":      100,
					"response_format": map[string]any{"type": "json_object"},
				},
			},
		},
	})))
	require.NoError(t, err)
	require.NotNil(t, function)

	// Defer cleanup
	defer func() {
		_ = closeFixtureResponse(apiClient.DeleteFunctionId(ctx, function.ID))
	}()

	// Create FunctionsAPI and get the task
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Test: Call the task function
	input := testDatasetInput{Question: "What is 2+2?"}
	output, err := task(ctx, input, &TaskHooks{})
	require.NoError(t, err)

	assert.Equal(t, input.Question, output.Value.Answer)
}

// TestTaskAPI_TypeSafety verifies compile-time type safety
func TestTaskAPI_TypeSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Get API credentials and create API client

	apiClient := &openapi.Client{}
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
func createIntegrationTestAPIClient(t *testing.T) *openapi.Client {
	t.Helper()

	// Get HTTPS client with VCR support
	client := vcr.GetHTTPSClient(t)

	// Create API client with the VCR-wrapped client
	return https.NewGenerated(client)
}

// setupIntegrationTest creates a session and API client for integration tests.
// With VCR support, these tests can now run without BRAINTRUST_API_KEY by using recorded cassettes.
// Returns both the session (for auth) and a VCR-wrapped API client (for all API calls).
func setupIntegrationTest(t *testing.T) (*auth.Session, *openapi.Client) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Get VCR-wrapped HTTPS client
	httpsClient := vcr.GetHTTPSClient(t)

	ctx := context.Background()
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey: httpsClient.APIKey(),
		AppURL: "https://www.braintrust.dev",
		Logger: logger.Discard(),
		Client: httpsClient,
	})
	require.NoError(t, err)

	// Create VCR-wrapped API client
	apiClient := https.NewGenerated(httpsClient)

	return session, apiClient
}
