package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// WorkflowSubmissionContext identifies a provider operation. SubmissionID is
// stable across retries and should be used as a provider idempotency key.
type WorkflowSubmissionContext struct {
	RunID        string
	SubmissionID string
}

// WorkflowPollStatus is the provider's completion state.
type WorkflowPollStatus string

const (
	// WorkflowPending means the provider has not finished.
	WorkflowPending WorkflowPollStatus = "pending"
	// WorkflowComplete means results can be collected.
	WorkflowComplete WorkflowPollStatus = "complete"
	// WorkflowFailed means the entire provider submission failed. Poll returns
	// the error and rechecks the submission on every later call, so a permanent
	// failure blocks completion; start a new run to recover. To finish a run
	// despite a failed provider batch, report WorkflowComplete and let Collect
	// omit the results so those items are recorded as failed.
	WorkflowFailed WorkflowPollStatus = "failed"
)

// WorkflowPollResult reports provider completion or a terminal error.
type WorkflowPollResult struct {
	Status WorkflowPollStatus
	Error  error
}

// WorkflowCompletion selects exactly one completion mode. Poll checks the
// provider; GetExternalID extracts the ID delivered by a verified webhook.
type WorkflowCompletion struct {
	Poll          func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowPollResult, error)
	GetExternalID func(context.Context, json.RawMessage, WorkflowSubmissionContext) (string, error)
}

// WorkflowBatching controls provider batch sizes. MaxWait applies only to
// scorers: it flushes partial batches during advancement, and zero waits until
// full or no more items can arrive. Task batches are submitted immediately, so
// a batch task rejects a nonzero MaxWait.
type WorkflowBatching struct {
	MaxSize int
	MaxWait time.Duration
}

// WorkflowTaskItem is a persisted case/trial passed to a workflow task. ID is
// unique per trial. CaseID is shared by a case's trials: the dataset Case.ID
// when set, otherwise generated when the run starts.
type WorkflowTaskItem[I, R any] struct {
	ID         string
	CaseID     string
	Input      I
	Expected   R
	Metadata   Metadata
	Tags       []string
	Parameters Parameters
	TrialIndex int
}

// WorkflowTaskResult is a task output with optional metadata and replacement tags.
type WorkflowTaskResult[R any] struct {
	Output   R
	Metadata Metadata
	Tags     []string
}

// WorkflowScorerItem is a completed task passed to a deferred scorer. Tags
// reflect any replacement returned by the task.
type WorkflowScorerItem[I, R any] struct {
	TaskResult[I, R]
	ID         string
	CaseID     string
	Tags       []string
	TrialIndex int
}

// WorkflowBatchItem attaches a provider-safe identifier to a case. Submit must
// use CustomID as the provider's custom_id, and Collect must return it unchanged.
type WorkflowBatchItem[T any] struct {
	CustomID string
	Item     T
}

// WorkflowBatchResult contains either a result or a per-item error.
type WorkflowBatchResult[T any] struct {
	CustomID string
	Result   T
	Error    error
}

// WorkflowTaskDefinition accepts a TaskFunc, WorkflowTask, or WorkflowBatchTask.
type WorkflowTaskDefinition[I, R any] interface{ workflowTask() workflowStage[I, R] }

// WorkflowScorerDefinition accepts a WorkflowScorer or WorkflowBatchScorer.
// Ordinary scorers belong in WorkflowEval.Scorers.
type WorkflowScorerDefinition[I, R any] interface{ workflowScorer() workflowStage[I, R] }

// WorkflowTask submits one asynchronous provider operation per case/trial.
// Submission handles must contain valid JSON and survive a process restart.
type WorkflowTask[I, R any] struct {
	Submit     func(context.Context, WorkflowTaskItem[I, R], WorkflowSubmissionContext) (json.RawMessage, error)
	Completion WorkflowCompletion
	Collect    func(context.Context, json.RawMessage, WorkflowSubmissionContext) (WorkflowTaskResult[R], error)
}

// WorkflowBatchTask submits multiple cases in each provider operation. Collect
// calls yield for each result and must stop on a yield error. A failed Collect
// can be retried; already checkpointed items are ignored on replay.
type WorkflowBatchTask[I, R any] struct {
	Batching   WorkflowBatching
	Submit     func(context.Context, []WorkflowBatchItem[WorkflowTaskItem[I, R]], WorkflowSubmissionContext) (json.RawMessage, error)
	Completion WorkflowCompletion
	Collect    func(context.Context, json.RawMessage, WorkflowSubmissionContext, func(WorkflowBatchResult[WorkflowTaskResult[R]]) error) error
}

// WorkflowScorer submits one asynchronous scoring operation per task output.
type WorkflowScorer[I, R any] struct {
	Name       string
	Submit     func(context.Context, WorkflowScorerItem[I, R], WorkflowSubmissionContext) (json.RawMessage, error)
	Completion WorkflowCompletion
	Collect    func(context.Context, json.RawMessage, WorkflowSubmissionContext) (Scores, error)
}

// WorkflowBatchScorer batches task outputs for asynchronous scoring.
type WorkflowBatchScorer[I, R any] struct {
	Name       string
	Batching   WorkflowBatching
	Submit     func(context.Context, []WorkflowBatchItem[WorkflowScorerItem[I, R]], WorkflowSubmissionContext) (json.RawMessage, error)
	Completion WorkflowCompletion
	Collect    func(context.Context, json.RawMessage, WorkflowSubmissionContext, func(WorkflowBatchResult[Scores]) error) error
}

type workflowValue[R any] struct {
	Task   WorkflowTaskResult[R]
	Scores Scores
}
type workflowStage[I, R any] struct {
	name       string
	batching   WorkflowBatching
	batch      bool
	completion WorkflowCompletion
	submit     func(context.Context, []WorkflowBatchItem[workflowCase[I, R]], WorkflowSubmissionContext) (json.RawMessage, error)
	collect    func(context.Context, json.RawMessage, WorkflowSubmissionContext, func(string, workflowValue[R], error) error) error
	local      TaskFunc[I, R]
	valid      bool
}

//nolint:unused // Invoked through the generic workflow definition interface.
func (t WorkflowTask[I, R]) workflowTask() workflowStage[I, R] {
	return workflowStage[I, R]{name: "task", batching: WorkflowBatching{MaxSize: 1}, completion: t.Completion, valid: t.Submit != nil && t.Collect != nil,
		submit: func(ctx context.Context, items []WorkflowBatchItem[workflowCase[I, R]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			return t.Submit(ctx, items[0].Item.Item, info)
		},
		collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(string, workflowValue[R], error) error) error {
			result, err := t.Collect(ctx, data, info)
			if err != nil {
				return err
			}
			return yield(info.SubmissionID+"_0", workflowValue[R]{Task: result}, nil)
		}}
}

//nolint:unused // Invoked through the generic workflow definition interface.
func (t WorkflowBatchTask[I, R]) workflowTask() workflowStage[I, R] {
	return workflowStage[I, R]{name: "task", batching: t.Batching, batch: true, completion: t.Completion, valid: t.Submit != nil && t.Collect != nil,
		submit: func(ctx context.Context, items []WorkflowBatchItem[workflowCase[I, R]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			batch := make([]WorkflowBatchItem[WorkflowTaskItem[I, R]], len(items))
			for i, item := range items {
				batch[i] = WorkflowBatchItem[WorkflowTaskItem[I, R]]{CustomID: item.CustomID, Item: item.Item.Item}
			}
			return t.Submit(ctx, batch, info)
		}, collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(string, workflowValue[R], error) error) error {
			return t.Collect(ctx, data, info, func(r WorkflowBatchResult[WorkflowTaskResult[R]]) error {
				return yield(r.CustomID, workflowValue[R]{Task: r.Result}, r.Error)
			})
		}}
}

//nolint:unused // Invoked through the generic workflow definition interface.
func (s WorkflowScorer[I, R]) workflowScorer() workflowStage[I, R] {
	return workflowStage[I, R]{name: s.Name, batching: WorkflowBatching{MaxSize: 1}, completion: s.Completion, valid: s.Submit != nil && s.Collect != nil,
		submit: func(ctx context.Context, items []WorkflowBatchItem[workflowCase[I, R]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			return s.Submit(ctx, items[0].Item.scorerItem(), info)
		},
		collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(string, workflowValue[R], error) error) error {
			scores, err := s.Collect(ctx, data, info)
			if err != nil {
				return err
			}
			return yield(info.SubmissionID+"_0", workflowValue[R]{Scores: scores}, nil)
		}}
}

//nolint:unused // Invoked through the generic workflow definition interface.
func (s WorkflowBatchScorer[I, R]) workflowScorer() workflowStage[I, R] {
	return workflowStage[I, R]{name: s.Name, batching: s.Batching, batch: true, completion: s.Completion, valid: s.Submit != nil && s.Collect != nil,
		submit: func(ctx context.Context, items []WorkflowBatchItem[workflowCase[I, R]], info WorkflowSubmissionContext) (json.RawMessage, error) {
			batch := make([]WorkflowBatchItem[WorkflowScorerItem[I, R]], len(items))
			for i, item := range items {
				batch[i] = WorkflowBatchItem[WorkflowScorerItem[I, R]]{CustomID: item.CustomID, Item: item.Item.scorerItem()}
			}
			return s.Submit(ctx, batch, info)
		}, collect: func(ctx context.Context, data json.RawMessage, info WorkflowSubmissionContext, yield func(string, workflowValue[R], error) error) error {
			return s.Collect(ctx, data, info, func(r WorkflowBatchResult[Scores]) error {
				return yield(r.CustomID, workflowValue[R]{Scores: r.Result}, r.Error)
			})
		}}
}

//nolint:unused // Invoked through the generic workflow definition interface.
func (t TaskFunc[I, R]) workflowTask() workflowStage[I, R] {
	return workflowStage[I, R]{name: "task", local: t, valid: t != nil}
}

func (s workflowStage[I, R]) validate() error {
	if !s.valid {
		return fmt.Errorf("workflow %q: submit and collect callbacks are required", s.name)
	}
	if s.local != nil {
		return nil
	}
	if s.batching.MaxSize < 1 || s.batching.MaxWait < 0 {
		return fmt.Errorf("workflow %q: batch size must be positive and wait nonnegative", s.name)
	}
	if s.name == "task" && s.batching.MaxWait != 0 {
		return errors.New(`workflow "task": MaxWait applies only to batch scorers`)
	}
	if (s.completion.Poll == nil) == (s.completion.GetExternalID == nil) {
		return fmt.Errorf("workflow %q: choose exactly one completion mode", s.name)
	}
	return nil
}
