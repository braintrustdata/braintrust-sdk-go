package experiments

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const integrationTestProject = "go-sdk-tests"

// createTestProject creates a test project for experiment tests
func createTestProject(t *testing.T, client *https.Client) *projects.Project {
	t.Helper()
	ctx := context.Background()
	projectsAPI := projects.New(client)
	project, err := projectsAPI.Create(ctx, projects.CreateParams{
		Name: integrationTestProject,
	})
	require.NoError(t, err)
	return project
}

// TestExperiments_Create_Integration tests creating an experiment
func TestExperiments_Create_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project first
	project := createTestProject(t, client)

	// Create an experiment
	experiment, err := api.Create(ctx, CreateParams{
		ProjectID:   project.ID,
		Name:        "test-experiment",
		Description: "Test experiment for integration tests",
		Tags:        []string{"test", "integration"},
		Metadata: map[string]interface{}{
			"purpose": "testing",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, experiment)
	assert.NotEmpty(t, experiment.ID)
	assert.Equal(t, project.ID, experiment.ProjectID)
	assert.NotEmpty(t, experiment.Name)
	assert.Equal(t, "Test experiment for integration tests", experiment.Description)
	assert.Contains(t, experiment.Tags, "test")
}

// TestExperiments_Register_Integration tests registering an experiment
func TestExperiments_Register_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project first
	project := createTestProject(t, client)

	// Register an experiment
	experiment, err := api.Register(ctx, "test-experiment-register", project.ID, RegisterOpts{
		Tags: []string{"test"},
		Metadata: map[string]interface{}{
			"purpose": "testing",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, experiment)
	assert.NotEmpty(t, experiment.ID)
	assert.Equal(t, project.ID, experiment.ProjectID)
	assert.Contains(t, experiment.Name, "test-experiment-register")
}

// TestExperiments_List_Integration tests listing experiments
func TestExperiments_List_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and experiment
	project := createTestProject(t, client)
	_, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-experiment-list",
	})
	require.NoError(t, err)

	// List experiments
	response, err := api.List(ctx, ListParams{
		ProjectID: project.ID,
		Limit:     10,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.NotEmpty(t, response.Objects)

	// Verify at least one experiment is returned
	assert.GreaterOrEqual(t, len(response.Objects), 1)

	// Verify structure of returned experiments
	for _, exp := range response.Objects {
		assert.NotEmpty(t, exp.ID)
		assert.NotEmpty(t, exp.Name)
		assert.Equal(t, project.ID, exp.ProjectID)
	}
}

// TestExperiments_Get_Integration tests getting an experiment by ID
func TestExperiments_Get_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and experiment
	project := createTestProject(t, client)
	created, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-experiment-list",
	})
	require.NoError(t, err)

	// Get the experiment by ID
	experiment, err := api.Get(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, experiment)
	assert.Equal(t, created.ID, experiment.ID)
	assert.Equal(t, created.Name, experiment.Name)
	assert.Equal(t, created.ProjectID, experiment.ProjectID)
}

// TestExperiments_Delete_Integration tests deleting an experiment
func TestExperiments_Delete_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and experiment
	project := createTestProject(t, client)
	experiment, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-experiment-list",
	})
	require.NoError(t, err)

	// Delete the experiment
	err = api.Delete(ctx, experiment.ID)
	require.NoError(t, err)
}

// TestExperiments_FullLifecycle tests the complete experiment lifecycle
func TestExperiments_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Step 1: Create a project
	project := createTestProject(t, client)

	// Step 2: Create an experiment
	created, err := api.Create(ctx, CreateParams{
		ProjectID:   project.ID,
		Name:        "lifecycle-exp",
		Description: "Test experiment for lifecycle testing",
		Tags:        []string{"lifecycle", "test"},
		Metadata: map[string]interface{}{
			"stage": "created",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Contains(t, created.Name, "lifecycle-exp")
	assert.Equal(t, project.ID, created.ProjectID)
	assert.Equal(t, "Test experiment for lifecycle testing", created.Description)

	// Step 3: Verify experiment exists via Get
	retrieved, err := api.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, retrieved.ID)
	assert.Equal(t, created.Name, retrieved.Name)
	assert.Equal(t, created.ProjectID, retrieved.ProjectID)

	// Step 4: Verify experiment appears in List
	listResponse, err := api.List(ctx, ListParams{
		ProjectID: project.ID,
		Limit:     100,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, listResponse.Objects)

	found := false
	for _, exp := range listResponse.Objects {
		if exp.ID == created.ID {
			found = true
			assert.Contains(t, exp.Name, "lifecycle-exp")
			break
		}
	}
	assert.True(t, found, "Created experiment should appear in List results")

	// Step 5: Idempotent create - creating with same name returns same experiment
	idempotent, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "lifecycle-exp",
	})
	require.NoError(t, err)
	assert.Equal(t, created.ID, idempotent.ID, "Creating experiment with same name should be idempotent")
	assert.Equal(t, created.Name, idempotent.Name)

	// Step 6: Delete the experiment
	err = api.Delete(ctx, created.ID)
	require.NoError(t, err)

	// Step 7: Verify experiment no longer exists
	_, err = api.Get(ctx, created.ID)
	require.Error(t, err, "Should error when trying to get deleted experiment")
}

// TestExperiments_CreateParams_Validation tests parameter validation
func TestExperiments_CreateParams_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test missing required field - ProjectID
	_, err := api.Create(ctx, CreateParams{
		Name: "test-experiment-validation",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestExperiments_RegisterParams_Validation tests Register parameter validation
func TestExperiments_RegisterParams_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project
	project := createTestProject(t, client)

	// Test missing required field - Name
	_, err := api.Register(ctx, "", project.ID, RegisterOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	// Test missing required field - ProjectID
	_, err = api.Register(ctx, "test-name", "", RegisterOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestExperiments_Get_Validation tests Get parameter validation
func TestExperiments_Get_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test empty experiment ID
	_, err := api.Get(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestExperiments_Delete_Validation tests Delete parameter validation
func TestExperiments_Delete_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test empty experiment ID
	err := api.Delete(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestExperiments_EnsureNew tests the EnsureNew parameter
func TestExperiments_EnsureNew(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project
	project := createTestProject(t, client)

	// Create an experiment with a specific name
	first, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "ensure-new-test",
	})
	require.NoError(t, err)

	// Create another experiment with the same name but EnsureNew=true
	second, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "ensure-new-test",
		EnsureNew: true,
	})
	require.NoError(t, err)

	// IDs should be different
	assert.NotEqual(t, first.ID, second.ID, "EnsureNew should create a new experiment with different ID")

	// Names should start with the same prefix (API may append suffix to avoid conflicts)
	assert.Contains(t, second.Name, "ensure-new-test", "EnsureNew experiment name should contain original name")
}
