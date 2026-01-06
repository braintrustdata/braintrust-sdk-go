package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

// TestScorerAPI_Get tests loading a scorer by slug
func TestScorerAPI_Get(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	apiClient := createIntegrationTestAPIClient(t)
	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	// Use fixed name for VCR determinism
	testSlug := tests.Name(t, "slug")

	// Clean up any existing function with this slug from previous failed test runs
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
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

	function, err := functions.Create(ctx, functionsapi.CreateParams{
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

	// Create FunctionsAPI
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Test: Scorer should return a Scorer
	scorer, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug})
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

	// Get API credentials and create API client
	apiInfo := session.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL))
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on empty slug
	_, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestScorerAPI_Get_NotFound tests not found error
func TestScorerAPI_Get_NotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create API client with VCR support
	apiClient := createIntegrationTestAPIClient(t)
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Should error on non-existent scorer
	_, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: "nonexistent-scorer-slug-12345"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestScorerAPI_TypeSafety verifies compile-time type safety
func TestScorerAPI_TypeSafety(t *testing.T) {
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

	// The returned Scorer should have the correct type
	var _ = func() (Scorer[testDatasetInput, testDatasetOutput], error) {
		return functionsAPI.Scorer(ctx, FunctionOpts{Slug: "test-slug"})
	}

	// This is a compile-time check - if it compiles, the test passes
	assert.NotNil(t, functionsAPI)
}

// TestScorerAPI_OutputParsing tests various scorer output formats
func TestScorerAPI_OutputParsing(t *testing.T) {
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

	// Test 1: Scorer that returns a number (simple score)
	promptDataSimple := map[string]any{
		"parser": map[string]any{
			"type":          "llm_classifier",
			"use_cot":       false,
			"choice_scores": map[string]any{"bad": 0.0, "good": 1.0},
		},
		"prompt": map[string]any{
			"type": "chat",
			"messages": []map[string]any{
				{"role": "system", "content": "You are a scorer."},
				{"role": "user", "content": "Choose 'good' if output matches expected, 'bad' otherwise."},
			},
		},
		"options": map[string]any{
			"model":  "gpt-4o-mini",
			"params": map[string]any{"use_cache": true, "temperature": 0},
		},
	}

	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "Simple Scorer",
		Slug:         testSlug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: promptDataSimple,
	})
	require.NoError(t, err)
	require.NotNil(t, function)

	defer func() {
		_ = functions.Delete(ctx, function.ID)
	}()

	// Create FunctionsAPI
	functionsAPI := &FunctionsAPI[testDatasetInput, testDatasetOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Get the scorer
	scorer, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, scorer)

	// Test invoking with simple output (number)
	result := TaskResult[testDatasetInput, testDatasetOutput]{
		Input:    testDatasetInput{Question: "What is 2+2?"},
		Output:   testDatasetOutput{Answer: "4"},
		Expected: testDatasetOutput{Answer: "4"},
	}

	scores, err := scorer.Run(ctx, result)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.GreaterOrEqual(t, scores[0].Score, 0.0)
	assert.LessOrEqual(t, scores[0].Score, 1.0)

	// Test 2: Scorer that returns a map with name and score
	testSlug2 := tests.Name(t, "slug2")

	// Clean up any existing function
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug2,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	promptDataMap := map[string]any{
		"parser": map[string]any{
			"type":          "llm_classifier",
			"use_cot":       true,
			"choice_scores": map[string]any{"incorrect": 0.0, "correct": 1.0},
		},
		"prompt": map[string]any{
			"type": "chat",
			"messages": []map[string]any{
				{"role": "system", "content": "You are a detailed scorer."},
				{"role": "user", "content": "Evaluate if the output is correct. Return 'correct' or 'incorrect'."},
			},
		},
		"options": map[string]any{
			"model":  "gpt-4o-mini",
			"params": map[string]any{"use_cache": true, "temperature": 0},
		},
	}

	function2, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "Detailed Scorer",
		Slug:         testSlug2,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: promptDataMap,
	})
	require.NoError(t, err)
	require.NotNil(t, function2)

	defer func() {
		_ = functions.Delete(ctx, function2.ID)
	}()

	// Get the second scorer
	scorer2, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug2})
	require.NoError(t, err)
	require.NotNil(t, scorer2)
	assert.Equal(t, "Detailed Scorer", scorer2.Name())

	// Test invoking with map output (with name, score, and potentially metadata)
	scores2, err := scorer2.Run(ctx, result)
	require.NoError(t, err)
	require.Len(t, scores2, 1)
	assert.Equal(t, "Detailed Scorer", scores2[0].Name)
	assert.GreaterOrEqual(t, scores2[0].Score, 0.0)
	assert.LessOrEqual(t, scores2[0].Score, 1.0)
}
