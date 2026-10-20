package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// These tests exercise the orchestration state machine with local callbacks;
// they do not simulate a provider HTTP API. Provider integration uses VCR.
func TestWorkflowBatchResume(t *testing.T) {
	ctx := context.Background()
	store := NewWorkflowEvalMemoryStore()
	submits := 0
	var batches [][]WorkflowBatchItem[WorkflowTaskItem[string, string]]
	task := WorkflowBatchTask[string, string]{
		Batching: WorkflowBatching{MaxSize: 2},
		Submit: func(ctx context.Context, items []WorkflowBatchItem[WorkflowTaskItem[string, string]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			submits++
			batches = append(batches, items)
			return json.Marshal(info.SubmissionID)
		},
		Completion: WorkflowCompletion{GetExternalID: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (string, error) {
			var id string
			err := json.Unmarshal(data, &id)
			return id, err
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(WorkflowBatchResult[WorkflowTaskResult[string]]) error) error {
			for _, batch := range batches {
				if batch[0].CustomID != info.SubmissionID+"_0" {
					continue
				}
				for i := len(batch) - 1; i >= 0; i-- {
					if err := yield(WorkflowBatchResult[WorkflowTaskResult[string]]{CustomID: batch[i].CustomID, Result: WorkflowTaskResult[string]{Output: batch[i].Item.Input + "!"}}); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	definition := WorkflowEval[string, string]{Name: "resume", Store: store, Task: task, MaxConcurrency: 1,
		Scorers: []Scorer[string, string]{NewScorer("correct", func(ctx context.Context, r TaskResult[string, string]) (Scores, error) {
			require.Equal(t, r.Input+"!", r.Output)
			return S(1), nil
		})},
	}
	result, err := definition.Start(ctx, WorkflowStartOpts[string, string]{NoSendLogs: true, Dataset: NewDataset([]Case[string, string]{{Input: "a"}, {Input: "b"}, {Input: "c"}})})
	require.NoError(t, err)
	require.Equal(t, 2, submits)
	require.Equal(t, WorkflowRunWaiting, result.Status)
	// Reconstruct the definition: no execution state lives on the evaluator.
	resumed := definition
	for _, batch := range batches {
		id := batch[0].CustomID[:len(batch[0].CustomID)-2]
		result, err = resumed.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: id})
		require.NoError(t, err)
		_, err = resumed.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: id})
		require.NoError(t, err)
	}
	require.Equal(t, WorkflowRunCompleted, result.Status)
	require.Equal(t, 3, result.Completed)
	require.Equal(t, 1.0, result.Scores["correct"])
	require.Equal(t, 2, submits)
}

func TestWorkflowCollectionCheckpoint(t *testing.T) {
	ctx := context.Background()
	var items []WorkflowBatchItem[WorkflowTaskItem[int, int]]
	interrupted := true
	w := WorkflowEval[int, int]{Name: "checkpoint", Store: NewWorkflowEvalMemoryStore(), Task: WorkflowBatchTask[int, int]{
		Batching: WorkflowBatching{MaxSize: 3},
		Submit: func(ctx context.Context, batch []WorkflowBatchItem[WorkflowTaskItem[int, int]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			items = batch
			return json.RawMessage(`{}`), nil
		},
		Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(WorkflowBatchResult[WorkflowTaskResult[int]]) error) error {
			if err := yield(WorkflowBatchResult[WorkflowTaskResult[int]]{CustomID: items[0].CustomID, Result: WorkflowTaskResult[int]{Output: 10}}); err != nil {
				return err
			}
			if interrupted {
				return errors.New("connection interrupted")
			}
			return yield(WorkflowBatchResult[WorkflowTaskResult[int]]{CustomID: items[1].CustomID, Error: errors.New("item failed")})
		},
	}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: 2}, {Input: 3}})})
	require.NoError(t, err)
	_, err = w.Poll(ctx, r.RunID)
	require.ErrorContains(t, err, "connection interrupted")
	interrupted = false
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	require.Equal(t, 2, r.Failed)
	require.Equal(t, 3, r.Completed)
}

func TestWorkflowValidation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		batching        WorkflowBatching
		missingCallback string
		wantError       string
	}{
		{name: "missing submit", batching: WorkflowBatching{MaxSize: 1}, missingCallback: "submit", wantError: "submit and collect callbacks are required"},
		{name: "missing collect", batching: WorkflowBatching{MaxSize: 1}, missingCallback: "collect", wantError: "submit and collect callbacks are required"},
		{name: "zero batch size", batching: WorkflowBatching{MaxSize: 0}, wantError: "batch size must be positive"},
		{name: "negative wait", batching: WorkflowBatching{MaxSize: 1, MaxWait: -time.Second}, wantError: "wait nonnegative"},
		{name: "task max wait", batching: WorkflowBatching{MaxSize: 2, MaxWait: time.Second}, wantError: "MaxWait applies only to batch scorers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := WorkflowBatchTask[int, int]{
				Batching: tc.batching,
				Submit: func(context.Context, []WorkflowBatchItem[WorkflowTaskItem[int, int]], WorkflowSubmissionContext) (json.RawMessage, error) {
					return json.RawMessage(`{}`), nil
				},
				Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
					return WorkflowPollResult{Status: WorkflowComplete}, nil
				}},
				Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext, func(WorkflowBatchResult[WorkflowTaskResult[int]]) error) error {
					return nil
				},
			}
			switch tc.missingCallback {
			case "submit":
				task.Submit = nil
			case "collect":
				task.Collect = nil
			}
			w := WorkflowEval[int, int]{Name: "validation", Store: NewWorkflowEvalMemoryStore(), Task: task}
			_, err := w.Start(context.Background(), WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
			require.ErrorContains(t, err, tc.wantError)
		})
	}
}

func TestWorkflowDuplicateResults(t *testing.T) {
	var item WorkflowBatchItem[WorkflowTaskItem[int, int]]
	w := WorkflowEval[int, int]{Name: "duplicate", Store: NewWorkflowEvalMemoryStore(), Task: WorkflowBatchTask[int, int]{
		Batching: WorkflowBatching{MaxSize: 1},
		Submit: func(ctx context.Context, items []WorkflowBatchItem[WorkflowTaskItem[int, int]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			item = items[0]
			return json.RawMessage(`{}`), nil
		},
		Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(WorkflowBatchResult[WorkflowTaskResult[int]]) error) error {
			result := WorkflowBatchResult[WorkflowTaskResult[int]]{CustomID: item.CustomID, Result: WorkflowTaskResult[int]{Output: 2}}
			if err := yield(result); err != nil {
				return err
			}
			return yield(result)
		},
	}}
	r, err := w.Start(context.Background(), WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
	require.NoError(t, err)
	_, err = w.Poll(context.Background(), r.RunID)
	require.ErrorContains(t, err, "duplicate")
}

func TestWorkflowMixedScorerBatches(t *testing.T) {
	for _, maxWait := range []time.Duration{0, time.Nanosecond} {
		t.Run(maxWait.String(), func(t *testing.T) {
			ctx := context.Background()
			taskCalls := 0
			scoreCalls := 0
			taskReady := map[string]bool{}
			taskInputs := map[string]int{}
			batches := map[string][]WorkflowBatchItem[WorkflowScorerItem[int, int]]{}
			w := WorkflowEval[int, int]{Name: "mixed", Store: NewWorkflowEvalMemoryStore(), MaxConcurrency: 1,
				Task: WorkflowTask[int, int]{
					Submit: func(ctx context.Context, item WorkflowTaskItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
						taskCalls++
						taskInputs[info.SubmissionID] = item.Input
						taskReady[info.SubmissionID] = item.Input == 1
						return json.Marshal(info.SubmissionID)
					},
					Completion: WorkflowCompletion{Poll: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowPollResult, error) {
						if taskReady[info.SubmissionID] {
							return WorkflowPollResult{Status: WorkflowComplete}, nil
						}
						return WorkflowPollResult{Status: WorkflowPending}, nil
					}},
					Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
						return WorkflowTaskResult[int]{Output: taskInputs[info.SubmissionID] * 2, Metadata: Metadata{"collected": true}, Tags: []string{"done"}}, nil
					},
				},
				WorkflowScorers: []WorkflowScorerDefinition[int, int]{WorkflowBatchScorer[int, int]{Name: "batch", Batching: WorkflowBatching{MaxSize: 2, MaxWait: maxWait},
					Submit: func(ctx context.Context, items []WorkflowBatchItem[WorkflowScorerItem[int, int]], info WorkflowSubmissionContext) (json.RawMessage, error) {
						scoreCalls++
						batches[info.SubmissionID] = items
						return json.Marshal(info.SubmissionID)
					},
					Completion: WorkflowCompletion{GetExternalID: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (string, error) {
						var id string
						err := json.Unmarshal(data, &id)
						return id, err
					}},
					Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(WorkflowBatchResult[Scores]) error) error {
						for _, item := range batches[info.SubmissionID] {
							require.Equal(t, item.Item.Input*2, item.Item.Output)
							require.Equal(t, true, item.Item.Metadata["collected"])
							if err := yield(WorkflowBatchResult[Scores]{CustomID: item.CustomID, Result: S(0.5)}); err != nil {
								return err
							}
						}
						return nil
					},
				}}, Scorers: []Scorer[int, int]{NewScorer("local", func(context.Context, TaskResult[int, int]) (Scores, error) { return S(1), nil })},
				Classifiers: []Classifier[int, int]{NewClassifier("class", func(context.Context, TaskResult[int, int]) (Classifications, error) {
					return Classifications{{ID: "ok"}}, nil
				})},
			}
			r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: 2}, {Input: 3}})})
			require.NoError(t, err)
			require.Equal(t, 3, taskCalls)
			r, err = w.Poll(ctx, r.RunID)
			require.NoError(t, err)
			if maxWait == 0 {
				require.Equal(t, 0, scoreCalls)
			} else {
				require.Equal(t, 1, scoreCalls)
			}
			for k := range taskReady {
				taskReady[k] = true
			}
			r, err = w.Poll(ctx, r.RunID)
			require.NoError(t, err)
			require.Equal(t, 2, scoreCalls)
			for id := range batches {
				r, err = w.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: id})
				require.NoError(t, err)
			}
			require.Equal(t, WorkflowRunCompleted, r.Status)
			require.Equal(t, 0, r.Failed)
			require.Equal(t, 0.5, r.Scores["batch"])
			require.Equal(t, 1.0, r.Scores["local"])
		})
	}
}

func TestWorkflowLocalTrialsAndFailures(t *testing.T) {
	w := WorkflowEval[int, int]{Name: "local", Store: NewWorkflowEvalMemoryStore(), ParameterSchema: ParameterSchema{"model": {Type: "string", Default: "test"}}, Task: TaskWithHooks(func(ctx context.Context, input int, hooks *TaskHooks) (int, error) {
		require.Equal(t, "test", hooks.Parameters.String("model"))
		if input < 0 {
			return 0, errors.New("bad task")
		}
		return input * 2, nil
	}), Scorers: []Scorer[int, int]{NewScorer("bad", func(context.Context, TaskResult[int, int]) (Scores, error) { return nil, errors.New("bad scorer") }), NewScorer("good", func(context.Context, TaskResult[int, int]) (Scores, error) { return S(1), nil })}}
	r, err := w.Start(context.Background(), WorkflowStartOpts[int, int]{NoSendLogs: true, TrialCount: 2, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: -1, TrialCount: 1}})})
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	require.Equal(t, 3, r.Completed)
	require.Equal(t, 3, r.Failed)
	require.Equal(t, 1.0, r.Scores["good"])
}

func TestWorkflowSharedExternalID(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	var submissions []WorkflowSubmissionContext
	w := WorkflowEval[int, int]{Name: "external", Store: NewWorkflowEvalMemoryStore(), Task: WorkflowTask[int, int]{
		Submit: func(context.Context, WorkflowTaskItem[int, int], WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
		Completion: WorkflowCompletion{GetExternalID: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (string, error) {
			require.Equal(t, "caller", ctx.Value(ctxKey{}))
			submissions = append(submissions, info)
			return "shared", nil
		}},
		Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			return WorkflowTaskResult[int]{Output: 2}, nil
		},
	}}
	a, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
	require.NoError(t, err)
	b, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
	require.NoError(t, err)
	require.Len(t, submissions, 2)
	require.Equal(t, a.RunID, submissions[0].RunID)
	require.Equal(t, b.RunID, submissions[1].RunID)
	require.NotEmpty(t, submissions[0].SubmissionID)
	require.NotEmpty(t, submissions[1].SubmissionID)
	require.NotEqual(t, submissions[0].SubmissionID, submissions[1].SubmissionID)
	_, err = w.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{ExternalID: "shared"})
	require.ErrorContains(t, err, "run ID")
	for _, id := range []string{a.RunID, b.RunID} {
		r, err := w.ProcessSubmissionResult(ctx, WorkflowSubmissionResult{RunID: id, ExternalID: "shared"})
		require.NoError(t, err)
		require.Equal(t, WorkflowRunCompleted, r.Status)
	}
}

func TestWorkflowLocalTracing(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(ctx)) })
	var root oteltrace.SpanContext
	w := NewWorkflowEval(NewEvaluator[int, int](nil, tp, nil, ""), &WorkflowEval[int, int]{Name: "tracing", Store: NewWorkflowEvalMemoryStore(),
		Task: TaskWithHooks(func(ctx context.Context, input int, hooks *TaskHooks) (int, error) {
			require.True(t, hooks.TaskSpan.IsRecording())
			root = hooks.EvalSpan.SpanContext()
			_, child := tp.Tracer("test").Start(ctx, "provider-call")
			child.End()
			return input * 2, nil
		}), Scorers: []Scorer[int, int]{NewScorer("score", func(ctx context.Context, r TaskResult[int, int]) (Scores, error) {
			require.True(t, oteltrace.SpanFromContext(ctx).IsRecording())
			return S(1), nil
		})},
	})
	result, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 2}})})
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, result.Status)
	require.Empty(t, result.Permalink, "NoSendLogs runs have no experiment to link")
	spans := exporter.GetSpans()
	require.Len(t, spans, 3)
	var task, child, scorer sdktrace.ReadOnlySpan
	for _, stub := range spans {
		switch stub.Name {
		case "task":
			task = stub.Snapshot()
		case "provider-call":
			child = stub.Snapshot()
		case "score":
			scorer = stub.Snapshot()
		}
	}
	require.NotNil(t, task)
	require.NotNil(t, child)
	require.NotNil(t, scorer)
	require.Equal(t, root.SpanID(), task.Parent().SpanID())
	require.Equal(t, task.SpanContext().SpanID(), child.Parent().SpanID())
	require.Equal(t, root.SpanID(), scorer.Parent().SpanID())
}

type failingWorkflowStore struct {
	*WorkflowEvalMemoryStore
	fail bool
}

func (s *failingWorkflowStore) Write(ctx context.Context, key string, value []byte) error {
	if s.fail {
		s.fail = false
		return errors.New("storage unavailable")
	}
	return s.WorkflowEvalMemoryStore.Write(ctx, key, value)
}

func TestWorkflowCheckpointWriteFailure(t *testing.T) {
	ctx := context.Background()
	store := &failingWorkflowStore{WorkflowEvalMemoryStore: NewWorkflowEvalMemoryStore()}
	fail := true
	w := WorkflowEval[int, int]{Name: "write-failure", Store: store, Task: WorkflowTask[int, int]{
		Submit: func(context.Context, WorkflowTaskItem[int, int], WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
		Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			store.fail = fail
			return WorkflowTaskResult[int]{Output: 2}, nil
		},
	}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
	require.NoError(t, err)
	r, err = w.Poll(ctx, r.RunID)
	require.ErrorContains(t, err, "storage unavailable")
	require.Zero(t, r.Completed)
	fail = false
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
}

func TestWorkflowSingleScorerAndRetry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	w := WorkflowEval[int, int]{Name: "single-score", Store: NewWorkflowEvalMemoryStore(), Task: T(func(ctx context.Context, input int) (int, error) { return input * 2, nil }), WorkflowScorers: []WorkflowScorerDefinition[int, int]{WorkflowScorer[int, int]{Name: "judge",
		Submit: func(ctx context.Context, item WorkflowScorerItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("submit interrupted")
			}
			require.Equal(t, 4, item.Output)
			return json.Marshal(item.Output)
		},
		Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (Scores, error) {
			return S(1), nil
		},
	}}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 2}})})
	require.ErrorContains(t, err, "submit interrupted")
	require.NotEmpty(t, r.RunID)
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	require.Equal(t, 1.0, r.Scores["judge"])
	require.Equal(t, 2, calls)
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	require.Equal(t, 2, calls)
}

func TestWorkflowScoreOrder(t *testing.T) {
	w := WorkflowEval[int, int]{Name: "score-order", Store: NewWorkflowEvalMemoryStore(), Task: T(func(ctx context.Context, input int) (int, error) { return input, nil }), Scorers: []Scorer[int, int]{
		NewScorer("first", func(context.Context, TaskResult[int, int]) (Scores, error) {
			return Scores{{Name: "quality", Score: 0.25}}, nil
		}),
		NewScorer("second", func(context.Context, TaskResult[int, int]) (Scores, error) {
			return Scores{{Name: "quality", Score: 0.75}}, nil
		}),
	}}
	for range 50 {
		r, err := w.Start(context.Background(), WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}})})
		require.NoError(t, err)
		require.Equal(t, 0.75, r.Scores["quality"], "last declared scorer wins, as in normal evals")
	}
}

func TestWorkflowPartialBatchWaitAfterFullBatch(t *testing.T) {
	ctx := context.Background()
	submitted := 0
	w := WorkflowEval[int, int]{Name: "batch-wait", Store: NewWorkflowEvalMemoryStore(), MaxConcurrency: 1,
		Task: WorkflowTask[int, int]{Submit: func(context.Context, WorkflowTaskItem[int, int], WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}, Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
			return WorkflowPollResult{Status: WorkflowPending}, nil
		}}, Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			return WorkflowTaskResult[int]{}, nil
		}},
		WorkflowScorers: []WorkflowScorerDefinition[int, int]{WorkflowBatchScorer[int, int]{Name: "judge", Batching: WorkflowBatching{MaxSize: 3, MaxWait: time.Hour},
			Submit: func(ctx context.Context, items []WorkflowBatchItem[WorkflowScorerItem[int, int]], info WorkflowSubmissionContext) (json.RawMessage, error) {
				submitted++
				return json.RawMessage(`{}`), nil
			},
			Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
				return WorkflowPollResult{Status: WorkflowPending}, nil
			}},
			Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext, func(WorkflowBatchResult[Scores]) error) error {
				return nil
			},
		}}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: 2}, {Input: 3}, {Input: 4}, {Input: 5}})})
	require.NoError(t, err)
	run, stages, err := w.load(ctx, r.RunID)
	require.NoError(t, err)
	// Prepare durable ready timestamps without a wall-clock sleep: an old item
	// belongs to the full batch, while the remaining partial batch is still young.
	for i := range 4 {
		run.Cases[i].Done["task"] = true
		run.Cases[i].ReadyAt = time.Now()
	}
	run.Cases[0].ReadyAt = time.Now().Add(-2 * time.Hour)
	require.NoError(t, w.advance(ctx, run, stages))
	require.Equal(t, 1, submitted, "oldest item of the consumed batch must not flush the younger remainder")
}

func TestWorkflowFailedPollBlocksCompletion(t *testing.T) {
	ctx := context.Background()
	failedPolls := 0
	w := WorkflowEval[int, int]{Name: "failed-poll", Store: NewWorkflowEvalMemoryStore(), Task: WorkflowTask[int, int]{
		Submit: func(ctx context.Context, item WorkflowTaskItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.Marshal(item.Input)
		},
		Completion: WorkflowCompletion{Poll: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowPollResult, error) {
			if string(data) == "1" {
				failedPolls++
				return WorkflowPollResult{Status: WorkflowFailed, Error: errors.New("batch expired")}, nil
			}
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			var input int
			err := json.Unmarshal(data, &input)
			return WorkflowTaskResult[int]{Output: input * 2}, err
		},
	}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: 2}})})
	require.NoError(t, err)
	// As in Python and JS, a failed submission raises on every poll and the run
	// never completes, while independent submissions still advance.
	for attempt := 1; attempt <= 2; attempt++ {
		r, err = w.Poll(ctx, r.RunID)
		require.ErrorContains(t, err, "batch expired")
		require.Equal(t, WorkflowRunWaiting, r.Status)
		require.Equal(t, 1, r.Completed)
		require.Equal(t, 0, r.Failed)
		require.Equal(t, 1, r.PendingPoll)
		require.Equal(t, attempt, failedPolls)
	}
}

func TestWorkflowScorerItems(t *testing.T) {
	ctx := context.Background()
	var items []WorkflowTaskItem[int, int]
	var scored []WorkflowScorerItem[int, int]
	w := WorkflowEval[int, int]{Name: "case-ids", Store: NewWorkflowEvalMemoryStore(), MaxConcurrency: 1,
		Task: WorkflowBatchTask[int, int]{Batching: WorkflowBatching{MaxSize: 10},
			Submit: func(ctx context.Context, batch []WorkflowBatchItem[WorkflowTaskItem[int, int]], info WorkflowSubmissionContext) (json.RawMessage, error) {
				for _, item := range batch {
					items = append(items, item.Item)
				}
				return json.RawMessage(`{}`), nil
			},
			Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
				return WorkflowPollResult{Status: WorkflowComplete}, nil
			}},
			Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(WorkflowBatchResult[WorkflowTaskResult[int]]) error) error {
				for i := range items {
					if err := yield(WorkflowBatchResult[WorkflowTaskResult[int]]{CustomID: info.SubmissionID + "_" + strconv.Itoa(i), Result: WorkflowTaskResult[int]{Output: 1, Tags: []string{"collected"}}}); err != nil {
						return err
					}
				}
				return nil
			},
		},
		WorkflowScorers: []WorkflowScorerDefinition[int, int]{WorkflowScorer[int, int]{Name: "judge",
			Submit: func(ctx context.Context, item WorkflowScorerItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
				scored = append(scored, item)
				return json.RawMessage(`{}`), nil
			},
			Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
				return WorkflowPollResult{Status: WorkflowComplete}, nil
			}},
			Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (Scores, error) { return S(1), nil },
		}}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, TrialCount: 2, Dataset: NewDataset([]Case[int, int]{{ID: "q1", Input: 1, Tags: []string{"case"}}, {Input: 2, Tags: []string{"case"}}})})
	require.NoError(t, err)
	require.Len(t, items, 4)
	// Trials share their case ID: the dataset ID when present, otherwise one
	// generated per case. Each trial keeps a distinct row ID.
	require.Equal(t, "q1", items[0].CaseID)
	require.Equal(t, "q1", items[1].CaseID)
	require.NotEmpty(t, items[2].CaseID)
	require.NotEqual(t, "q1", items[2].CaseID)
	require.Equal(t, items[2].CaseID, items[3].CaseID)
	rowIDs := map[string]bool{}
	for _, item := range items {
		require.NotEqual(t, item.CaseID, item.ID)
		rowIDs[item.ID] = true
	}
	require.Len(t, rowIDs, 4)
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, 4, r.PendingPoll, "one scorer submission per trial remains")
	require.Len(t, scored, 4)
	for _, item := range scored {
		require.NotEmpty(t, item.CaseID)
		require.True(t, rowIDs[item.ID])
		require.Equal(t, []string{"collected"}, item.Tags, "scorers see tags after the task replaces them")
	}
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
}

func TestWorkflowSubmitIsolatesCaseState(t *testing.T) {
	ctx := context.Background()
	scorer := func(name string, check func(WorkflowScorerItem[int, int])) WorkflowScorer[int, int] {
		return WorkflowScorer[int, int]{Name: name,
			Submit: func(ctx context.Context, item WorkflowScorerItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
				check(item)
				return json.RawMessage(`{}`), nil
			},
			Completion: WorkflowCompletion{Poll: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error) {
				return WorkflowPollResult{Status: WorkflowComplete}, nil
			}},
			Collect: func(context.Context, json.RawMessage, WorkflowSubmissionContext) (Scores, error) { return S(1), nil },
		}
	}
	// Sequential callbacks make the leak deterministic; with concurrency the same
	// sharing is a concurrent map write.
	w := WorkflowEval[int, int]{Name: "isolation", Store: NewWorkflowEvalMemoryStore(), MaxConcurrency: 1,
		Task: T(func(ctx context.Context, input int) (int, error) { return input, nil }),
		WorkflowScorers: []WorkflowScorerDefinition[int, int]{
			scorer("first", func(item WorkflowScorerItem[int, int]) { item.Metadata["leaked"] = true }),
			scorer("second", func(item WorkflowScorerItem[int, int]) {
				require.NotContains(t, item.Metadata, "leaked", "a callback's mutation reached another stage")
			}),
		}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1, Metadata: Metadata{"source": "dataset"}}})})
	require.NoError(t, err)
	r, err = w.Poll(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, WorkflowRunCompleted, r.Status)
	run, _, err := w.load(ctx, r.RunID)
	require.NoError(t, err)
	require.Equal(t, Metadata{"source": "dataset"}, run.Cases[0].Item.Metadata)
}

func TestWorkflowPollErrorDoesNotBlockOtherSubmissions(t *testing.T) {
	ctx := context.Background()
	w := WorkflowEval[int, int]{Name: "poll-error", Store: NewWorkflowEvalMemoryStore(), Task: WorkflowTask[int, int]{
		Submit: func(ctx context.Context, item WorkflowTaskItem[int, int], info WorkflowSubmissionContext) (json.RawMessage, error) {
			return json.Marshal(item.Input)
		},
		Completion: WorkflowCompletion{Poll: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowPollResult, error) {
			if string(data) == "1" {
				return WorkflowPollResult{}, errors.New("network timeout")
			}
			return WorkflowPollResult{Status: WorkflowComplete}, nil
		}},
		Collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext) (WorkflowTaskResult[int], error) {
			return WorkflowTaskResult[int]{Output: 2}, nil
		},
	}}
	r, err := w.Start(ctx, WorkflowStartOpts[int, int]{NoSendLogs: true, Dataset: NewDataset([]Case[int, int]{{Input: 1}, {Input: 2}})})
	require.NoError(t, err)
	r, err = w.Poll(ctx, r.RunID)
	require.ErrorContains(t, err, "network timeout")
	require.Equal(t, 1, r.Completed, "a poll error for one submission must not block the others")
	require.Equal(t, 1, r.PendingPoll)
}
