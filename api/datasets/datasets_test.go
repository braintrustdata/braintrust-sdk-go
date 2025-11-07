package datasets

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const integrationTestProject = "go-sdk-tests"

// createTestProject creates a test project for dataset tests
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

// TestDatasets_Create_Integration tests creating a dataset
func TestDatasets_Create_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project first (using same VCR client)
	project := createTestProject(t, client)

	// Create a dataset
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID:   project.ID,
		Name:        "test-dataset",
		Description: "Test dataset for integration tests",
	})
	require.NoError(t, err)
	require.NotNil(t, dataset)
	assert.NotEmpty(t, dataset.ID)
	assert.Equal(t, project.ID, dataset.ProjectID)
	assert.NotEmpty(t, dataset.Name)
}

// TestDatasets_InsertEvents_Integration tests inserting events
func TestDatasets_InsertEvents_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-query",
	})
	require.NoError(t, err)

	// Insert events using convenience function
	events := []Event{
		{
			Input: map[string]interface{}{
				"question": "What is 2+2?",
			},
			Expected: map[string]interface{}{
				"answer": "4",
			},
			Tags: []string{"math", "easy"},
		},
		{
			Input: map[string]interface{}{
				"question": "What is the capital of France?",
			},
			Expected: map[string]interface{}{
				"answer": "Paris",
			},
			Tags: []string{"geography"},
		},
	}

	err = api.InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)
}

// TestDatasets_Insert_Integration tests inserting events with InsertParams
func TestDatasets_Insert_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-query",
	})
	require.NoError(t, err)

	// Insert events using Insert method
	err = api.Insert(ctx, dataset.ID, InsertParams{
		Events: []Event{
			{
				Input: map[string]interface{}{
					"text": "hello",
				},
				Expected: map[string]interface{}{
					"output": "Hello",
				},
			},
		},
	})
	require.NoError(t, err)
}

// TestDatasets_Fetch_Integration tests fetching dataset events
func TestDatasets_Fetch_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset with events
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-query",
	})
	require.NoError(t, err)

	// Insert some events
	events := []Event{
		{Input: map[string]interface{}{"q": "1"}, Expected: map[string]interface{}{"a": "1"}},
		{Input: map[string]interface{}{"q": "2"}, Expected: map[string]interface{}{"a": "2"}},
		{Input: map[string]interface{}{"q": "3"}, Expected: map[string]interface{}{"a": "3"}},
	}
	err = api.InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Fetch events
	response, err := api.Fetch(ctx, dataset.ID, "", 10)
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.GreaterOrEqual(t, len(response.Events), 3)
}

// TestDatasets_EventFields_Integration tests that all Event fields are properly unmarshaled
func TestDatasets_EventFields_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-fields",
	})
	require.NoError(t, err)

	// Insert an event with comprehensive data
	testInput := map[string]interface{}{
		"question": "What is the meaning of life?",
		"context":  "philosophical",
	}
	testExpected := map[string]interface{}{
		"answer":     "42",
		"confidence": 0.95,
	}
	testMetadata := map[string]interface{}{
		"source": "test",
		"model":  "test-model",
	}
	testTags := []string{"philosophy", "test", "integration"}

	events := []Event{
		{
			Input:    testInput,
			Expected: testExpected,
			Metadata: testMetadata,
			Tags:     testTags,
		},
	}
	err = api.InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Fetch the events back
	response, err := api.Fetch(ctx, dataset.ID, "", 10)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotEmpty(t, response.Events)

	// Unmarshal the first event
	var event Event
	err = json.Unmarshal(response.Events[0], &event)
	require.NoError(t, err)

	// Verify core data fields
	assert.NotEmpty(t, event.ID, "Event should have an ID")
	assert.NotNil(t, event.Input, "Event should have Input")
	assert.NotNil(t, event.Expected, "Event should have Expected")
	assert.NotNil(t, event.Metadata, "Event should have Metadata")
	assert.Equal(t, testTags, event.Tags, "Event tags should match")

	// Verify system fields are populated
	assert.NotEmpty(t, event.XactID, "Event should have _xact_id")
	assert.NotEmpty(t, event.Created, "Event should have created timestamp")
	assert.Equal(t, project.ID, event.ProjectID, "Event should have project_id")
	assert.Equal(t, dataset.ID, event.DatasetID, "Event should have dataset_id")

	// Verify tracing fields are populated
	assert.NotEmpty(t, event.SpanID, "Event should have span_id")
	assert.NotEmpty(t, event.RootSpanID, "Event should have root_span_id")

	// Log all fields for inspection
	t.Logf("Event fields: ID=%s, XactID=%s, Created=%s, ProjectID=%s, DatasetID=%s",
		event.ID, event.XactID, event.Created, event.ProjectID, event.DatasetID)
	t.Logf("Tracing fields: SpanID=%s, RootSpanID=%s, SpanParents=%v, IsRoot=%v",
		event.SpanID, event.RootSpanID, event.SpanParents, event.IsRoot)
	t.Logf("PaginationKey=%s", event.PaginationKey)
}

// TestDatasets_Fetch_Pagination tests paginated fetching
func TestDatasets_Fetch_Pagination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-query",
	})
	require.NoError(t, err)

	// Insert multiple events
	events := []Event{
		{Input: map[string]interface{}{"n": 1}, Expected: map[string]interface{}{"v": 1}},
		{Input: map[string]interface{}{"n": 2}, Expected: map[string]interface{}{"v": 2}},
		{Input: map[string]interface{}{"n": 3}, Expected: map[string]interface{}{"v": 3}},
		{Input: map[string]interface{}{"n": 4}, Expected: map[string]interface{}{"v": 4}},
	}
	err = api.InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Fetch with limit
	response, err := api.Fetch(ctx, dataset.ID, "", 2)
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, 2, len(response.Events))

	// Fetch next page if cursor exists
	if response.Cursor != "" {
		nextResponse, err := api.Fetch(ctx, dataset.ID, response.Cursor, 2)
		require.NoError(t, err)
		assert.NotNil(t, nextResponse)
	}
}

// TestDatasets_Query_ByID tests querying a dataset by ID
func TestDatasets_Query_ByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	datasetName := "test-dataset-modify"
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      datasetName,
	})
	require.NoError(t, err)

	// Query by ID
	response, err := api.Query(ctx, QueryParams{
		ID:    dataset.ID,
		Limit: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.GreaterOrEqual(t, len(response.Objects), 1)

	// Verify we found our dataset
	found := false
	for _, ds := range response.Objects {
		if ds.ID == dataset.ID {
			found = true
			assert.Contains(t, ds.Name, datasetName)
			break
		}
	}
	assert.True(t, found, "Should find the created dataset")
}

// TestDatasets_Query_ByName tests querying datasets by name
func TestDatasets_Query_ByName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset with unique name
	project := createTestProject(t, client)
	datasetName := "test-dataset-modify"
	created, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      datasetName,
	})
	require.NoError(t, err)

	// Query by name
	response, err := api.Query(ctx, QueryParams{
		Name:  datasetName,
		Limit: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, response)

	// Should find our dataset
	found := false
	for _, ds := range response.Objects {
		if ds.ID == created.ID {
			found = true
			assert.Contains(t, ds.Name, datasetName)
			break
		}
	}
	assert.True(t, found, "Should find the created dataset by name")
}

// TestDatasets_Query_ByProjectName tests querying datasets by project name
func TestDatasets_Query_ByProjectName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "test-dataset-query",
	})
	require.NoError(t, err)

	// Query by project name (with retry for eventual consistency)
	var found bool
	var response *QueryResponse
	for i := 0; i < 3; i++ {
		response, err = api.Query(ctx, QueryParams{
			ProjectName: integrationTestProject,
			Limit:       10,
		})
		require.NoError(t, err)
		require.NotNil(t, response)

		// Check if dataset is in results
		found = false
		for _, ds := range response.Objects {
			if ds.ID == dataset.ID {
				found = true
				break
			}
		}

		if found {
			break
		}

		// Wait a bit before retrying (eventual consistency)
		if i < 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	assert.GreaterOrEqual(t, len(response.Objects), 1)
	assert.True(t, found, "Should find dataset in project")
}

// TestDatasets_Delete_Integration tests deleting a dataset and verifying it's gone
func TestDatasets_Delete_Integration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	datasetName := "test-dataset-modify"
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      datasetName,
	})
	require.NoError(t, err)

	// Verify dataset exists by querying for it
	queryBefore, err := api.Query(ctx, QueryParams{
		ID:    dataset.ID,
		Limit: 1,
	})
	require.NoError(t, err)
	assert.Len(t, queryBefore.Objects, 1)
	assert.Equal(t, dataset.ID, queryBefore.Objects[0].ID)

	// Delete the dataset
	err = api.Delete(ctx, dataset.ID)
	require.NoError(t, err)

	// Verify dataset is gone by querying for it
	queryAfter, err := api.Query(ctx, QueryParams{
		ID:    dataset.ID,
		Limit: 1,
	})
	require.NoError(t, err)
	assert.Len(t, queryAfter.Objects, 0, "Dataset should not be found after deletion")
}

// TestDatasets_FullLifecycle tests the complete dataset lifecycle
func TestDatasets_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Step 1: Create project and dataset
	project := createTestProject(t, client)
	datasetName := "lifecycle-test"
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID:   project.ID,
		Name:        datasetName,
		Description: "Full lifecycle test dataset",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, dataset.ID)
	assert.Contains(t, dataset.Name, datasetName)

	// Step 2: Verify dataset exists via Query
	queryResult, err := api.Query(ctx, QueryParams{
		ID:    dataset.ID,
		Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, queryResult.Objects, 1)
	assert.Equal(t, dataset.ID, queryResult.Objects[0].ID)

	// Step 3: Insert events
	events := []Event{
		{
			Input: map[string]interface{}{
				"question": "What is 2+2?",
			},
			Expected: map[string]interface{}{
				"answer": "4",
			},
			Tags: []string{"math", "easy"},
		},
		{
			Input: map[string]interface{}{
				"question": "What is the capital of France?",
			},
			Expected: map[string]interface{}{
				"answer": "Paris",
			},
			Tags: []string{"geography", "easy"},
		},
	}
	err = api.InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Step 4: Fetch events and verify content
	fetchResult, err := api.Fetch(ctx, dataset.ID, "", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(fetchResult.Events), 2, "Should have at least 2 events")

	// Verify event content
	foundMath := false
	foundGeo := false
	for _, rawEvent := range fetchResult.Events {
		var event Event
		err := json.Unmarshal(rawEvent, &event)
		require.NoError(t, err)

		if input, ok := event.Input.(map[string]interface{}); ok {
			if question, ok := input["question"].(string); ok {
				if question == "What is 2+2?" {
					foundMath = true
					if expected, ok := event.Expected.(map[string]interface{}); ok {
						assert.Equal(t, "4", expected["answer"])
					}
				}
				if question == "What is the capital of France?" {
					foundGeo = true
					if expected, ok := event.Expected.(map[string]interface{}); ok {
						assert.Equal(t, "Paris", expected["answer"])
					}
				}
			}
		}
	}
	assert.True(t, foundMath, "Should find math question")
	assert.True(t, foundGeo, "Should find geography question")

	// Step 5: Delete dataset
	err = api.Delete(ctx, dataset.ID)
	require.NoError(t, err)

	// Step 6: Verify dataset is gone
	queryAfterDelete, err := api.Query(ctx, QueryParams{
		ID:    dataset.ID,
		Limit: 1,
	})
	require.NoError(t, err)
	assert.Len(t, queryAfterDelete.Objects, 0, "Dataset should not exist after deletion")
}

// TestDatasets_Query_WithAllParams tests querying with various parameters
func TestDatasets_Query_WithAllParams(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Create a project and dataset
	project := createTestProject(t, client)
	datasetName := "query-params-test"
	dataset, err := api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      datasetName,
	})
	require.NoError(t, err)

	// Test query with ProjectID
	response1, err := api.Query(ctx, QueryParams{
		ProjectID: project.ID,
		Limit:     5,
	})
	require.NoError(t, err)
	assert.NotNil(t, response1)

	// Test query with StartingAfter (pagination)
	if len(response1.Objects) > 0 {
		response2, err := api.Query(ctx, QueryParams{
			ProjectID:     project.ID,
			StartingAfter: response1.Objects[0].ID,
			Limit:         5,
		})
		require.NoError(t, err)
		assert.NotNil(t, response2)
	}

	// Test query with Name and ProjectID
	response3, err := api.Query(ctx, QueryParams{
		ProjectID: project.ID,
		Name:      datasetName,
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, response3.Objects, 1)
	assert.Equal(t, dataset.ID, response3.Objects[0].ID)
}

// TestDatasets_CreateParams_Validation tests parameter validation
func TestDatasets_CreateParams_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test empty project ID
	_, err := api.Create(ctx, CreateParams{
		Name: "test-dataset",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	// Test empty name
	project := createTestProject(t, client)
	_, err = api.Create(ctx, CreateParams{
		ProjectID: project.ID,
		Name:      "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestDatasets_Insert_Validation tests Insert parameter validation
func TestDatasets_Insert_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test empty dataset ID
	err := api.InsertEvents(ctx, "", []Event{
		{Input: map[string]interface{}{"test": "value"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestDatasets_Fetch_Validation tests Fetch parameter validation
func TestDatasets_Fetch_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	client := vcr.GetHTTPSClient(t)
	api := New(client)

	// Test empty dataset ID
	_, err := api.Fetch(ctx, "", "", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestOrigin_Serialization tests that the Origin struct properly serializes all fields
func TestOrigin_Serialization(t *testing.T) {
	origin := Origin{
		ObjectType: "dataset",
		ObjectID:   "dataset-123",
		ID:         "event-456",
		Created:    "2024-01-15T10:30:00Z",
		XactID:     "xact-789",
	}

	// Marshal to JSON
	data, err := json.Marshal(origin)
	require.NoError(t, err)

	// Unmarshal back
	var unmarshaled Origin
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	// Verify all fields
	assert.Equal(t, "dataset", unmarshaled.ObjectType)
	assert.Equal(t, "dataset-123", unmarshaled.ObjectID)
	assert.Equal(t, "event-456", unmarshaled.ID)
	assert.Equal(t, "2024-01-15T10:30:00Z", unmarshaled.Created)
	assert.Equal(t, "xact-789", unmarshaled.XactID)
}
