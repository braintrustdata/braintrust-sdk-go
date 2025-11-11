package eval

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// TaskFunc is the signature for evaluation task functions.
// It receives the input, hooks for accessing eval context, and returns a TaskOutput.
type TaskFunc[I, R any] func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error)

// TaskHooks provides access to evaluation context within a task.
// All fields are read-only except for span modification.
type TaskHooks struct {
	// The eval and task spans are included, if you want to add custom attributes or events.
	TaskSpan oteltrace.Span
	EvalSpan oteltrace.Span

	// Readonly fields. These aren't necessarily recommended to be included in the task function,
	// but are available for advanced use cases.
	Expected any      // Not usually used in tasks, so this is untyped
	Metadata Metadata // Case metadata
	Tags     []string // Case tags
}

// TaskOutput wraps the output value from a task.
type TaskOutput[R any] struct {
	Value R

	// UserData allows passing custom application context to scorers.
	// This field is NOT logged and isn't supported outside the context of the Go SDK.
	// Use this for in-process data like database connections, file handles, or metrics.
	UserData any
}

// TaskResult represents the complete result of executing a task on a case.
// This is passed to scorers for evaluation.
type TaskResult[I, R any] struct {
	Input    I        // The case input
	Expected R        // What we expected
	Output   R        // What the task actually returned
	Metadata Metadata // Case metadata

	// UserData is custom application context from the task.
	// This field is NOT logged and isn't supported outside the context of the Go SDK.
	UserData any
}

// T is a convenience function for writing short task functions ([TaskFunc]) that only
// use the input and output and don't need Hooks or other advanced features.
//
//	task := eval.T(func(ctx context.Context, input string) (string, error) {
//		return input, nil
//	})
func T[I, R any](fn func(ctx context.Context, input I) (R, error)) TaskFunc[I, R] {
	return func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error) {
		val, err := fn(ctx, input)
		return TaskOutput[R]{Value: val}, err
	}
}
