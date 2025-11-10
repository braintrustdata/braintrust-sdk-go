package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
)

// FunctionsAPI provides access for executing tasks and scorers hosted at braintrust.dev.
type FunctionsAPI[I, R any] struct {
	api         *api.API
	projectName string
}

// FunctionOpts contains options for loading functions.
type FunctionOpts struct {
	// Slug is the function slug (required)
	Slug string

	// Project overrides the default project name (optional)
	Project string

	// Version pins to a specific function version (optional, e.g., "5878bd218351fb8e")
	Version string

	// Environment specifies the deployment environment (optional, e.g., "dev", "staging", "production")
	Environment string
}

// Task loads a server-side task/prompt and returns a TaskFunc.
// The returned function, when called, will invoke the Braintrust function remotely.
func (f *FunctionsAPI[I, R]) Task(ctx context.Context, opts FunctionOpts) (TaskFunc[I, R], error) {
	if opts.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	// Build query params
	projectName := f.projectName
	if opts.Project != "" {
		projectName = opts.Project
	}

	// Query for the function/prompt
	functions, err := f.api.Functions().Query(ctx, functionsapi.QueryParams{
		ProjectName: projectName,
		Slug:        opts.Slug,
		Version:     opts.Version,
		Environment: opts.Environment,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, fmt.Errorf("function not found: project=%s slug=%s", projectName, opts.Slug)
	}

	function := functions[0]

	// Return a TaskFunc that invokes the function
	return func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error) {
		// Invoke the function
		output, err := f.api.Functions().Invoke(ctx, function.ID, input)
		if err != nil {
			return TaskOutput[R]{}, fmt.Errorf("failed to invoke function: %w", err)
		}

		// Convert output to R with robust type conversion
		result, err := convertToType[R](output)
		if err != nil {
			return TaskOutput[R]{}, err
		}

		return TaskOutput[R]{
			Value: result,
		}, nil
	}, nil
}

// Scorer loads a server-side scorer and returns a Scorer.
// The returned scorer, when called, will invoke the Braintrust scorer function remotely.
func (f *FunctionsAPI[I, R]) Scorer(ctx context.Context, opts FunctionOpts) (Scorer[I, R], error) {
	if opts.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	// Build query params
	projectName := f.projectName
	if opts.Project != "" {
		projectName = opts.Project
	}

	// Query for the function/scorer
	functions, err := f.api.Functions().Query(ctx, functionsapi.QueryParams{
		ProjectName: projectName,
		Slug:        opts.Slug,
		Version:     opts.Version,
		Environment: opts.Environment,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, fmt.Errorf("scorer not found: project=%s slug=%s", projectName, opts.Slug)
	}

	function := functions[0]

	// Create a scorer that invokes the function
	scorerFunc := func(ctx context.Context, result TaskResult[I, R]) (Scores, error) {
		// Build scorer input
		scorerInput := map[string]any{
			"input":    result.Input,
			"output":   result.Output,
			"expected": result.Expected,
		}

		// Invoke the scorer function
		output, err := f.api.Functions().Invoke(ctx, function.ID, scorerInput)
		if err != nil {
			return nil, fmt.Errorf("failed to invoke scorer: %w", err)
		}

		// Convert result to Scores
		// The scorer should return a score (number) or a struct with name/score
		if output == nil {
			return nil, fmt.Errorf("scorer returned nil")
		}

		// Try to parse as map first (most common case)
		if resultMap, ok := output.(map[string]any); ok {
			score := Score{}
			if name, ok := resultMap["name"].(string); ok {
				score.Name = name
			}
			if scoreVal, ok := resultMap["score"].(float64); ok {
				score.Score = scoreVal
			}
			if metadata, ok := resultMap["metadata"].(map[string]any); ok {
				score.Metadata = metadata
			}
			return Scores{score}, nil
		}

		// Try to parse as a number (simple score)
		if scoreVal, ok := output.(float64); ok {
			return Scores{{Score: scoreVal}}, nil
		}

		return nil, fmt.Errorf("scorer output type mismatch: expected map or number, got %T", output)
	}

	return NewScorer(function.Name, scorerFunc), nil
}

// convertToType converts the function output to the expected type R.
// It handles various conversion scenarios:
// - Direct type assertion (for matching types)
// - String to JSON struct (parse string as JSON)
// - String to string type (including custom string types)
// - Map to struct (marshal/unmarshal through JSON)
func convertToType[R any](output any) (R, error) {
	var zero R

	if output == nil {
		return zero, nil
	}

	// Try direct type assertion first (works for simple types like string, int, etc.)
	typedResult, ok := output.(R)
	if ok {
		return typedResult, nil
	}

	// For complex types (structs) or type mismatches, we need to convert via JSON
	// If result is a string, it might be a JSON string that needs parsing
	// This handles cases where the LLM returns JSON as a string
	if resultStr, ok := output.(string); ok {
		// Try to unmarshal the string as JSON
		if err := json.Unmarshal([]byte(resultStr), &zero); err != nil {
			// If unmarshaling fails and R is string type (including custom string types),
			// return the string as-is. This handles cases where GetTask[string, string]
			// or GetTask[CustomString, CustomString] receives a plain string.
			// Use reflection to check if the underlying type is string to support type aliases.
			if reflect.TypeOf(zero).Kind() == reflect.String {
				// Use reflection to convert the string to the target type (handles custom string types)
				resultValue := reflect.ValueOf(resultStr)
				typedValue := resultValue.Convert(reflect.TypeOf(zero))
				typedResult, ok := typedValue.Interface().(R)
				if !ok {
					return zero, fmt.Errorf("failed to convert string to type %T", zero)
				}
				return typedResult, nil
			}
			return zero, fmt.Errorf("failed to unmarshal JSON string to type %T: %w", zero, err)
		}
		return zero, nil
	}

	// Otherwise, result is likely a map[string]any from JSON parsing
	// Marshal and unmarshal to convert to the target type
	jsonBytes, err := json.Marshal(output)
	if err != nil {
		return zero, fmt.Errorf("failed to marshal result to JSON: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, &zero); err != nil {
		return zero, fmt.Errorf("failed to unmarshal result to type %T: %w", zero, err)
	}

	return zero, nil
}
