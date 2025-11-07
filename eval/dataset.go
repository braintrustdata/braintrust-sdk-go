package eval

import "io"

// NewDataset creates a Dataset iterator from a slice of cases.
func NewDataset[I, R any](cases []Case[I, R]) Dataset[I, R] {
	return &sliceCases[I, R]{
		cases: cases,
		index: 0,
	}
}

// sliceCases implements the Dataset interface for a slice of cases.
type sliceCases[I, R any] struct {
	cases []Case[I, R]
	index int
}

// Next returns the next case, or io.EOF if there are no more cases.
func (s *sliceCases[I, R]) Next() (Case[I, R], error) {
	if s.index >= len(s.cases) {
		var zero Case[I, R]
		return zero, io.EOF
	}

	c := s.cases[s.index]
	s.index++
	return c, nil
}

// ID returns empty string for literal in-memory cases.
func (s *sliceCases[I, R]) ID() string {
	return ""
}

// Version returns empty string for literal in-memory cases.
func (s *sliceCases[I, R]) Version() string {
	return ""
}
