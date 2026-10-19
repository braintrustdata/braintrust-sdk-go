package evalrunner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseSession fails the test if the runner tries to log in. Under
// --no-send-logs nothing may reach Braintrust, and logging in is the first
// thing that would.
func refuseSession(t *testing.T) func(context.Context) (*session, error) {
	t.Helper()
	return func(context.Context) (*session, error) {
		t.Error("runner logged in to Braintrust despite --no-send-logs")
		return nil, assert.AnError
	}
}

// `bt eval --no-send-logs` must keep the run local: no login, no experiment,
// and a summary computed from the local scores.
func TestBatchMode_NoSendLogsRunsLocally(t *testing.T) {
	bt := startFakeBT(t)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_SSE_SOCK":     bt.sockPath,
		"BT_EVAL_NO_SEND_LOGS": "1",
	})
	r.newSession = refuseSession(t)
	registerFoodClassifier(r)

	require.NoError(t, Run(context.Background(), r))
	assert.Equal(t, 0, r.exitCode())

	frames := bt.collected(t)
	names := eventNames(frames)
	assert.NotContains(t, names, "start", "there is no experiment to announce")
	assert.NotContains(t, names, "error")

	var summary summaryEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, frames, "summary").data), &summary))
	assert.Equal(t, "food-classifier", summary.ExperimentName)
	assert.Empty(t, summary.ExperimentID)
	assert.Empty(t, summary.ExperimentURL)
	assert.InDelta(t, 0.5, summary.Scores["exact_match"].Score, 1e-9)
}
