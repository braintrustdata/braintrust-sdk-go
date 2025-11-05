package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/braintrustdata/braintrust-sdk-go/api"
)

// TaskAPI provides methods for loading tasks/prompts for evaluation.
type TaskAPI[I, R any] struct {
	api         *api.API
	projectName string
}

// TaskInfo contains metadata about a task/prompt.
type TaskInfo struct {
	ID      string
	Name    string
	Slug    string
	Project string
}

// TaskQueryOpts contains options for querying tasks.
type TaskQueryOpts struct {
	// Project filters tasks by project
	Project string

	// Slug filters by specific slug
	Slug string

	// Version specifies a specific version
	Version string
}

// Get loads a task/prompt by slug and returns a TaskFunc.
func (t *TaskAPI[I, R]) Get(ctx context.Context, slug string) (TaskFunc[I, R], error) {
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	// Query for the function/prompt
	functions, err := t.api.Functions().Query(ctx, api.FunctionQueryOpts{
		ProjectName: t.projectName,
		Slug:        slug,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, fmt.Errorf("function not found: project=%s slug=%s", t.projectName, slug)
	}

	function := functions[0]

	// Return a TaskFunc that invokes the function
	return func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error) {
		// Invoke the function
		output, err := t.api.Functions().Invoke(ctx, function.ID, input)
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

// Query searches for tasks matching the given options.
func (t *TaskAPI[I, R]) Query(ctx context.Context, opts TaskQueryOpts) ([]TaskInfo, error) {
	// TODO: Implement
	panic("not implemented")
}

// convertToType converts the function output to the expected type R.
// This is copied from the working implementation in braintrust/eval/functions/functions.go
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
