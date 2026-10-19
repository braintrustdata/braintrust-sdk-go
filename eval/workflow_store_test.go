package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkflowMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := NewWorkflowEvalMemoryStore()
	value, err := s.Read(ctx, "missing")
	require.NoError(t, err)
	require.Nil(t, value)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, _, err := s.GetOrSet(ctx, "key", []byte("value"))
			require.NoError(t, err)
			require.NoError(t, s.AddToSet(ctx, "members", "one"))
		})
	}
	wg.Wait()
	value, created, err := s.GetOrSet(ctx, "key", []byte("other"))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "value", string(value))
	value[0] = 'X'
	value, err = s.Read(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "value", string(value))
	count, err := s.GetSetSize(ctx, "members")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	input := []byte("updated")
	require.NoError(t, s.Write(ctx, "key", input))
	input[0] = 'X'
	value, err = s.Read(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "updated", string(value))
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Read(ctx, "key")
	require.ErrorIs(t, err, context.Canceled)
}

func TestWorkflowRedisStore(t *testing.T) {
	ctx := context.Background()
	command := startWorkflowRedis(t)
	s, err := NewWorkflowEvalRedisStore(command, "test:", time.Hour)
	require.NoError(t, err)
	value, err := s.Read(ctx, "missing")
	require.NoError(t, err)
	require.Nil(t, value)
	bytes := []byte{0, 1, 255, 2}
	value, created, err := s.GetOrSet(ctx, "key", bytes)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, bytes, value)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, created, err := s.GetOrSet(ctx, "key", []byte("other"))
			require.NoError(t, err)
			require.False(t, created)
			require.NoError(t, s.AddToSet(ctx, "key", "member"))
		})
	}
	wg.Wait()
	size, err := s.GetSetSize(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, 1, size)
	require.NoError(t, s.Write(ctx, "key", []byte("replacement")))
	other, err := NewWorkflowEvalRedisStore(command, "test:", time.Hour)
	require.NoError(t, err)
	value, err = other.Read(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "replacement", string(value))
}

// Use real Redis. No fake server or provider responses are involved.
func startWorkflowRedis(t *testing.T) WorkflowRedisCommand {
	t.Helper()
	server, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for Redis integration tests")
	}
	_, err = exec.LookPath("redis-cli")
	if err != nil {
		t.Skip("redis-cli is required for Redis integration tests")
	}
	base := filepath.Join(os.Getenv("HOME"), ".devbox", "tmp")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "workflow-redis-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	socket := filepath.Join(dir, "redis.sock")
	cmd := exec.Command(server, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	t.Setenv("WORKFLOW_REDIS_SOCKET", socket)
	command := workflowRedisCLI(socket)
	require.Eventually(t, func() bool { _, err := command(context.Background(), "PING"); return err == nil }, 5*time.Second, 20*time.Millisecond)
	return command
}

func TestWorkflowRedisProcessRestart(t *testing.T) {
	ctx := context.Background()
	var command WorkflowRedisCommand
	runID := os.Getenv("WORKFLOW_TEST_RUN")
	if runID == "" {
		command = startWorkflowRedis(t)
	} else {
		command = workflowRedisCLI(os.Getenv("WORKFLOW_REDIS_SOCKET"))
	}
	store, err := NewWorkflowEvalRedisStore(command, "restart:", time.Hour)
	require.NoError(t, err)
	w := WorkflowEval[int, int]{Name: "restart", Store: store, Task: WorkflowTask[int, int]{
		Submit: func(ctx context.Context, item WorkflowTaskItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.Marshal(item.Input)
		},
		Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			var input int
			err := json.Unmarshal(data, &input)
			return WorkflowTaskResult[int]{Output: input * 2}, err
		},
	}, Scorers: []Scorer[int, int]{NewScorer("correct", func(ctx context.Context, r TaskResult[int, int]) (Scores, error) {
		require.Equal(t, r.Input*2, r.Output)
		return S(1), nil
	})}}
	if runID != "" {
		r, err := w.Poll(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, WorkflowRunCompleted, r.Status)
		return
	}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 2}, {Input: 3}})})
	require.NoError(t, err)
	require.Equal(t, WorkflowRunWaiting, r.Status)
	commandLine := exec.Command(os.Args[0], "-test.run=^TestWorkflowRedisProcessRestart$")
	commandLine.Env = append(os.Environ(), "WORKFLOW_TEST_RUN="+r.RunID)
	output, err := commandLine.CombinedOutput()
	require.NoError(t, err, string(output))
	r, err = w.Status(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	require.Equal(t, 1.0, r.Scores["correct"])
}

func workflowRedisCLI(socket string) WorkflowRedisCommand {
	return func(ctx context.Context, args ...any) (any, error) {
		strings := []string{"--json", "-s", socket}
		for _, arg := range args {
			strings = append(strings, fmt.Sprint(arg))
		}
		output, err := exec.CommandContext(ctx, "redis-cli", strings...).Output()
		if err != nil {
			return nil, err
		}
		var result any
		if err = json.Unmarshal(output, &result); err != nil {
			return nil, fmt.Errorf("Redis: %s: %w", output, err)
		}
		return result, nil
	}
}
