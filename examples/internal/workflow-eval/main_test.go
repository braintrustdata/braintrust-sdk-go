package main

import (
	"context"
	"math/rand"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// Exercise the actual provider batch API. Replay requires a genuine recording;
// no fake HTTP responses are used when API credentials are unavailable.
func TestWorkflowOpenAIBatches(t *testing.T) {
	if vcr.GetVCRMode() != vcr.ModeReplay && os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is required to record provider batches")
	}
	// Stable custom IDs let recorded provider responses correlate to replayed cases.
	// This test must not run in parallel with other UUID consumers.
	uuid.SetRand(rand.New(rand.NewSource(297)))
	t.Cleanup(func() { uuid.SetRand(nil) })
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		key = "vcr-dummy-key"
	}
	client := openai.NewClient(option.WithAPIKey(key), option.WithHTTPClient(vcr.WrapHTTPClient(t, &http.Client{Timeout: time.Minute})), option.WithMaxRetries(0))
	task, scorer := batchStages(client, false)
	store := eval.NewWorkflowEvalMemoryStore()
	definition := func() *eval.WorkflowEval[string, string] {
		return &eval.WorkflowEval[string, string]{Name: "provider-batches", Store: store, Task: task, WorkflowScorers: []eval.WorkflowScorerDefinition[string, string]{scorer}}
	}
	// Follow go test -timeout, leaving time for VCR to save the recording.
	// With -timeout 0, the caller is responsible for stopping the live test.
	ctx := context.Background()
	cancel := func() {}
	if deadline, ok := t.Deadline(); ok {
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
	}
	defer cancel()
	result, err := definition().Start(ctx, eval.WorkflowStartOpts[string, string]{NoSendLogs: true, Dataset: eval.NewDataset([]eval.Case[string, string]{{Input: "What is 2 + 2? Reply with the number only.", Expected: "4"}, {Input: "What is 3 + 3? Reply with the number only.", Expected: "6"}})})
	require.NoError(t, err)
	t.Logf("Started workflow %s; waiting for task and scorer batches", result.RunID)
	for result.Status != eval.WorkflowRunCompleted {
		if vcr.GetVCRMode() != vcr.ModeReplay {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Minute):
			}
		}
		result, err = definition().Poll(ctx, result.RunID)
		require.NoError(t, err)
	}
	require.Equal(t, 2, result.Completed)
	require.Zero(t, result.Failed)
	require.Equal(t, 1.0, result.Scores["correctness"])
}
