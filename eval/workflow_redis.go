package eval

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// WorkflowRedisCommand executes a Redis command and returns its decoded result.
// With go-redis, wrap client.Do(ctx, args...).Result() and translate redis.Nil
// to nil, nil.
// Supplying a command adapter avoids adding a Redis client dependency to the SDK.
// The adapter must support EVAL and return nil, nil for a missing GET.
type WorkflowRedisCommand func(context.Context, ...any) (any, error)

// WorkflowEvalRedisStore persists records in Redis with atomic Lua operations.
// Use a dedicated prefix. TTL must exceed the longest run and webhook retry
// window; it is refreshed on writes, not reads. Expired runs cannot be resumed.
type WorkflowEvalRedisStore struct {
	command WorkflowRedisCommand
	prefix  string
	ttl     int64
}

// NewWorkflowEvalRedisStore creates a Redis store. An empty prefix defaults to
// braintrust-eval:, and a zero TTL defaults to seven days.
func NewWorkflowEvalRedisStore(command WorkflowRedisCommand, prefix string, ttl time.Duration) (*WorkflowEvalRedisStore, error) {
	if command == nil {
		return nil, fmt.Errorf("redis command function is required")
	}
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	if ttl < time.Millisecond {
		return nil, fmt.Errorf("redis TTL must be at least one millisecond")
	}
	if prefix == "" {
		prefix = "braintrust-eval:"
	}
	return &WorkflowEvalRedisStore{command: command, prefix: prefix, ttl: ttl.Milliseconds()}, nil
}

// Read returns a byte record, or nil when absent.
func (s *WorkflowEvalRedisStore) Read(ctx context.Context, key string) ([]byte, error) {
	value, err := s.command(ctx, "GET", s.prefix+"record:"+key)
	if err != nil || value == nil {
		return nil, err
	}
	return decodeWorkflowRedis(value)
}

// Write replaces a record and refreshes its TTL.
func (s *WorkflowEvalRedisStore) Write(ctx context.Context, key string, value []byte) error {
	_, err := s.command(ctx, "SET", s.prefix+"record:"+key, base64.StdEncoding.EncodeToString(value), "PX", s.ttl)
	return err
}

// GetOrSet atomically creates an absent record and returns the stored value.
func (s *WorkflowEvalRedisStore) GetOrSet(ctx context.Context, key string, value []byte) ([]byte, bool, error) {
	const script = `local v=redis.call('GET',KEYS[1]); if v then return {v,0} end; redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2]); return {ARGV[1],1}`
	result, err := s.command(ctx, "EVAL", script, 1, s.prefix+"record:"+key, base64.StdEncoding.EncodeToString(value), s.ttl)
	if err != nil {
		return nil, false, err
	}
	parts, ok := result.([]any)
	if !ok || len(parts) != 2 {
		return nil, false, fmt.Errorf("unexpected Redis get-or-set result %T", result)
	}
	data, err := decodeWorkflowRedis(parts[0])
	if err != nil {
		return nil, false, err
	}
	created, err := workflowRedisInt(parts[1])
	return data, created == 1, err
}

// AddToSet adds a member and refreshes the set's TTL atomically.
func (s *WorkflowEvalRedisStore) AddToSet(ctx context.Context, key, member string) error {
	const script = `redis.call('SADD',KEYS[1],ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[2]); return 1`
	_, err := s.command(ctx, "EVAL", script, 1, s.prefix+"set:"+key, member, s.ttl)
	return err
}

// GetSetSize returns the number of distinct members.
func (s *WorkflowEvalRedisStore) GetSetSize(ctx context.Context, key string) (int, error) {
	result, err := s.command(ctx, "SCARD", s.prefix+"set:"+key)
	if err != nil {
		return 0, err
	}
	return workflowRedisInt(result)
}

func decodeWorkflowRedis(value any) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return base64.StdEncoding.DecodeString(v)
	case []byte:
		return base64.StdEncoding.DecodeString(string(v))
	default:
		return nil, fmt.Errorf("unexpected Redis byte record %T", value)
	}
}
func workflowRedisInt(value any) (int, error) {
	switch v := value.(type) {
	case int64:
		return int(v), nil
	case int:
		return v, nil
	case float64:
		if v == float64(int(v)) {
			return int(v), nil
		}
	case string:
		return strconv.Atoi(v)
	}
	return 0, fmt.Errorf("unexpected Redis integer %v", value)
}
