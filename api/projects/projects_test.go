package projects

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

const integrationTestProject = "go-sdk-tests"

// TestProjects_Create_Integration tests creating a project
func TestProjects_Create_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Create a project
	project, err := api.Create(ctx, CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)
	require.NotNil(t, project)
	assert.Equal(t, integrationTestProject, project.Name)
	assert.NotEmpty(t, project.ID)
	assert.NotEmpty(t, project.OrgID)
}

// TestProjects_Get_Integration tests getting a project by ID
func TestProjects_Get_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// First create a project
	created, err := api.Create(ctx, CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)
	require.NotNil(t, created)

	// Get the project by ID
	project, err := api.Get(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, project)
	assert.Equal(t, created.ID, project.ID)
	assert.Equal(t, created.Name, project.Name)
	assert.Equal(t, created.OrgID, project.OrgID)
}

// TestProjects_List_Integration tests listing projects
func TestProjects_List_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// First create a project to ensure we have at least one
	_, err := api.Create(ctx, CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)

	// List projects
	response, err := api.List(ctx, ListParams{
		Limit: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.NotEmpty(t, response.Objects)

	// Verify at least one project is returned
	assert.GreaterOrEqual(t, len(response.Objects), 1)

	// Verify structure of returned projects
	for _, project := range response.Objects {
		assert.NotEmpty(t, project.ID)
		assert.NotEmpty(t, project.Name)
		assert.NotEmpty(t, project.OrgID)
	}
}

// TestProjects_FullLifecycle tests the complete project lifecycle
func TestProjects_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Step 1: Create a project with unique name
	projectName := "go-sdk-lifecycle-test-" + fmt.Sprintf("%d", time.Now().Unix())
	created, err := api.Create(ctx, CreateParams{
		Name: projectName,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, projectName, created.Name)
	assert.NotEmpty(t, created.OrgID)

	// Clean up: Delete the project when test completes
	defer func() {
		err := api.Delete(ctx, created.ID)
		if err != nil {
			t.Logf("Failed to delete test project %s: %v", created.ID, err)
		}
	}()

	// Step 2: Verify project exists via Get
	retrieved, err := api.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, retrieved.ID)
	assert.Equal(t, created.Name, retrieved.Name)
	assert.Equal(t, created.OrgID, retrieved.OrgID)

	// Step 3: Verify project appears in List
	listResponse, err := api.List(ctx, ListParams{
		Limit: 100, // Get enough to find our project
	})
	require.NoError(t, err)
	assert.NotEmpty(t, listResponse.Objects)

	found := false
	for _, project := range listResponse.Objects {
		if project.ID == created.ID {
			found = true
			assert.Equal(t, projectName, project.Name)
			assert.Equal(t, created.OrgID, project.OrgID)
			break
		}
	}
	assert.True(t, found, "Created project should appear in List results")

	// Step 4: Idempotent create - creating with same name returns same project
	idempotent, err := api.Create(ctx, CreateParams{
		Name: projectName,
	})
	require.NoError(t, err)
	assert.Equal(t, created.ID, idempotent.ID, "Creating project with same name should be idempotent")
	assert.Equal(t, created.Name, idempotent.Name)
}

// TestProjects_CreateParams_Validation tests parameter validation
func TestProjects_CreateParams_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Test empty name
	_, err := api.Create(ctx, CreateParams{
		Name: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestProjects_Get_Validation tests Get parameter validation
func TestProjects_Get_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Test empty ID
	_, err := api.Get(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestProjects_List_WithOrgID tests listing projects filtered by organization
func TestProjects_List_WithOrgID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// First create a project to get an orgID
	project, err := api.Create(ctx, CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)
	require.NotEmpty(t, project.OrgID)

	// List projects filtered by OrgID
	response, err := api.List(ctx, ListParams{
		OrgID: project.OrgID,
		Limit: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.NotEmpty(t, response.Objects)

	// Verify all returned projects belong to the same org
	for _, p := range response.Objects {
		assert.Equal(t, project.OrgID, p.OrgID)
	}
}

// TestProjects_Get_NotFound tests getting a non-existent project
func TestProjects_Get_NotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := tests.GetTestHTTPSClient(t)
	api := New(client)

	// Try to get a non-existent project
	_, err := api.Get(ctx, "non-existent-project-id-12345")
	require.Error(t, err)
}
