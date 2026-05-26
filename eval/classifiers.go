package eval

import (
	"context"
)

// Classifier is an interface for categorizing the output of a task.
//
// Unlike [Scorer], which returns numeric scores, a Classifier returns one or
// more [Classification] items with structured id/label/metadata.
type Classifier[I, R any] interface {
	// Name returns the name of this classifier.
	Name() string
	// Run categorizes the task result.
	// It returns zero or more Classification results.
	Run(context.Context, TaskResult[I, R]) (Classifications, error)
}

// Classification represents a single category result returned by a Classifier.
type Classification struct {
	// Name is the grouping key for this classification.
	// If empty, it defaults to the name of the Classifier that produced it.
	Name string

	// ID is the stable identifier for the category (required).
	ID string

	// Label is an optional human-readable label.
	Label string

	// Metadata is optional additional metadata for this classification.
	Metadata map[string]interface{}
}

// Classifications is a collection of Classification results returned by classifiers.
type Classifications = []Classification

// C is a helper to concisely return a single classification from a classifier.
// The Name defaults to the classifier's name when empty.
//
//	return eval.C("positive"), nil
func C(id string) Classifications {
	return Classifications{{ID: id}}
}

// ClassifyFunc is a function that classifies a task result and returns a list of Classifications.
type ClassifyFunc[I, R any] func(ctx context.Context, result TaskResult[I, R]) (Classifications, error)

// NewClassifier creates a new classifier with the given name and classify function.
func NewClassifier[I, R any](name string, fn ClassifyFunc[I, R]) Classifier[I, R] {
	return &classifierImpl[I, R]{
		name: name,
		fn:   fn,
	}
}

type classifierImpl[I, R any] struct {
	name string
	fn   ClassifyFunc[I, R]
}

func (c *classifierImpl[I, R]) Name() string {
	return c.name
}

func (c *classifierImpl[I, R]) Run(ctx context.Context, result TaskResult[I, R]) (Classifications, error) {
	return c.fn(ctx, result)
}
