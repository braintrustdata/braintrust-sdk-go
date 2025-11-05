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
	Expected any            // Expected output (type-assert when needed)
	Metadata Metadata       // Case metadata (read-only)
	Tags     []string       // Case tags (read-only)
	TaskSpan oteltrace.Span // Current task execution span (can modify)
	EvalSpan oteltrace.Span // Parent case/eval span (can modify)
}

// TaskOutput wraps the output value from a task.
// Future fields may include metadata, skip flags, etc.
type TaskOutput[R any] struct {
	Value R
}

// TaskResult represents the complete result of executing a task on a case.
// This is passed to scorers for evaluation.
type TaskResult[I, R any] struct {
	Input    I        // The case input
	Expected R        // What we expected
	Output   R        // What the task actually returned
	Metadata Metadata // Case metadata
}

// T is a simple adapter that converts a basic task function into a TaskFunc.
// Use this when you don't need access to TaskHooks.
func T[I, R any](fn func(ctx context.Context, input I) (R, error)) TaskFunc[I, R] {
	return func(ctx context.Context, input I, hooks *TaskHooks) (TaskOutput[R], error) {
		val, err := fn(ctx, input)
		return TaskOutput[R]{Value: val}, err
	}
}
