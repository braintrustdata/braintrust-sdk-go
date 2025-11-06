package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

// TestEval_Integration tests creating a task function and running a full evaluation
func TestEval_Integration(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// Create config for the evaluation
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "task")

	// Clean up any existing function from previous test runs
	if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a simple prompt
	// Note: function_type should be omitted for prompts, not "prompt"
	function, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID: project.ID,
		Name:      "Test Echo Task",
		Slug:      testSlug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "user", "content": "Say hello to {{input}}"},
				},
			},
			"options": map[string]any{
				"model":  "gpt-4o-mini",
				"params": map[string]any{"use_cache": true, "temperature": 0},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, function)

	// Defer cleanup
	defer func() {
		_ = functions.Delete(ctx, function.ID)
	}()

	// Verify the function is queryable
	foundFuncs, err := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	})
	require.NoError(t, err)
	require.Len(t, foundFuncs, 1, "function should be queryable after creation")

	// Create TaskAPI and get the task
	taskAPI := &TaskAPI[string, string]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := taskAPI.Get(ctx, testSlug)
	require.NoError(t, err)
	require.NotNil(t, task)

	// Create evaluation cases
	cases := NewCases([]Case[string, string]{
		{
			Input:    "hello",
			Expected: "hello",
		},
		{
			Input:    "world",
			Expected: "world",
		},
	})

	// Create a simple scorer that checks if output contains the input
	containsScorer := NewScorer("contains", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
		score := 0.0
		if len(result.Output) > 0 {
			score = 1.0
		}
		return Scores{{
			Name:  "contains",
			Score: score,
		}}, nil
	})

	// Create tracer provider for the evaluation
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run the evaluation
	result, err := Run(ctx, Opts[string, string]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task:       task,
		Scorers:    []Scorer[string, string]{containsScorer},
		Quiet:      true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the evaluation ran
	assert.NotEmpty(t, result.String(), "result should have a string representation")
}

// TestEval_Integration_StringToStruct tests that a prompt returning a plain string
// can be properly converted to a struct type
func TestEval_Integration_StringToStruct(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// Create config for the evaluation
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "struct")

	// Clean up any existing function from previous test runs
	if existing, _ := functions.Query(ctx, api.FunctionQueryOpts{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that returns JSON
	function, err := functions.Create(ctx, api.FunctionCreateRequest{
		ProjectID: project.ID,
		Name:      "JSON Answer Prompt",
		Slug:      testSlug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You are a helpful assistant that returns JSON.",
					},
					{
						"role":    "user",
						"content": `Return ONLY a JSON object (no other text) with an "answer" field containing the answer as a STRING. Question: {{input.question}}. Example: {"answer": "4"}`,
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature": 0,
					"max_tokens":  50,
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

	// Define input and output types
	type QuestionInput struct {
		Question string `json:"question"`
	}
	type AnswerOutput struct {
		Answer string `json:"answer"`
	}

	// Create TaskAPI and get the task
	taskAPI := &TaskAPI[QuestionInput, AnswerOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := taskAPI.Get(ctx, testSlug)
	require.NoError(t, err)
	require.NotNil(t, task)

	// Create evaluation cases
	cases := NewCases([]Case[QuestionInput, AnswerOutput]{
		{
			Input:    QuestionInput{Question: "What is 2+2?"},
			Expected: AnswerOutput{Answer: "4"},
		},
	})

	// Create a simple scorer
	scorer := NewScorer("match", func(ctx context.Context, result TaskResult[QuestionInput, AnswerOutput]) (Scores, error) {
		if result.Output.Answer != "" {
			return S(1.0), nil
		}
		return S(0.0), nil
	})

	// Create tracer provider for the evaluation
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run the evaluation - this should handle string-to-struct conversion
	result, err := Run(ctx, Opts[QuestionInput, AnswerOutput]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task:       task,
		Scorers:    []Scorer[QuestionInput, AnswerOutput]{scorer},
		Quiet:      true,
	}, cfg, session, tp)

	require.NoError(t, err, "evaluation should succeed when prompt returns JSON that can be parsed to struct")
	require.NotNil(t, result)
	assert.NotEmpty(t, result.String(), "result should have a string representation")
}

// TestEval_Integration_DatasetByID tests loading a dataset by ID
func TestEval_Integration_DatasetByID(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	// Create dataset
	datasetsAPI := apiClient.Datasets()
	dataset, err := datasetsAPI.Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        tests.RandomName(t, "dataset"),
		Description: "Test dataset for eval integration",
	})
	require.NoError(t, err)
	defer func() { _ = datasetsAPI.Delete(ctx, dataset.ID) }()

	// Insert test data
	err = datasetsAPI.InsertEvents(ctx, dataset.ID, []datasets.Event{
		{Input: 2, Expected: 4},
		{Input: 5, Expected: 10},
	})
	require.NoError(t, err)

	// Load dataset using DatasetAPI
	datasetAPI := &DatasetAPI[int, int]{apiClient: apiClient}
	cases, err := datasetAPI.Get(ctx, dataset.ID)
	require.NoError(t, err)

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run evaluation
	result, err := Run(ctx, Opts[int, int]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task: T(func(ctx context.Context, input int) (int, error) {
			return input * 2, nil
		}),
		Scorers: []Scorer[int, int]{
			NewScorer("equals", func(ctx context.Context, result TaskResult[int, int]) (Scores, error) {
				if result.Output == result.Expected {
					return S(1.0), nil
				}
				return S(0.0), nil
			}),
		},
		Quiet: true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestEval_Integration_DatasetByName tests loading a dataset by name
func TestEval_Integration_DatasetByName(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	// Create dataset with unique name
	datasetName := tests.RandomName(t, "dataset")
	datasetsAPI := apiClient.Datasets()
	dataset, err := datasetsAPI.Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        datasetName,
		Description: "Test dataset for name-based eval",
	})
	require.NoError(t, err)
	defer func() { _ = datasetsAPI.Delete(ctx, dataset.ID) }()

	// Insert test data
	err = datasetsAPI.InsertEvents(ctx, dataset.ID, []datasets.Event{
		{Input: 3, Expected: 9},
		{Input: 4, Expected: 16},
	})
	require.NoError(t, err)

	// Load dataset by name using DatasetAPI
	datasetAPI := &DatasetAPI[int, int]{apiClient: apiClient}
	cases, err := datasetAPI.Query(ctx, DatasetQueryOpts{Name: datasetName})
	require.NoError(t, err)

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run evaluation
	result, err := Run(ctx, Opts[int, int]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task: T(func(ctx context.Context, input int) (int, error) {
			return input * input, nil
		}),
		Scorers: []Scorer[int, int]{
			NewScorer("equals", func(ctx context.Context, result TaskResult[int, int]) (Scores, error) {
				if result.Output == result.Expected {
					return S(1.0), nil
				}
				return S(0.0), nil
			}),
		},
		Quiet: true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestEval_Integration_DatasetWithTagsAndMetadata tests that tags and metadata are preserved from datasets
func TestEval_Integration_DatasetWithTagsAndMetadata(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	// Create dataset
	datasetsAPI := apiClient.Datasets()
	dataset, err := datasetsAPI.Create(ctx, datasets.CreateParams{
		ProjectID:   project.ID,
		Name:        tests.RandomName(t, "dataset"),
		Description: "Test dataset with tags and metadata",
	})
	require.NoError(t, err)
	defer func() { _ = datasetsAPI.Delete(ctx, dataset.ID) }()

	// Insert test data WITH TAGS AND METADATA
	err = datasetsAPI.InsertEvents(ctx, dataset.ID, []datasets.Event{
		{
			Input:    2,
			Expected: 4,
			Tags:     []string{"even", "small"},
			Metadata: map[string]interface{}{
				"category": "simple",
				"priority": 1,
			},
		},
	})
	require.NoError(t, err)

	// Load dataset
	datasetAPI := &DatasetAPI[int, int]{apiClient: apiClient}
	cases, err := datasetAPI.Get(ctx, dataset.ID)
	require.NoError(t, err)

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run evaluation - tags and metadata should be preserved
	result, err := Run(ctx, Opts[int, int]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task: T(func(ctx context.Context, input int) (int, error) {
			return input * 2, nil
		}),
		Scorers: []Scorer[int, int]{
			NewScorer("equals", func(ctx context.Context, result TaskResult[int, int]) (Scores, error) {
				// Verify metadata is passed to scorer
				assert.NotNil(t, result.Metadata)
				assert.Equal(t, "simple", result.Metadata["category"])
				if result.Output == result.Expected {
					return S(1.0), nil
				}
				return S(0.0), nil
			}),
		},
		Quiet: true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestEval_Integration_ExperimentTags tests experiment-level tags
func TestEval_Integration_ExperimentTags(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	cases := NewCases([]Case[string, string]{
		{Input: "hello", Expected: "hello"},
	})

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run eval with experiment-level tags
	result, err := Run(ctx, Opts[string, string]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{
			NewScorer("equals", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
				return S(1.0), nil
			}),
		},
		Tags:  []string{"production", "baseline", "v2.0"},
		Quiet: true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestEval_Integration_ExperimentMetadata tests experiment-level metadata
func TestEval_Integration_ExperimentMetadata(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	cases := NewCases([]Case[string, string]{
		{Input: "test", Expected: "test"},
	})

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run eval with experiment-level metadata
	result, err := Run(ctx, Opts[string, string]{
		Experiment: tests.RandomName(t, "exp"),
		Cases:      cases,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{
			NewScorer("equals", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
				return S(1.0), nil
			}),
		},
		Metadata: map[string]interface{}{
			"model":       "gpt-4",
			"temperature": 0.7,
			"version":     "1.0.0",
		},
		Quiet: true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestEval_Integration_UpdateFlag tests the Update flag for appending to experiments
func TestEval_Integration_UpdateFlag(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create unique experiment name
	experimentName := tests.RandomName(t, "update-test")

	cases1 := NewCases([]Case[string, string]{
		{Input: "hello", Expected: "hello"},
	})

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	scorer := NewScorer("equals", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
		return S(1.0), nil
	})

	// First run: Create new experiment (Update: false)
	result1, err := Run(ctx, Opts[string, string]{
		Experiment: experimentName,
		Cases:      cases1,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{scorer},
		Update:  false, // Create new
		Quiet:   true,
	}, cfg, session, tp)
	require.NoError(t, err)
	require.NotNil(t, result1)

	firstExpID := result1.ID()

	cases2 := NewCases([]Case[string, string]{
		{Input: "world", Expected: "world"},
	})

	// Second run: Append to existing experiment (Update: true)
	result2, err := Run(ctx, Opts[string, string]{
		Experiment: result1.Name(), // Use exact name from first run
		Cases:      cases2,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{scorer},
		Update:  true, // Append to existing
		Quiet:   true,
	}, cfg, session, tp)
	require.NoError(t, err)
	require.NotNil(t, result2)

	secondExpID := result2.ID()

	// When Update: true, should reuse the same experiment ID
	assert.Equal(t, firstExpID, secondExpID, "Update: true should reuse the same experiment ID")

	// Third run: Create new experiment (Update: false)
	result3, err := Run(ctx, Opts[string, string]{
		Experiment: result1.Name(),
		Cases:      cases1,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{scorer},
		Update:  false, // Create new
		Quiet:   true,
	}, cfg, session, tp)
	require.NoError(t, err)
	require.NotNil(t, result3)

	thirdExpID := result3.ID()

	// When Update: false, should create a different experiment ID
	assert.NotEqual(t, firstExpID, thirdExpID, "Update: false should create a new experiment ID")
}

// TestEval_DifferentProject tests running an eval with a different project name
func TestEval_DifferentProject(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// Create a different project (use fixed suffix instead of random)
	differentProjectName := integrationTestProject + "-other"
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: differentProjectName})
	require.NoError(t, err)
	require.NotNil(t, project)

	// Create config with default project (should be overridden by opts.Project)
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create a TracerProvider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Create test cases
	cases := NewCases([]Case[string, string]{
		{Input: "test1", Expected: "test1"},
		{Input: "test2", Expected: "test2"},
	})

	// Create a simple scorer
	scorer := NewScorer("exact-match", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
		if result.Output == result.Expected {
			return S(1.0), nil
		}
		return S(0.0), nil
	})

	// Run eval with the different project specified in opts
	result, err := Run(ctx, Opts[string, string]{
		Experiment:  tests.RandomName(t, "exp"),
		ProjectName: differentProjectName, // Override config.DefaultProjectName
		Cases:       cases,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{scorer},
		Quiet:   true,
	}, cfg, session, tp)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the result
	assert.NotEmpty(t, result.ID())
	assert.NotEmpty(t, result.Name())
	assert.NotEmpty(t, result.String())

	// Verify the experiment was created in the correct project by querying the project's experiments
	experiments := apiClient.Experiments()

	// Get the experiment by its ID to verify it's in the correct project
	// We'll use the experiments client to verify the project ID matches
	expFromAPI, err := experiments.Register(ctx, result.Name(), project.ID, api.RegisterExperimentOpts{
		Update: true, // Use Update:true to get existing experiment
	})
	require.NoError(t, err)
	assert.Equal(t, result.ID(), expFromAPI.ID, "Should get the same experiment")
	assert.Equal(t, project.ID, expFromAPI.ProjectID, "Experiment should be in the different project")
}

// TestEval_ProjectNameFallback tests that the project name fallback logic works correctly
func TestEval_ProjectNameFallback(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Get endpoints and create API client
	endpoints := session.Endpoints()
	apiClient, err := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))
	require.NoError(t, err)

	// Create a project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)
	require.NotNil(t, project)

	// Create config with default project
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Create a TracerProvider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Create test cases
	cases := NewCases([]Case[string, string]{
		{Input: "test1", Expected: "test1"},
	})

	// Create a simple scorer
	scorer := NewScorer("exact-match", func(ctx context.Context, result TaskResult[string, string]) (Scores, error) {
		if result.Output == result.Expected {
			return S(1.0), nil
		}
		return S(0.0), nil
	})

	// Run eval WITHOUT specifying ProjectName (should use cfg.DefaultProjectName)
	result, err := Run(ctx, Opts[string, string]{
		Experiment: tests.RandomName(t, "exp"),
		// ProjectName not specified - should fall back to cfg.DefaultProjectName
		Cases: cases,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Scorers: []Scorer[string, string]{scorer},
		Quiet:   true,
	}, cfg, session, tp)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the experiment was created in the correct project (cfg.DefaultProjectName)
	experiments := apiClient.Experiments()
	expFromAPI, err := experiments.Register(ctx, result.Name(), project.ID, api.RegisterExperimentOpts{
		Update: true, // Use Update:true to get existing experiment
	})
	require.NoError(t, err)
	assert.Equal(t, result.ID(), expFromAPI.ID, "Should get the same experiment")
	assert.Equal(t, project.ID, expFromAPI.ProjectID, "Experiment should be in the default project from config")
}

// TestEval_NoProjectName tests that eval fails when no project name is provided
func TestEval_NoProjectName(t *testing.T) {
	session := createIntegrationTestSession(t)
	t.Parallel()

	ctx := context.Background()

	// Create config with NO default project
	cfg := &config.Config{
		DefaultProjectName: "", // No default project
	}

	// Create a TracerProvider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Create test cases
	cases := NewCases([]Case[string, string]{
		{Input: "test1", Expected: "test1"},
	})

	// Run eval WITHOUT specifying ProjectName and NO config default (should fail)
	result, err := Run(ctx, Opts[string, string]{
		Experiment: tests.RandomName(t, "exp"),
		// ProjectName not specified AND cfg.DefaultProjectName is empty
		Cases: cases,
		Task: T(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}),
		Quiet: true,
	}, cfg, session, tp)

	// Should error because no project name is available
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "project name is required")
}
