package eval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/logger"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// Records the real Braintrust insert/fetch lifecycle. This test intentionally
// does not substitute a fake HTTP server when credentials/cassettes are absent.
func TestWorkflowExperimentIntegration(t *testing.T) {
	ctx := context.Background()
	httpClient := vcr.GetHTTPSClient(t)
	apiClient := api.NewWithHTTPSClient(httpClient)
	session := auth.NewTestSession(auth.TestAPIKey, "org-test", "Test Org", "https://api.braintrust.dev", "https://www.braintrust.dev", "https://www.braintrust.dev", logger.NewFailTestLogger(t))
	evaluator := NewEvaluator[string, string](session, nil, apiClient, integrationTestProject)
	store := NewWorkflowEvalMemoryStore()
	makeDefinition := func() *WorkflowEval[string, string] {
		return NewWorkflowEval(evaluator, &WorkflowEval[string, string]{Name: t.Name(), Store: store,
			Task: WorkflowTask[string, string]{
				Submit: func(ctx context.Context, item WorkflowTaskItem[string, string], info WorkflowSubmissionContext) (json.RawMessage, error) {
					return json.Marshal(item.Input)
				},
				Completion: WorkflowCompletion{GetExternalID: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (string, error) {
					return "workflow-integration-completion", nil
				}},
				Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowTaskResult[string], error) {
					var input string
					err := json.Unmarshal(data, &input)
					return WorkflowTaskResult[string]{Output: input + "!"}, err
				},
			}, Scorers: []Scorer[string, string]{NewScorer("correct", func(ctx context.Context, r TaskResult[string, string]) (Scores, error) {
				require.Equal(t, "hello!", r.Output)
				return S(1), nil
			})},
		})
	}
	w := makeDefinition()
	result, err := w.Start(ctx, WorkflowStartOpts[string, string]{Dataset: NewDataset([]Case[string, string]{{ID: "greeting", Input: "hello", Expected: "hello!"}})})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, apiClient.Experiments().Delete(ctx, result.ExperimentID)) })
	fetch := func() []map[string]any {
		t.Helper()
		resp, err := httpClient.POST(ctx, "/v1/experiment/"+result.ExperimentID+"/fetch", map[string]any{"limit": 100})
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		var body struct {
			Events []map[string]any `json:"events"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Events
	}
	initial := fetch()
	require.Len(t, initial, 1)
	w = makeDefinition()
	result, err = w.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: "workflow-integration-completion"})
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, result.Status)
	require.Equal(t, "https://www.braintrust.dev/app/Test%20Org/object?object_type=experiment&object_id="+result.ExperimentID, result.Permalink)
	// A repeated notification changes no rows, so it uploads nothing. The
	// cassette records no insert for it, so replay fails if one is sent.
	_, err = w.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: "workflow-integration-completion"})
	require.NoError(t, err)
	final := fetch()
	require.Len(t, final, 1)
	require.Equal(t, initial[0]["id"], final[0]["id"])
	require.Equal(t, initial[0]["span_id"], final[0]["span_id"])
	require.Equal(t, "hello!", final[0]["output"])
	metadata, ok := final[0]["metadata"].(map[string]any)
	require.True(t, ok)
	workflow, ok := metadata["workflow_eval"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "greeting", workflow["case_id"])
	require.NotEqual(t, "greeting", final[0]["id"], "rows are keyed per trial")
	scores, ok := final[0]["scores"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 1.0, scores["correct"])
}
