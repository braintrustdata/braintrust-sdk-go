package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/tests"
)

// setupAPIIntegrationTest sets up common test infrastructure for API integration tests
func setupAPIIntegrationTest(t *testing.T) (*api.API, *auth.Session) {
	t.Helper()
	session := createIntegrationTestSession(t)

	endpoints := session.Endpoints()
	apiClient := api.NewClient(endpoints.APIKey, api.WithAPIURL(endpoints.APIURL))

	return apiClient, session
}

// TestFunctionsAPI_Task_StringInput tests that tasks can accept string input and return string output
func TestFunctionsAPI_Task_StringInput(t *testing.T) {
	apiClient, _ := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "task-string")

	// Clean up any existing function from previous test runs
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that echoes the input
	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID: project.ID,
		Name:      "String Echo Task",
		Slug:      testSlug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "user", "content": "Echo this text: {{input}}"},
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

	defer func() {
		_ = functions.Delete(ctx, function.ID)
	}()

	// Create FunctionsAPI with string types
	functionsAPI := &FunctionsAPI[string, string]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Get the task
	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Test invoking the task with string input
	output, err := task(ctx, "hello world", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, output.Value)

	// Verify output contains something (since it's an LLM, we can't predict exact output)
	assert.IsType(t, "", output.Value)
}

// TestFunctionsAPI_Task_StructInput tests that tasks can accept struct input and return struct output
func TestFunctionsAPI_Task_StructInput(t *testing.T) {
	apiClient, _ := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()

	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "task-struct")

	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that returns JSON
	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID: project.ID,
		Name:      "Struct Math Task",
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
						"content": "You return JSON with the answer field containing the numeric result.",
					},
					{
						"role":    "user",
						"content": `Calculate {{input.operation}} of {{input.a}} and {{input.b}}. Return ONLY a JSON object like {"answer": 5}. No other text.`,
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
	defer func() { _ = functions.Delete(ctx, function.ID) }()

	// Define input and output structs
	type MathInput struct {
		Operation string `json:"operation"`
		A         int    `json:"a"`
		B         int    `json:"b"`
	}
	type MathOutput struct {
		Answer int `json:"answer"`
	}

	// Create FunctionsAPI with struct types
	functionsAPI := &FunctionsAPI[MathInput, MathOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Invoke task with struct input
	input := MathInput{Operation: "sum", A: 2, B: 3}
	output, err := task(ctx, input, nil)
	require.NoError(t, err)
	assert.NotZero(t, output.Value.Answer)
	// The LLM should calculate 2+3=5, but we just verify it's a reasonable number
	assert.Greater(t, output.Value.Answer, 0)
}

// TestFunctionsAPI_Task_MapInput tests that tasks can accept map input and return map output
func TestFunctionsAPI_Task_MapInput(t *testing.T) {
	apiClient, _ := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()

	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "task-map")

	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that processes arbitrary map input
	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID: project.ID,
		Name:      "Map Processing Task",
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
						"content": "You process input and return a JSON object with a 'result' field.",
					},
					{
						"role":    "user",
						"content": `Process this data: {{input.name}} - {{input.value}}. Return ONLY JSON like {"result": "processed"}`,
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
	defer func() { _ = functions.Delete(ctx, function.ID) }()

	// Create FunctionsAPI with map types
	functionsAPI := &FunctionsAPI[map[string]any, map[string]any]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Invoke task with map input
	input := map[string]any{
		"name":  "test",
		"value": 42,
		"nested": map[string]any{
			"key": "value",
		},
	}
	output, err := task(ctx, input, nil)
	require.NoError(t, err)
	assert.NotNil(t, output.Value)
	assert.IsType(t, map[string]any{}, output.Value)
}

// TestFunctionsAPI_Scorer_VariousTypes tests that scorers can handle various input and output types
func TestFunctionsAPI_Scorer_VariousTypes(t *testing.T) {
	apiClient, _ := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()

	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "scorer-types")

	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a scorer that evaluates based on input/output
	promptData := map[string]any{
		"parser": map[string]any{
			"type":          "llm_classifier",
			"use_cot":       true,
			"choice_scores": map[string]any{"bad": 0.0, "good": 1.0},
		},
		"prompt": map[string]any{
			"type": "chat",
			"messages": []map[string]any{
				{"role": "system", "content": "You are a scorer. Evaluate quality."},
				{"role": "user", "content": "Input: {{input}}, Output: {{output}}. Choose 'good' or 'bad'."},
			},
		},
		"options": map[string]any{
			"model":  "gpt-4o-mini",
			"params": map[string]any{"use_cache": true, "temperature": 0},
		},
	}

	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "Type Flexible Scorer",
		Slug:         testSlug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: promptData,
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, function.ID) }()

	// Test with string types
	stringFunctionsAPI := &FunctionsAPI[string, string]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	stringScorer, err := stringFunctionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, stringScorer)

	stringResult := TaskResult[string, string]{
		Input:    "test input",
		Output:   "test output",
		Expected: "expected",
	}
	scores, err := stringScorer.Run(ctx, stringResult)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.GreaterOrEqual(t, scores[0].Score, 0.0)
	assert.LessOrEqual(t, scores[0].Score, 1.0)

	// Test with struct types
	type TestInput struct {
		Question string `json:"question"`
	}
	type TestOutput struct {
		Answer string `json:"answer"`
	}

	structFunctionsAPI := &FunctionsAPI[TestInput, TestOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	structScorer, err := structFunctionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)

	structResult := TaskResult[TestInput, TestOutput]{
		Input:    TestInput{Question: "What is 2+2?"},
		Output:   TestOutput{Answer: "4"},
		Expected: TestOutput{Answer: "4"},
	}
	scores2, err := structScorer.Run(ctx, structResult)
	require.NoError(t, err)
	require.Len(t, scores2, 1)
	assert.GreaterOrEqual(t, scores2[0].Score, 0.0)

	// Test with map types
	mapFunctionsAPI := &FunctionsAPI[map[string]any, map[string]any]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	mapScorer, err := mapFunctionsAPI.Scorer(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)

	mapResult := TaskResult[map[string]any, map[string]any]{
		Input:    map[string]any{"key": "value"},
		Output:   map[string]any{"result": "success"},
		Expected: map[string]any{"result": "success"},
	}
	scores3, err := mapScorer.Run(ctx, mapResult)
	require.NoError(t, err)
	require.Len(t, scores3, 1)
	assert.GreaterOrEqual(t, scores3[0].Score, 0.0)
}

// TestFunctionsAPI_EndToEnd_MixedTypes tests a complete evaluation flow with mixed input/output types
func TestFunctionsAPI_EndToEnd_MixedTypes(t *testing.T) {
	apiClient, session := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()
	cfg := &config.Config{
		DefaultProjectName: integrationTestProject,
	}

	// Register project
	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	// Create task function
	taskSlug := tests.RandomName(t, "e2e-task")
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        taskSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	taskFunc, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID: project.ID,
		Name:      "E2E Task",
		Slug:      taskSlug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "system",
						"content": "You answer questions with JSON. Return ONLY the JSON object, no markdown, no code blocks, no backticks.",
					},
					{
						"role":    "user",
						"content": `Question: {{input.question}}. Return ONLY JSON like {"answer": "your answer", "confidence": 0.9}. No markdown formatting.`,
					},
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
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, taskFunc.ID) }()

	// Create scorer function
	scorerSlug := tests.RandomName(t, "e2e-scorer")
	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        scorerSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	scorerFunc, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID:    project.ID,
		Name:         "E2E Scorer",
		Slug:         scorerSlug,
		FunctionType: "scorer",
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"parser": map[string]any{
				"type":          "llm_classifier",
				"use_cot":       false,
				"choice_scores": map[string]any{"incorrect": 0.0, "correct": 1.0},
			},
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{"role": "system", "content": "You are a scorer."},
					{"role": "user", "content": "Is the output.answer field non-empty? Choose 'correct' if yes, 'incorrect' if no."},
				},
			},
			"options": map[string]any{
				"model":  "gpt-4o-mini",
				"params": map[string]any{"temperature": 0},
			},
		},
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, scorerFunc.ID) }()

	// Define types
	type QuestionInput struct {
		Question string `json:"question"`
	}
	type AnswerOutput struct {
		Answer     string  `json:"answer"`
		Confidence float64 `json:"confidence"`
	}

	// Create FunctionsAPI
	functionsAPI := &FunctionsAPI[QuestionInput, AnswerOutput]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	// Get task and scorer
	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: taskSlug})
	require.NoError(t, err)

	scorer, err := functionsAPI.Scorer(ctx, FunctionOpts{Slug: scorerSlug})
	require.NoError(t, err)

	// Create evaluation cases
	cases := NewDataset([]Case[QuestionInput, AnswerOutput]{
		{
			Input:    QuestionInput{Question: "What is the capital of France?"},
			Expected: AnswerOutput{Answer: "Paris", Confidence: 1.0},
		},
		{
			Input:    QuestionInput{Question: "What is 2+2?"},
			Expected: AnswerOutput{Answer: "4", Confidence: 1.0},
		},
	})

	// Create tracer provider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(ctx) }()

	// Run the full evaluation with Functions API task and scorer
	result, err := Run(ctx, Opts[QuestionInput, AnswerOutput]{
		Experiment: tests.RandomName(t, "e2e-exp"),
		Dataset:    cases,
		Task:       task,
		Scorers:    []Scorer[QuestionInput, AnswerOutput]{scorer},
		Quiet:      true,
	}, cfg, session, tp)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.String())
}

// TestFunctionsAPI_Task_PrimitiveTypes tests that tasks can handle primitive types like int, float, bool
func TestFunctionsAPI_Task_PrimitiveTypes(t *testing.T) {
	apiClient, _ := setupAPIIntegrationTest(t)
	t.Parallel()

	ctx := context.Background()
	functions := apiClient.Functions()

	project, err := apiClient.Projects().Create(ctx, projects.CreateParams{Name: integrationTestProject})
	require.NoError(t, err)

	testSlug := tests.RandomName(t, "task-int")

	if existing, _ := functions.Query(ctx, functionsapi.QueryParams{
		ProjectName: integrationTestProject,
		Slug:        testSlug,
		Limit:       1,
	}); len(existing) > 0 {
		_ = functions.Delete(ctx, existing[0].ID)
	}

	// Create a prompt that doubles a number
	function, err := functions.Create(ctx, functionsapi.CreateParams{
		ProjectID: project.ID,
		Name:      "Number Doubler",
		Slug:      testSlug,
		FunctionData: map[string]any{
			"type": "prompt",
		},
		PromptData: map[string]any{
			"prompt": map[string]any{
				"type": "chat",
				"messages": []map[string]any{
					{
						"role":    "user",
						"content": "Double this number: {{input}}. Return ONLY the number, no other text.",
					},
				},
			},
			"options": map[string]any{
				"model": "gpt-4o-mini",
				"params": map[string]any{
					"temperature": 0,
					"max_tokens":  10,
				},
			},
		},
	})
	require.NoError(t, err)
	defer func() { _ = functions.Delete(ctx, function.ID) }()

	// Create FunctionsAPI with int types
	functionsAPI := &FunctionsAPI[int, int]{
		api:         apiClient,
		projectName: integrationTestProject,
	}

	task, err := functionsAPI.Task(ctx, FunctionOpts{Slug: testSlug})
	require.NoError(t, err)
	require.NotNil(t, task)

	// Invoke task with int input
	output, err := task(ctx, 5, nil)
	require.NoError(t, err)
	// Since LLM output is a string that gets converted to int, we just verify it's non-zero
	assert.NotEqual(t, 0, output.Value)
}
