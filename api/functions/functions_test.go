package functions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

const integrationTestProject = "go-sdk-tests"

// createTestProject creates a test project for function tests
func createTestProject(t *testing.T) *projects.Project {
	t.Helper()
	ctx := context.Background()
	client := tests.GetTestHTTPSClient(t)
	projectsAPI := projects.New(client)
	project, err := projectsAPI.Create(ctx, projects.CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)
	return project
}

// TestFunctions_Create_Integration tests creating a function
func TestFunctions_Create_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project first
	project := createTestProject(t)

	// Create a function
	function, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      tests.RandomName(t, "test-function"),
		Slug:      tests.RandomName(t, "test-func"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, function)
	assert.NotEmpty(t, function.ID)
	assert.Equal(t, project.ID, function.ProjectID)
	assert.NotEmpty(t, function.Name)
	assert.NotEmpty(t, function.Slug)
}

// TestFunctions_Query_Integration tests querying functions
func TestFunctions_Query_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project and function
	project := createTestProject(t)
	slug := tests.RandomName(t, "test-func")
	created, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      tests.RandomName(t, "test-function"),
		Slug:      slug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.NoError(t, err)

	// Query by project name and slug
	functions, err := api.Query(ctx, QueryParams{
		ProjectName: integrationTestProject,
		Slug:        slug,
		Limit:       1,
	})
	require.NoError(t, err)
	require.Len(t, functions, 1)
	assert.Equal(t, created.ID, functions[0].ID)
	assert.Equal(t, created.Name, functions[0].Name)
	assert.Equal(t, created.Slug, functions[0].Slug)
}

// TestFunctions_Delete_Integration tests deleting a function
func TestFunctions_Delete_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project and function
	project := createTestProject(t)
	function, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      tests.RandomName(t, "test-function"),
		Slug:      tests.RandomName(t, "test-func"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.NoError(t, err)

	// Delete the function
	err = api.Delete(ctx, function.ID)
	require.NoError(t, err)
}

// TestFunctions_Invoke_Integration tests invoking a function
func TestFunctions_Invoke_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project and prompt function
	project := createTestProject(t)
	function, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      tests.RandomName(t, "test-prompt"),
		Slug:      tests.RandomName(t, "test-prompt"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are a helpful assistant. Respond with valid JSON in the format: {\"answer\": \"your answer here\"}",
					},
					{
						"role":    "user",
						"content": "{{input.question}}",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature":     0,
					"max_tokens":      50,
					"response_format": map[string]any{"type": "json_object"},
				},
			},
		},
	})
	require.NoError(t, err)

	// Invoke the function
	output, err := api.Invoke(ctx, function.ID, map[string]any{
		"question": "What is 2+2?",
	})
	require.NoError(t, err)
	require.NotNil(t, output)
}

// TestFunctions_FullLifecycle tests the complete function lifecycle
func TestFunctions_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Step 1: Create a project
	project := createTestProject(t)

	// Step 2: Create a function with unique slug
	slug := tests.RandomName(t, "lifecycle-func")
	created, err := api.Create(ctx, CreateParams{
		ProjectID:   project.ID,
		Name:        tests.RandomName(t, "lifecycle-function"),
		Slug:        slug,
		Description: "Test function for lifecycle testing",
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, slug, created.Slug)
	assert.Equal(t, project.ID, created.ProjectID)

	// Step 3: Verify function exists via Query
	functions, err := api.Query(ctx, QueryParams{
		ProjectName: integrationTestProject,
		Slug:        slug,
		Limit:       1,
	})
	require.NoError(t, err)
	require.Len(t, functions, 1)
	assert.Equal(t, created.ID, functions[0].ID)
	assert.Equal(t, created.Name, functions[0].Name)

	// Step 4: Query by project ID
	byProjectID, err := api.Query(ctx, QueryParams{
		ProjectID: project.ID,
		Limit:     100,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, byProjectID)
	found := false
	for _, fn := range byProjectID {
		if fn.ID == created.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Created function should appear in project query")

	// Step 5: Delete the function
	err = api.Delete(ctx, created.ID)
	require.NoError(t, err)

	// Step 6: Verify function no longer exists
	afterDelete, err := api.Query(ctx, QueryParams{
		ProjectName: integrationTestProject,
		Slug:        slug,
		Limit:       1,
	})
	require.NoError(t, err)
	assert.Empty(t, afterDelete, "Function should not exist after deletion")
}

// TestFunctions_CreateParams_Validation tests parameter validation
func TestFunctions_CreateParams_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project
	project := createTestProject(t)

	// Test missing required fields - ProjectID
	_, err := api.Create(ctx, CreateParams{
		Name: tests.RandomName(t, "test-function"),
		Slug: tests.RandomName(t, "test-func"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	// Test missing required fields - Name
	_, err = api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Slug:      tests.RandomName(t, "test-func"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	// Test missing required fields - Slug
	_, err = api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      tests.RandomName(t, "test-function"),
		FunctionData: map[string]any{
			"type": "prompt",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestFunctions_Delete_Validation tests Delete parameter validation
func TestFunctions_Delete_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Test empty function ID
	err := api.Delete(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestFunctions_Invoke_Validation tests Invoke parameter validation
func TestFunctions_Invoke_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Test empty function ID
	_, err := api.Invoke(ctx, "", map[string]any{"input": "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
