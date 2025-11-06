package eval

import (
	"context"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
)

// ScorerAPI provides methods for loading scorers for evaluation.
type ScorerAPI[I, R any] struct {
	api         *api.API
	projectName string
}

// ScorerInfo contains metadata about a scorer.
type ScorerInfo struct {
	ID      string
	Name    string
	Project string
}

// ScorerQueryOpts contains options for querying scorers.
type ScorerQueryOpts struct {
	// Project filters scorers by project
	Project string

	// Name filters by specific name
	Name string
}

// Get loads a scorer by slug and returns a Scorer.
func (s *ScorerAPI[I, R]) Get(ctx context.Context, slug string) (Scorer[I, R], error) {
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	// Query for the function/scorer
	functions, err := s.api.Functions().Query(ctx, functionsapi.QueryParams{
		ProjectName: s.projectName,
		Slug:        slug,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, fmt.Errorf("scorer not found: project=%s slug=%s", s.projectName, slug)
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
		output, err := s.api.Functions().Invoke(ctx, function.ID, scorerInput)
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

// Query searches for scorers matching the given options.
func (s *ScorerAPI[I, R]) Query(ctx context.Context, opts ScorerQueryOpts) ([]ScorerInfo, error) {
	// Get project name from opts or use default
	projectName := opts.Project
	if projectName == "" {
		projectName = s.projectName
	}

	// Query for functions
	functions, err := s.api.Functions().Query(ctx, functionsapi.QueryParams{
		ProjectName:  projectName,
		FunctionName: opts.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query functions: %w", err)
	}

	// Convert to ScorerInfo
	result := make([]ScorerInfo, len(functions))
	for i, fn := range functions {
		result[i] = ScorerInfo{
			ID:      fn.ID,
			Name:    fn.Name,
			Project: projectName,
		}
	}

	return result, nil
}
