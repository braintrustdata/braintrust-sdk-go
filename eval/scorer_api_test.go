package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

// TestScorerAPI_Get tests loading a scorer by slug
func TestScorerAPI_Get(t *testing.T) {
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

	testSlug := "test-scorer-api-get"

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a test scorer function
	promptData := map[string]any{
		"parser": map[string]any{
			"type":          "llm_classifier",
			"use_cot":       true,
			"choice_scores": map[string]any{"fail": 0.0, "pass": 1.0},
		},
		"prompt": map[string]any{
			"type": "chat",
			"messages": []map[string]any{
				{"role": "system", "content": "You are a scorer. Evaluate the input and output."},
				{"role": "user", "content": "Choose 'pass' if the output is good, 'fail' if it's bad."},
			},
		},
		"options": map[string]any{
			"model":  "gpt-4o-mini",
			"params": map[string]any{"use_cache": true, "temperature": 0},
		},
	}

	function, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID:    project.ID,
		Name:         "Test Scorer",
		Slug:         testSlug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: promptData,
	})
	require.NoError(t, err)
	require.NotNil(t, function)

	// Defer cleanup
	defer func() {
		_ = functions.Delete(ctx, function.ID)
	}()

	// Create ScorerAPI
	scorerAPI := &ScorerAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Test: Get should return a Scorer
	scorer, err := scorerAPI.Get(ctx, testSlug)
	require.NoError(t, err)
	require.NotNil(t, scorer)

	// Verify the scorer has the correct name
	assert.Equal(t, "Test Scorer", scorer.Name())

	// Test: Scorer should be callable
	result := TaskResult[testDatasetInput, testDatasetOutput]{
		Input:    testDatasetInput{Question: "What is 2+2?"},
		Output:   testDatasetOutput{Answer: "4"},
		Expected: testDatasetOutput{Answer: "4"},
	}

	scores, err := scorer.Run(ctx, result)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, "Test Scorer", scores[0].Name)
	assert.GreaterOrEqual(t, scores[0].Score, 0.0)
	assert.LessOrEqual(t, scores[0].Score, 1.0)
}

// TestScorerAPI_Get_EmptySlug tests error handling
func TestScorerAPI_Get_EmptySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	scorerAPI := &ScorerAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on empty slug
	_, err = scorerAPI.Get(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestScorerAPI_Get_NotFound tests not found error
func TestScorerAPI_Get_NotFound(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	scorerAPI := &ScorerAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on non-existent scorer
	_, err = scorerAPI.Get(ctx, "nonexistent-scorer-slug-12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestScorerAPI_Query tests querying scorers
func TestScorerAPI_Query(t *testing.T) {
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

	testSlug1 := "test-scorer-query-1"
	testSlug2 := "test-scorer-query-2"

	// Clean up any existing functions
	for _, slug := range []string{testSlug1, testSlug2} {
		if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
			ProjectName: integrationTestProject,
			Slug:        slug,
			Limit:       1,
		}); len(existing) > 0 {
			_ = functions.Delete(ctx, existing[0].ID)
		}
	}

	// Create test scorer functions
	promptData := map[string]any{
		"parser": map[string]any{
			"type":          "llm_classifier",
			"use_cot":       true,
			"choice_scores": map[string]any{"fail": 0.0, "pass": 1.0},
		},
		"prompt": map[string]any{
			"type": "chat",
			"messages": []map[string]any{
				{"role": "system", "content": "You are a scorer."},
			},
		},
		"options": map[string]any{
			"model": "gpt-4o-mini",
		},
	}

	function1, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID:    project.ID,
		Name:         "Scorer 1",
		Slug:         testSlug1,
		FunctionType: "scorer",
		FunctionData: map[string]any{"type": "prompt"},
		PromptData:   promptData,
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, function1.ID) }()

	function2, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID:    project.ID,
		Name:         "Scorer 2",
		Slug:         testSlug2,
		FunctionType: "scorer",
		FunctionData: map[string]any{"type": "prompt"},
		PromptData:   promptData,
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, function2.ID) }()

	// Create ScorerAPI
	scorerAPI := &ScorerAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Test: Query should find scorers
	scorers, err := scorerAPI.Query(ctx, ScorerQueryOpts{
		Project: integrationTestProject,
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(scorers), 2)

	// Verify scorer info is correct
	var foundScorer1, foundScorer2 bool
	for _, scorer := range scorers {
		if scorer.Name == "Scorer 1" {
			foundScorer1 = true
			assert.NotEmpty(t, scorer.ID)
			assert.Equal(t, integrationTestProject, scorer.Project)
		}
		if scorer.Name == "Scorer 2" {
			foundScorer2 = true
			assert.NotEmpty(t, scorer.ID)
			assert.Equal(t, integrationTestProject, scorer.Project)
		}
	}
	assert.True(t, foundScorer1, "Should find Scorer 1")
	assert.True(t, foundScorer2, "Should find Scorer 2")
}

// TestScorerAPI_TypeSafety verifies compile-time type safety
func TestScorerAPI_TypeSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session := tests.NewSession(t)

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// This should compile
	scorerAPI := &ScorerAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// The returned Scorer should have the correct type
	var _ = func() (Scorer[testDatasetInput, testDatasetOutput], error) {
		return scorerAPI.Get(ctx, "test-slug")
	}

	// This is a compile-time check - if it compiles, the test passes
	assert.NotNil(t, scorerAPI)
}

// TestScorerAPI_OutputParsing tests various scorer output formats
func TestScorerAPI_OutputParsing(t *testing.T) {
	t.Skip("TODO: Add unit tests for scorer output parsing (map with score, number, etc.)")
}
