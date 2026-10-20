package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// FunctionsAPI provides access for executing tasks and scorers hosted at braintrust.dev.
type FunctionsAPI[I, R any] struct {
	api         *openapi.Client
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

	functions, err := f.queryFunctions(ctx, projectName, opts)
	if err != nil {
		return nil, err
	}
	if len(functions.Objects) == 0 {
		return nil, fmt.Errorf("function not found: project=%s slug=%s", projectName, opts.Slug)
	}

	function := functions.Objects[0]

	// Return a TaskFunc that invokes the function
	return func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error) {
		// Invoke the function
		output, err := invokeFunction(ctx, f.api, function.ID, input)
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

	functions, err := f.queryFunctions(ctx, projectName, opts)
	if err != nil {
		return nil, err
	}
	if len(functions.Objects) == 0 {
		return nil, fmt.Errorf("scorer not found: project=%s slug=%s", projectName, opts.Slug)
	}

	function := functions.Objects[0]

	// Create a scorer that invokes the function
	scorerFunc := func(ctx context.Context, result TaskResult[I, R]) (Scores, error) {
		// Build scorer input
		scorerInput := map[string]any{
			"input":    result.Input,
			"output":   result.Output,
			"expected": result.Expected,
		}

		// Invoke the scorer function
		output, err := invokeFunction(ctx, f.api, function.ID, scorerInput)
		if err != nil {
			return nil, fmt.Errorf("failed to invoke scorer: %w", err)
		}

		if bytes.Equal(output, []byte("null")) {
			return nil, fmt.Errorf("scorer returned nil")
		}

		// Decode scores directly, without converting arbitrary metadata through
		// generated float32/any fields.
		if len(output) > 0 && output[0] == '{' {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(output, &fields); err != nil {
				return nil, fmt.Errorf("failed to decode scorer output: %w", err)
			}
			score := Score{}
			// As before, unrecognized field types leave the corresponding
			// score field at its zero value.
			_ = json.Unmarshal(fields["name"], &score.Name)
			_ = json.Unmarshal(fields["score"], &score.Score)
			_ = json.Unmarshal(fields["metadata"], &score.Metadata)
			return Scores{score}, nil
		}

		var score float64
		if err := json.Unmarshal(output, &score); err == nil {
			return Scores{{Score: score}}, nil
		}
		return nil, fmt.Errorf("scorer output type mismatch: expected map or number, got %s", output)
	}

	return NewScorer(function.Name, scorerFunc), nil
}

// functionList decodes only the hosted function metadata used by tasks and scorers.
type functionList struct {
	Objects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"objects"`
}

func (f *FunctionsAPI[I, R]) queryFunctions(ctx context.Context, projectName string, opts FunctionOpts) (functionList, error) {
	functions, err := https.DecodeJSON[functionList](f.api.GetFunction(ctx, &openapi.GetFunctionParams{ //nolint:bodyclose // false positive: DecodeJSON closes the body
		ProjectName: https.String(projectName),
		Slug:        https.String(opts.Slug),
		Version:     https.String(opts.Version),
		Environment: https.String(opts.Environment),
		Limit:       https.Positive(1),
	}))
	if err != nil {
		return functionList{}, fmt.Errorf("failed to query function: %w", err)
	}
	return functions, nil
}

// invokeFunction keeps SDK tracing and output-envelope semantics above the
// generated operation. Raw JSON allows the caller to decode its own numeric types.
func invokeFunction(ctx context.Context, client *openapi.Client, functionID string, input any) (json.RawMessage, error) {
	ctx, span := otel.Tracer("braintrust-functions").Start(ctx, "function.invoke")
	defer span.End()
	span.SetAttributes(attribute.String("function.id", functionID))
	if functionID == "" {
		return nil, fmt.Errorf("function ID is required")
	}
	body, err := json.Marshal(struct {
		Input any `json:"input"`
	}{Input: input})
	if err != nil {
		return nil, fmt.Errorf("failed to encode function input: %w", err)
	}
	output, err := https.DecodeJSON[json.RawMessage](client.PostFunctionIdInvokeWithBody(ctx, functionID, "application/json", bytes.NewReader(body))) //nolint:bodyclose // false positive: DecodeJSON closes the body
	if err != nil {
		return nil, err
	}
	if len(output) > 0 && output[0] == '{' {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(output, &envelope); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		if value, ok := envelope["output"]; ok {
			return value, nil
		}
	}
	return output, nil
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

	if raw, ok := output.(json.RawMessage); ok {
		// Strings may themselves contain JSON from an LLM. Keep the existing
		// string conversion rules, while decoding all other values directly.
		if len(raw) > 0 && raw[0] == '"' {
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return zero, fmt.Errorf("failed to decode function output: %w", err)
			}
			return convertToType[R](text)
		}
		if err := json.Unmarshal(raw, &zero); err != nil {
			return zero, fmt.Errorf("failed to unmarshal result to type %T: %w", zero, err)
		}
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
