package eval

import (
	"context"
)

// Scorer is an interface for scoring the output of a task.
type Scorer[I, R any] interface {
	// Name returns the name of this scorer.
	Name() string
	// Run evaluates the task result.
	// It returns one or more Score results.
	Run(context.Context, TaskResult[I, R]) (Scores, error)
}

// Score represents a single score result.
type Score struct {
	// Name is the name of the score (e.g., "accuracy", "exact_match").
	Name string

	// Score is the numeric score value.
	Score float64

	// Metadata is optional additional metadata for this score.
	Metadata map[string]interface{}
}

// Scores is a collection of Score results returned by scorers.
type Scores = []Score

// S is a helper function to concisely return a single score from scorers.
// Scores created with S will default to the name of the scorer that creates them.
// S(0.5) is equivalent to Scores{{Score: 0.5}}.
func S(score float64) Scores {
	return Scores{{Name: "", Score: score}}
}

// ScoreFunc is a function that evaluates a task result and returns a list of Scores.
type ScoreFunc[I, R any] func(ctx context.Context, result TaskResult[I, R]) (Scores, error)

// NewScorer creates a new scorer with the given name and score function.
func NewScorer[I, R any](name string, scoreFunc ScoreFunc[I, R]) Scorer[I, R] {
	return &scorerImpl[I, R]{
		name:      name,
		scoreFunc: scoreFunc,
	}
}

type scorerImpl[I, R any] struct {
	name      string
	scoreFunc ScoreFunc[I, R]
}

func (s *scorerImpl[I, R]) Name() string {
	return s.name
}

func (s *scorerImpl[I, R]) Run(ctx context.Context, result TaskResult[I, R]) (Scores, error) {
	return s.scoreFunc(ctx, result)
}
