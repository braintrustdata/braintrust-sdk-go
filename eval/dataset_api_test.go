package eval

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

type testDatasetInput struct {
	Question string `json:"question"`
}

type testDatasetOutput struct {
	Answer string `json:"answer"`
}

// requireAPIKey fails the test if BRAINTRUST_API_KEY is not set
func requireAPIKey(t *testing.T) string {
	t.Helper()
	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if apiKey == "" {
		t.Fatal("BRAINTRUST_API_KEY not set, cannot run integration test")
	}
	return apiKey
}

// createTestSession creates a real session for testing
func createTestSession(t *testing.T, apiKey string) *auth.Session {
	t.Helper()
	ctx := context.Background()

	session, err := auth.NewSession(ctx, auth.Options{
		APIKey: apiKey,
		AppURL: "https://www.braintrust.dev",
		Logger: logger.Discard(),
	})
	require.NoError(t, err)

	// Wait for login to complete
	_, err = session.Login(ctx)
	require.NoError(t, err)

	return session
}

// TestDatasetAPI_Get_Integration tests loading a dataset by ID with real API calls
func TestDatasetAPI_Get_Integration(t *testing.T) {
	apiKey := requireAPIKey(t)

	ctx := context.Background()
	session := createTestSession(t, apiKey)
	defer session.Close()

	// Create API client
	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	// Create a test dataset
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	dataset, err := apiClient.Datasets().Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        tests.RandomName(t, "dataset"),
		Description: "Test dataset for DatasetAPI.Get",
	})
	require.NoError(t, err)
	defer func() {
		_ = apiClient.Datasets().Delete(ctx, dataset.ID)
	}()

	// Insert test data
	events := []datasets.Event{
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

	err = apiClient.Datasets().InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Now test the DatasetAPI
	datasetAPI := &DatasetAPI[testDatasetInput, testDatasetOutput]{
		api: apiClient,
	}

	cases, err := datasetAPI.Get(ctx, dataset.ID)
	require.NoError(t, err)
	require.NotNil(t, cases)

	// Read all cases (order may not be guaranteed)
	var questions []string
	var answers []string
	for {
		testCase, err := cases.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		questions = append(questions, testCase.Input.Question)
		answers = append(answers, testCase.Expected.Answer)
	}

	// Verify we got both cases
	assert.Len(t, questions, 2)
	assert.Contains(t, questions, "What is 2+2?")
	assert.Contains(t, questions, "What is the capital of France?")
	assert.Contains(t, answers, "4")
	assert.Contains(t, answers, "Paris")
}

// TestDatasetAPI_Get_EmptyID tests error handling
func TestDatasetAPI_Get_EmptyID(t *testing.T) {
	apiKey := requireAPIKey(t)

	ctx := context.Background()
	session := createTestSession(t, apiKey)
	defer session.Close()

	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	datasetAPI := &DatasetAPI[testDatasetInput, testDatasetOutput]{
		api: apiClient,
	}

	// Should error on empty ID
	_, err := datasetAPI.Get(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestDatasetAPI_Query_Integration tests querying a dataset with options
func TestDatasetAPI_Query_Integration(t *testing.T) {
	apiKey := requireAPIKey(t)

	ctx := context.Background()
	session := createTestSession(t, apiKey)
	defer session.Close()

	// Create API client
	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	// Create a test dataset
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	datasetName := tests.RandomName(t, "dataset")
	dataset, err := apiClient.Datasets().Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        datasetName,
		Description: "Test dataset for DatasetAPI.Query",
	})
	require.NoError(t, err)
	defer func() {
		_ = apiClient.Datasets().Delete(ctx, dataset.ID)
	}()

	// Insert test data
	events := []datasets.Event{
		{
			Input: map[string]interface{}{
				"question": "Test question 1",
			},
			Expected: map[string]interface{}{
				"answer": "Test answer 1",
			},
		},
		{
			Input: map[string]interface{}{
				"question": "Test question 2",
			},
			Expected: map[string]interface{}{
				"answer": "Test answer 2",
			},
		},
		{
			Input: map[string]interface{}{
				"question": "Test question 3",
			},
			Expected: map[string]interface{}{
				"answer": "Test answer 3",
			},
		},
	}

	err = apiClient.Datasets().InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Test Query with ID and Limit
	datasetAPI := &DatasetAPI[testDatasetInput, testDatasetOutput]{
		api: apiClient,
	}

	cases, err := datasetAPI.Query(ctx, DatasetQueryOpts{
		ID:    dataset.ID,
		Limit: 2, // Only get 2 cases
	})
	require.NoError(t, err)
	require.NotNil(t, cases)

	// Read exactly 2 cases (limit was 2)
	case1, err := cases.Next()
	require.NoError(t, err)
	assert.NotEmpty(t, case1.Input.Question)

	case2, err := cases.Next()
	require.NoError(t, err)
	assert.NotEmpty(t, case2.Input.Question)

	// Should get EOF (limit was 2)
	_, err = cases.Next()
	assert.Equal(t, io.EOF, err)
}

// TestDatasetAPI_TypeSafety verifies compile-time type safety
func TestDatasetAPI_TypeSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create a minimal session (we won't actually use it)
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey: "test-key",
		AppURL: "https://test.braintrust.dev",
		Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer session.Close()

	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	// This should compile
	datasetAPI := &DatasetAPI[testDatasetInput, testDatasetOutput]{
		api: apiClient,
	}

	// The returned Dataset should have the correct type
	var _ = func() (Dataset[testDatasetInput, testDatasetOutput], error) {
		return datasetAPI.Get(ctx, "test-id")
	}

	// This is a compile-time check - if it compiles, the test passes
	assert.NotNil(t, datasetAPI)
}

// TestDatasetAPI_PopulatesDatasetFields tests that dataset iterator populates ID, XactID, and Created
func TestDatasetAPI_PopulatesDatasetFields(t *testing.T) {
	apiKey := requireAPIKey(t)

	ctx := context.Background()
	session := createTestSession(t, apiKey)
	defer session.Close()

	// Create API client
	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	// Create a test dataset
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	dataset, err := apiClient.Datasets().Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        tests.RandomName(t, "dataset-fields-test"),
		Description: "Test dataset for verifying ID, XactID, Created fields",
	})
	require.NoError(t, err)
	defer func() {
		_ = apiClient.Datasets().Delete(ctx, dataset.ID)
	}()

	// Insert a test event
	events := []datasets.Event{
		{
			Input: map[string]interface{}{
				"question": "What is 2+2?",
			},
			Expected: map[string]interface{}{
				"answer": "4",
			},
		},
	}

	err = apiClient.Datasets().InsertEvents(ctx, dataset.ID, events)
	require.NoError(t, err)

	// Load dataset
	datasetAPI := &DatasetAPI[testDatasetInput, testDatasetOutput]{
		api: apiClient,
	}

	cases, err := datasetAPI.Get(ctx, dataset.ID)
	require.NoError(t, err)
	require.NotNil(t, cases)

	// Read the first case
	testCase, err := cases.Next()
	require.NoError(t, err)

	// Verify that dataset-specific fields are populated
	assert.NotEmpty(t, testCase.ID, "Case should have ID populated from dataset record")
	assert.NotEmpty(t, testCase.XactID, "Case should have XactID populated from dataset record")
	assert.NotEmpty(t, testCase.Created, "Case should have Created populated from dataset record")

	t.Logf("Dataset record fields: ID=%s, XactID=%s, Created=%s",
		testCase.ID, testCase.XactID, testCase.Created)
}
