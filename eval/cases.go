package eval

import "io"

// NewCases creates a Cases iterator from a slice of cases.
// This is a convenience function for the common case of having all cases in memory.
func NewCases[I, R any](cases []Case[I, R]) Cases[I, R] {
	return &sliceCases[I, R]{
		cases: cases,
		index: 0,
	}
}

// sliceCases implements the Cases interface for a slice of cases.
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
