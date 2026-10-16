package eval

import (
	"bytes"
	"context"
	"sync"
)

// WorkflowEvalStore persists workflow records. A missing Read returns nil, nil.
// GetOrSet and AddToSet must be atomic. Byte records and sets use separate key
// spaces. Implementations must copy bytes and retain sets as long as records.
// Callers must serialize advancement of a run across processes; these primitives
// do not provide a distributed execution lock.
// Experimental: this API may change in non-major releases.
type WorkflowEvalStore interface {
	Read(context.Context, string) ([]byte, error)
	Write(context.Context, string, []byte) error
	GetOrSet(context.Context, string, []byte) (value []byte, created bool, err error)
	AddToSet(context.Context, string, string) error
	GetSetSize(context.Context, string) (int, error)
}

// WorkflowEvalMemoryStore stores workflow state in memory. It is safe for
// concurrent access, but loses all records when its process exits.
type WorkflowEvalMemoryStore struct {
	mu     sync.Mutex
	values map[string][]byte
	sets   map[string]map[string]struct{}
}

// NewWorkflowEvalMemoryStore creates an empty in-memory workflow store.
func NewWorkflowEvalMemoryStore() *WorkflowEvalMemoryStore {
	return &WorkflowEvalMemoryStore{values: make(map[string][]byte), sets: make(map[string]map[string]struct{})}
}

// Read returns a copy of the stored value, or nil when absent.
func (s *WorkflowEvalMemoryStore) Read(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.values[key]), nil
}

// Write replaces a byte record.
func (s *WorkflowEvalMemoryStore) Write(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = bytes.Clone(value)
	return nil
}

// GetOrSet atomically creates a record if absent and returns its stored value.
func (s *WorkflowEvalMemoryStore) GetOrSet(ctx context.Context, key string, value []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.values[key]; ok {
		return bytes.Clone(old), false, nil
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = bytes.Clone(value)
	return bytes.Clone(value), true, nil
}

// AddToSet atomically adds a unique member.
func (s *WorkflowEvalMemoryStore) AddToSet(ctx context.Context, key, member string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sets == nil {
		s.sets = make(map[string]map[string]struct{})
	}
	if s.sets[key] == nil {
		s.sets[key] = make(map[string]struct{})
	}
	s.sets[key][member] = struct{}{}
	return nil
}

// GetSetSize returns the number of unique members.
func (s *WorkflowEvalMemoryStore) GetSetSize(ctx context.Context, key string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sets[key]), nil
}
