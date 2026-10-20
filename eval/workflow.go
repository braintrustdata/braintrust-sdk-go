package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	oteltrace "go.opentelemetry.io/otel/trace"

	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

// WorkflowEval defines a durable evaluation. Keep the definition (including
// scorer names/order) unchanged while runs are active. Serialize calls advancing
// the same run, including across processes. Provider callbacks may run concurrently.
type WorkflowEval[I, R any] struct {
	Name            string
	Store           WorkflowEvalStore
	Task            WorkflowTaskDefinition[I, R]
	Scorers         []Scorer[I, R]
	WorkflowScorers []WorkflowScorerDefinition[I, R]
	Classifiers     []Classifier[I, R]
	ParameterSchema ParameterSchema
	ProjectName     string
	MaxConcurrency  int
	evaluator       *Evaluator[I, R]
}

// NewWorkflowEval attaches the evaluator's API and tracing infrastructure.
// Applications normally use braintrust.NewWorkflowEval.
func NewWorkflowEval[I, R any](evaluator *Evaluator[I, R], definition *WorkflowEval[I, R]) *WorkflowEval[I, R] {
	definition.evaluator = evaluator
	return definition
}

// WorkflowStartOpts configures a new run. NoSendLogs supports local execution
// without a Braintrust client; it does not disable workflow persistence.
type WorkflowStartOpts[I, R any] struct {
	Dataset    Dataset[I, R]
	Experiment string
	ProjectID  string
	Tags       []string
	Metadata   Metadata
	TrialCount int
	Parameters Parameters
	NoSendLogs bool
}

// WorkflowSubmissionResult identifies a completed submission. ExternalID alone
// is sufficient. SubmissionID requires RunID. The application verifies webhooks.
type WorkflowSubmissionResult struct {
	RunID        string
	SubmissionID string
	ExternalID   string
}

// WorkflowRunStatus is the overall state of a workflow run.
type WorkflowRunStatus string

const (
	// WorkflowRunWaiting means cases or provider submissions are outstanding.
	WorkflowRunWaiting WorkflowRunStatus = "waiting"
	// WorkflowRunCompleted means every case has finished, including failures.
	WorkflowRunCompleted WorkflowRunStatus = "completed"
)

// WorkflowResult reports progress. Completed counts terminal cases, including
// failures. Failed counts cases with any failed task, scorer, or classifier.
// Scores averages successful values by score name. Permalink links to the
// experiment and is empty for NoSendLogs runs.
type WorkflowResult struct {
	RunID          string
	ExperimentID   string
	Permalink      string
	Status         WorkflowRunStatus
	Completed      int
	Failed         int
	PendingPoll    int
	PendingWebhook int
	Scores         map[string]float64
}

type workflowRun[I, R any] struct {
	Version      int
	ID           string
	Definition   string
	ExperimentID string
	NoSendLogs   bool
	CaseIDs      []string
	Submissions  []*workflowSubmission
	Cases        []workflowCase[I, R] `json:"-"`
}

type workflowSubmission struct {
	ID         string
	Stage      int
	Cases      []int
	Data       json.RawMessage `json:",omitempty"`
	ExternalID string
	Done       bool
}

type workflowCase[I, R any] struct {
	Item             WorkflowTaskItem[I, R]
	DatasetID        string
	DatasetRecordID  string
	Output           R
	TaskError        string
	Done             map[string]bool
	Scores           map[string]Scores
	Errors           map[string]string
	Classifications  map[string][]map[string]any
	ClassifierErrors map[string]string
	ReadyAt          time.Time
	StartedAt        time.Time
	FinishedAt       time.Time
	BatchID          string
	// LogPending persists until the row is uploaded, so a failed upload is
	// retried by a later call, even from another process.
	LogPending bool
}

func (c workflowCase[I, R]) scorerItem() WorkflowScorerItem[I, R] {
	return WorkflowScorerItem[I, R]{ID: c.Item.ID, CaseID: c.Item.CaseID, Tags: slices.Clone(c.Item.Tags), TrialIndex: c.Item.TrialIndex, TaskResult: TaskResult[I, R]{Input: c.Item.Input, Expected: c.Item.Expected, Output: c.Output, Metadata: c.Item.Metadata}}
}

func workflowKey(parts ...string) string { return "workflow/v1/" + stringsJoin(parts) }
func stringsJoin(parts []string) string {
	// Hash each component to avoid collisions between user names and delimiters.
	var key string
	for _, p := range parts {
		h := sha256.Sum256([]byte(p))
		key += hex.EncodeToString(h[:]) + "/"
	}
	return key
}

func (w *WorkflowEval[I, R]) stages() ([]workflowStage[I, R], error) {
	if w.Name == "" || w.Store == nil || w.Task == nil {
		return nil, errors.New("workflow name, store, and task are required")
	}
	if w.MaxConcurrency < 0 {
		return nil, errors.New("workflow concurrency must be nonnegative")
	}
	stages := []workflowStage[I, R]{w.Task.workflowTask()}
	names := map[string]bool{"task": true}
	for _, s := range w.WorkflowScorers {
		if s == nil {
			return nil, errors.New("nil workflow scorer")
		}
		stage := s.workflowScorer()
		if stage.name == "" || names[stage.name] {
			return nil, fmt.Errorf("workflow scorer name %q is empty or duplicated", stage.name)
		}
		names[stage.name] = true
		stages = append(stages, stage)
	}
	for _, s := range w.Scorers {
		if s == nil || s.Name() == "" || names[s.Name()] {
			return nil, errors.New("local scorer names must be nonempty and unique")
		}
		names[s.Name()] = true
	}
	for _, c := range w.Classifiers {
		if c == nil || c.Name() == "" || names[c.Name()] {
			return nil, errors.New("classifier names must be nonempty and unique")
		}
		names[c.Name()] = true
	}
	for _, stage := range stages {
		if err := stage.validate(); err != nil {
			return nil, err
		}
	}
	return stages, nil
}

func (w *WorkflowEval[I, R]) definition(stages []workflowStage[I, R]) string {
	names := []string{w.Name}
	for _, s := range stages {
		names = append(names, fmt.Sprintf("%s:%t:%t:%t:%d:%d", s.name, s.local != nil, s.batch, s.completion.Poll != nil, s.batching.MaxSize, s.batching.MaxWait))
	}
	for _, s := range w.Scorers {
		names = append(names, "score:"+s.Name())
	}
	for _, s := range w.Classifiers {
		names = append(names, "classify:"+s.Name())
	}
	return stringsJoin(names)
}

// Start persists the dataset and submits initial task work. Provider polling is
// explicit: call Poll later, or deliver completion through ProcessSubmissionResult.
func (w *WorkflowEval[I, R]) Start(ctx context.Context, opts WorkflowStartOpts[I, R]) (*WorkflowResult, error) {
	stages, err := w.stages()
	if err != nil {
		return nil, err
	}
	if opts.Dataset == nil {
		return nil, errors.New("workflow dataset is required")
	}
	if !opts.NoSendLogs && (w.evaluator == nil || w.evaluator.api == nil) {
		return nil, errors.New("workflow requires a Braintrust evaluator or NoSendLogs")
	}
	params, err := w.ParameterSchema.Resolve(opts.Parameters)
	if err != nil {
		return nil, err
	}
	run := &workflowRun[I, R]{Version: 1, ID: uuid.NewString(), Definition: w.definition(stages), NoSendLogs: opts.NoSendLogs}
	// Register before writing cases so a failed registration leaves no state.
	if !run.NoSendLogs {
		name := opts.Experiment
		if name == "" {
			name = w.Name
		}
		project := w.ProjectName
		if project == "" {
			project = w.evaluator.defaultProjectName
		}
		experiment, err := registerExperiment(ctx, w.evaluator.api, name, project, opts.ProjectID, opts.Tags, opts.Metadata, false, opts.Dataset)
		if err != nil {
			return nil, err
		}
		run.ExperimentID = experiment.ID
	}
	trials := max(1, opts.TrialCount)
	runUUID := uuid.MustParse(run.ID)
	for caseIndex := 0; ; caseIndex++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := opts.Dataset.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		count := trials
		if c.TrialCount > 0 {
			count = c.TrialCount
		}
		caseID := c.ID
		if caseID == "" {
			// Derive rather than draw a UUID: unique per run and case, and it
			// leaves the random sequence used for trial and submission IDs intact.
			caseID = uuid.NewSHA1(runUUID, []byte(strconv.Itoa(caseIndex))).String()
		}
		for trial := 0; trial < count; trial++ {
			id := uuid.NewString()
			record := workflowCase[I, R]{Item: WorkflowTaskItem[I, R]{ID: id, CaseID: caseID, Input: c.Input, Expected: c.Expected, Metadata: maps.Clone(c.Metadata), Tags: slices.Clone(c.Tags), Parameters: params, TrialIndex: trial}, DatasetID: opts.Dataset.ID(), DatasetRecordID: c.ID, Done: map[string]bool{}, Scores: map[string]Scores{}, Errors: map[string]string{}, Classifications: map[string][]map[string]any{}, ClassifierErrors: map[string]string{}, StartedAt: time.Now().UTC(), LogPending: true}
			run.CaseIDs = append(run.CaseIDs, id)
			run.Cases = append(run.Cases, record)
		}
	}
	if err := w.parallel(ctx, len(run.Cases), func(i int) error { return w.saveCase(ctx, run.ID, &run.Cases[i]) }); err != nil {
		return nil, err
	}
	if err := w.saveRun(ctx, run); err != nil {
		return nil, err
	}
	err = w.advance(ctx, run, stages)
	return w.finish(ctx, run, err)
}

// Poll checks pending poll-mode submissions once and advances ready stages.
// It also retries incomplete submissions and collection checkpoints. Submissions
// reporting WorkflowFailed stay pending and are returned as errors.
func (w *WorkflowEval[I, R]) Poll(ctx context.Context, runID string) (*WorkflowResult, error) {
	run, stages, err := w.load(ctx, runID)
	if err != nil {
		return nil, err
	}
	// A failing Submit must not block polling work that already has a handle.
	// The final advance retries it again and reports any error that remains.
	submitErr := w.submitPending(ctx, run, stages)
	results := make([]WorkflowPollResult, len(run.Submissions))
	pollErrs := make([]error, len(run.Submissions))
	err = w.parallel(ctx, len(run.Submissions), func(i int) error {
		sub := run.Submissions[i]
		stage := stages[sub.Stage]
		if sub.Done || len(sub.Data) == 0 || stage.completion.Poll == nil {
			return nil
		}
		results[i], pollErrs[i] = stage.completion.Poll(ctx, sub.Data, WorkflowSubmissionContext{RunID: run.ID, SubmissionID: sub.ID})
		return nil
	})
	if err != nil {
		return w.finish(ctx, run, errors.Join(submitErr, err))
	}
	// A failed submission stays outstanding, so every later poll reports it
	// again and the run cannot complete, matching the Python and JS SDKs.
	// Independent submissions still advance before the failure is returned,
	// including when a Poll callback itself errors.
	var failures []error
	for i, sub := range run.Submissions {
		if sub.Done || len(sub.Data) == 0 || stages[sub.Stage].completion.Poll == nil {
			continue
		}
		if pollErrs[i] != nil {
			failures = append(failures, fmt.Errorf("workflow submission %s: %w", sub.ID, pollErrs[i]))
			continue
		}
		switch results[i].Status {
		case WorkflowPending:
		case WorkflowComplete:
			err = w.collect(ctx, run, sub, stages[sub.Stage])
		case WorkflowFailed:
			failure := results[i].Error
			if failure == nil {
				failure = errors.New("provider submission failed")
			}
			failures = append(failures, fmt.Errorf("workflow submission %s: %w", sub.ID, failure))
		default:
			err = fmt.Errorf("invalid provider poll status %q", results[i].Status)
		}
		if err != nil {
			return w.finish(ctx, run, errors.Join(append(failures, submitErr, err)...))
		}
	}
	err = w.advance(ctx, run, stages)
	return w.finish(ctx, run, errors.Join(append(failures, err)...))
}

// ProcessSubmissionResult collects a completed provider operation and advances
// its run. Repeated completion notifications are harmless after checkpointing.
func (w *WorkflowEval[I, R]) ProcessSubmissionResult(ctx context.Context, ref WorkflowSubmissionResult) (*WorkflowResult, error) {
	if _, err := w.stages(); err != nil {
		return nil, err
	}
	if ref.ExternalID != "" {
		key := workflowKey(w.Name, "external", ref.ExternalID)
		if ref.RunID != "" {
			key = workflowKey(w.Name, "external-run", ref.RunID, ref.ExternalID)
		}
		value, err := w.Store.Read(ctx, key)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, errors.New("unknown workflow external ID")
		}
		var stored WorkflowSubmissionResult
		if err = json.Unmarshal(value, &stored); err != nil {
			return nil, err
		}
		if stored.RunID == "" {
			return nil, errors.New("external ID is shared by several runs; supply a run ID")
		}
		if (ref.RunID != "" && ref.RunID != stored.RunID) || (ref.SubmissionID != "" && ref.SubmissionID != stored.SubmissionID) {
			return nil, errors.New("conflicting workflow submission identifiers")
		}
		ref = stored
	}
	if ref.RunID == "" || ref.SubmissionID == "" {
		return nil, errors.New("run ID and submission ID, or external ID, are required")
	}
	run, stages, err := w.load(ctx, ref.RunID)
	if err != nil {
		return nil, err
	}
	for _, sub := range run.Submissions {
		if sub.ID == ref.SubmissionID {
			if len(sub.Data) == 0 {
				return w.finish(ctx, run, errors.New("provider submission has not been persisted"))
			}
			if err = w.collect(ctx, run, sub, stages[sub.Stage]); err == nil {
				err = w.advance(ctx, run, stages)
			}
			return w.finish(ctx, run, err)
		}
	}
	return nil, errors.New("unknown workflow submission")
}

// Status reads persisted progress without submitting, polling, or collecting.
func (w *WorkflowEval[I, R]) Status(ctx context.Context, runID string) (*WorkflowResult, error) {
	run, _, err := w.load(ctx, runID)
	if err != nil {
		return nil, err
	}
	return w.result(run), nil
}

func (w *WorkflowEval[I, R]) saveRun(ctx context.Context, run *workflowRun[I, R]) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return w.Store.Write(ctx, workflowKey(w.Name, "run", run.ID), data)
}
func (w *WorkflowEval[I, R]) saveCase(ctx context.Context, runID string, c *workflowCase[I, R]) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("workflow case must be JSON serializable: %w", err)
	}
	return w.Store.Write(ctx, workflowKey(w.Name, "case", runID, c.Item.ID), data)
}
func (w *WorkflowEval[I, R]) load(ctx context.Context, id string) (*workflowRun[I, R], []workflowStage[I, R], error) {
	stages, err := w.stages()
	if err != nil {
		return nil, nil, err
	}
	data, err := w.Store.Read(ctx, workflowKey(w.Name, "run", id))
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		return nil, nil, errors.New("workflow run not found or expired")
	}
	run := &workflowRun[I, R]{}
	if err = json.Unmarshal(data, run); err != nil {
		return nil, nil, err
	}
	if run.Version != 1 || run.Definition != w.definition(stages) {
		return nil, nil, errors.New("workflow definition or state version changed")
	}
	// Report the first failure: expiry typically affects every case at once.
	run.Cases = make([]workflowCase[I, R], len(run.CaseIDs))
	errs := make([]error, len(run.CaseIDs))
	err = w.parallel(ctx, len(run.CaseIDs), func(i int) error {
		data, err := w.Store.Read(ctx, workflowKey(w.Name, "case", run.ID, run.CaseIDs[i]))
		if err == nil && data == nil {
			err = errors.New("workflow case state expired")
		}
		if err == nil {
			err = json.Unmarshal(data, &run.Cases[i])
		}
		errs[i] = err
		return nil
	})
	for _, e := range append([]error{err}, errs...) {
		if e != nil {
			return nil, nil, e
		}
	}
	return run, stages, nil
}

func (w *WorkflowEval[I, R]) parallel(ctx context.Context, n int, fn func(int) error) error {
	workers := w.MaxConcurrency
	if workers == 0 {
		workers = 10
	}
	workers = min(workers, n)
	errs := make([]error, n)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					errs[i] = err
				} else {
					errs[i] = fn(i)
				}
			}
		})
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return errors.Join(errs...)
}

func (w *WorkflowEval[I, R]) submitPending(ctx context.Context, run *workflowRun[I, R], stages []workflowStage[I, R]) error {
	// Save every successful handle even when another callback fails. All callbacks
	// complete before committing, so no goroutine mutates persisted state.
	data := make([]json.RawMessage, len(run.Submissions))
	callbackErrs := make([]error, len(run.Submissions))
	err := w.parallel(ctx, len(run.Submissions), func(i int) error {
		sub := run.Submissions[i]
		if sub.Done || len(sub.Data) > 0 {
			return nil
		}
		items := make([]WorkflowBatchItem[workflowCase[I, R]], len(sub.Cases))
		for j, index := range sub.Cases {
			items[j] = WorkflowBatchItem[workflowCase[I, R]]{CustomID: sub.ID + "_" + strconv.Itoa(j), Item: run.Cases[index]}
		}
		// Callbacks own their arguments, including maps. Isolate concurrent stages.
		// Decode into a fresh slice: decoding reuses the shared maps in items.
		encoded, e := json.Marshal(items)
		if e != nil {
			return e
		}
		items = nil
		if e = json.Unmarshal(encoded, &items); e != nil {
			return e
		}
		handle, e := stages[sub.Stage].submit(ctx, items, WorkflowSubmissionContext{RunID: run.ID, SubmissionID: sub.ID})
		if e == nil && !json.Valid(handle) {
			e = errors.New("workflow submission handle must be valid JSON")
		}
		data[i] = handle
		callbackErrs[i] = e
		return e
	})
	for i, sub := range run.Submissions {
		if callbackErrs[i] == nil && len(data[i]) > 0 {
			sub.Data = data[i]
		}
	}
	if saveErr := w.saveRun(ctx, run); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	// Register each webhook submission independently so one failure neither
	// hides another error nor leaves later submissions unregistered.
	errs := []error{err}
	for _, sub := range run.Submissions {
		stage := stages[sub.Stage]
		if sub.Done || len(sub.Data) == 0 || sub.ExternalID != "" || stage.completion.GetExternalID == nil {
			continue
		}
		if e := w.registerExternalID(ctx, run, sub, stage); e != nil {
			errs = append(errs, fmt.Errorf("workflow submission %s: %w", sub.ID, e))
		}
	}
	return errors.Join(append(errs, w.saveRun(ctx, run))...)
}

func (w *WorkflowEval[I, R]) registerExternalID(ctx context.Context, run *workflowRun[I, R], sub *workflowSubmission, stage workflowStage[I, R]) error {
	id, err := stage.completion.GetExternalID(ctx, sub.Data, WorkflowSubmissionContext{RunID: run.ID, SubmissionID: sub.ID})
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("empty provider external ID")
	}
	ref, _ := json.Marshal(WorkflowSubmissionResult{RunID: run.ID, SubmissionID: sub.ID})
	stored, _, err := w.Store.GetOrSet(ctx, workflowKey(w.Name, "external-run", run.ID, id), ref)
	if err != nil {
		return err
	}
	if string(stored) != string(ref) {
		return errors.New("external ID identifies multiple submissions in the same run")
	}
	stored, _, err = w.Store.GetOrSet(ctx, workflowKey(w.Name, "external", id), ref)
	if err != nil {
		return err
	}
	if string(stored) != string(ref) {
		if err = w.Store.Write(ctx, workflowKey(w.Name, "external", id), []byte(`{}`)); err != nil {
			return err
		}
	}
	sub.ExternalID = id
	return nil
}

func (w *WorkflowEval[I, R]) advance(ctx context.Context, run *workflowRun[I, R], stages []workflowStage[I, R]) error {
	if stages[0].local != nil {
		err := w.parallel(ctx, len(run.Cases), func(i int) error {
			c := &run.Cases[i]
			if c.Done["task"] {
				return nil
			}
			root := oteltrace.SpanFromContext(w.caseContext(ctx, run, c))
			taskCtx, span := w.callbackSpan(ctx, run, c, "task", "task")
			defer span.End()
			output, e := stages[0].local(taskCtx, c.Item.Input, &TaskHooks{TaskSpan: span, EvalSpan: root, Expected: c.Item.Expected, Metadata: c.Item.Metadata, Tags: c.Item.Tags, TrialIndex: c.Item.TrialIndex, Parameters: c.Item.Parameters})
			e = errors.Join(e, setJSONAttr(span, "braintrust.input_json", c.Item.Input), setJSONAttr(span, "braintrust.output_json", output.Value))
			if e != nil {
				recordSpanError(span, e)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if output.UserData != nil {
				e = errors.New("workflow tasks cannot persist TaskOutput.UserData; use JSON metadata")
			}
			return w.apply(ctx, run, c, stages[0], workflowValue[R]{Task: WorkflowTaskResult[R]{Output: output.Value}}, e, "")
		})
		if err != nil {
			return err
		}
	}
	// Run ordinary scoring/classification once per case, checkpointing each stage.
	if err := w.parallel(ctx, len(run.Cases), func(i int) error { return w.localScores(ctx, run, &run.Cases[i]) }); err != nil {
		return err
	}
	for stageIndex, stage := range stages {
		if stage.local != nil {
			continue
		}
		assigned := map[int]bool{}
		for _, sub := range run.Submissions {
			if sub.Stage == stageIndex {
				for _, index := range sub.Cases {
					assigned[index] = true
				}
			}
		}
		ready := []int{}
		allTasksDone := true
		for i, c := range run.Cases {
			if !c.Done["task"] {
				allTasksDone = false
			}
			if assigned[i] || c.Done[stage.name] {
				continue
			}
			if stageIndex > 0 && (!c.Done["task"] || c.TaskError != "") {
				continue
			}
			ready = append(ready, i)
		}
		for len(ready) > 0 {
			size := min(stage.batching.MaxSize, len(ready))
			if stageIndex > 0 && size < stage.batching.MaxSize && !allTasksDone {
				oldest := run.Cases[ready[0]].ReadyAt
				for _, index := range ready[1:] {
					if run.Cases[index].ReadyAt.Before(oldest) {
						oldest = run.Cases[index].ReadyAt
					}
				}
				if stage.batching.MaxWait == 0 || time.Since(oldest) < stage.batching.MaxWait {
					break
				}
			}
			run.Submissions = append(run.Submissions, &workflowSubmission{ID: uuid.NewString(), Stage: stageIndex, Cases: slices.Clone(ready[:size])})
			ready = ready[size:]
		}
	}
	if err := w.saveRun(ctx, run); err != nil {
		return err
	}
	return w.submitPending(ctx, run, stages)
}

func (w *WorkflowEval[I, R]) collect(ctx context.Context, run *workflowRun[I, R], sub *workflowSubmission, stage workflowStage[I, R]) error {
	if sub.Done {
		return nil
	}
	indexes := make(map[string]int, len(sub.Cases))
	for i, index := range sub.Cases {
		indexes[sub.ID+"_"+strconv.Itoa(i)] = index
	}
	seen := make(map[string]bool, len(indexes))
	err := stage.collect(ctx, sub.Data, WorkflowSubmissionContext{RunID: run.ID, SubmissionID: sub.ID}, func(id string, value workflowValue[R], itemErr error) error {
		index, ok := indexes[id]
		if !ok {
			return fmt.Errorf("unknown workflow custom ID %q", id)
		}
		if seen[id] {
			return fmt.Errorf("duplicate workflow custom ID %q", id)
		}
		seen[id] = true
		c := &run.Cases[index]
		if c.Done[stage.name] {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return w.apply(ctx, run, c, stage, value, itemErr, sub.ID)
	})
	if err != nil {
		return err
	}
	for _, index := range sub.Cases {
		c := &run.Cases[index]
		if c.Done[stage.name] {
			continue
		}
		if err := w.apply(ctx, run, c, stage, workflowValue[R]{}, errors.New("missing result in provider batch"), sub.ID); err != nil {
			return err
		}
	}
	sub.Done = true
	if err := w.saveRun(ctx, run); err != nil {
		sub.Done = false
		return err
	}
	return nil
}

func (w *WorkflowEval[I, R]) apply(ctx context.Context, run *workflowRun[I, R], c *workflowCase[I, R], stage workflowStage[I, R], value workflowValue[R], itemErr error, submissionID string) error {
	// Publish progress only after its checkpoint succeeds. Clone maps before
	// modification so an unavailable store cannot expose uncommitted results.
	original := c
	updated := *c
	updated.Item.Metadata = maps.Clone(c.Item.Metadata)
	updated.Done = maps.Clone(c.Done)
	updated.Errors = maps.Clone(c.Errors)
	updated.Scores = maps.Clone(c.Scores)
	c = &updated
	if stage.name == "task" {
		if itemErr != nil {
			c.TaskError = itemErr.Error()
		} else {
			c.Output = value.Task.Output
			if c.Item.Metadata == nil {
				c.Item.Metadata = Metadata{}
			}
			maps.Copy(c.Item.Metadata, value.Task.Metadata)
			if value.Task.Tags != nil {
				c.Item.Tags = value.Task.Tags
			}
		}
		c.ReadyAt = time.Now().UTC()
		if stage.batch {
			c.BatchID = submissionID
		}
	} else {
		if itemErr == nil {
			itemErr = normalizeWorkflowScores(value.Scores, stage.name)
		}
		if itemErr != nil {
			c.Errors[stage.name] = itemErr.Error()
		} else {
			c.Scores[stage.name] = value.Scores
		}
	}
	c.Done[stage.name] = true
	if w.caseDone(c) {
		c.FinishedAt = time.Now().UTC()
	}
	c.LogPending = true
	if err := w.saveCase(ctx, run.ID, c); err != nil {
		return err
	}
	*original = updated
	return nil
}

func normalizeWorkflowScores(scores Scores, name string) error {
	for i := range scores {
		if scores[i].Name == "" {
			scores[i].Name = name
		}
		if math.IsNaN(scores[i].Score) || math.IsInf(scores[i].Score, 0) {
			return errors.New("workflow score must be finite")
		}
	}
	return nil
}

func (w *WorkflowEval[I, R]) localScores(ctx context.Context, run *workflowRun[I, R], c *workflowCase[I, R]) error {
	if !c.Done["task"] || c.TaskError != "" {
		return nil
	}
	for _, s := range w.Scorers {
		if c.Done[s.Name()] {
			continue
		}
		scoreCtx, span := w.callbackSpan(ctx, run, c, s.Name(), "score")
		scores, err := s.Run(scoreCtx, c.scorerItem().TaskResult)
		err = errors.Join(err, setJSONAttr(span, "braintrust.input_json", c.scorerItem().TaskResult), setJSONAttr(span, "braintrust.output_json", scores))
		if err != nil {
			recordSpanError(span, err)
		}
		span.End()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = w.apply(ctx, run, c, workflowStage[I, R]{name: s.Name()}, workflowValue[R]{Scores: scores}, err, ""); err != nil {
			return err
		}
	}
	for _, classifier := range w.Classifiers {
		name := classifier.Name()
		if c.Done[name] {
			continue
		}
		original := c
		updated := *c
		updated.Done = maps.Clone(c.Done)
		updated.ClassifierErrors = maps.Clone(c.ClassifierErrors)
		updated.Classifications = make(map[string][]map[string]any, len(c.Classifications))
		for key, values := range c.Classifications {
			updated.Classifications[key] = slices.Clone(values)
		}
		c = &updated
		classCtx, span := w.callbackSpan(ctx, run, c, name, "classifier")
		values, err := classifier.Run(classCtx, c.scorerItem().TaskResult)
		err = errors.Join(err, setJSONAttr(span, "braintrust.input_json", c.scorerItem().TaskResult), setJSONAttr(span, "braintrust.output_json", values))
		if err != nil {
			recordSpanError(span, err)
		}
		span.End()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		grouped := map[string][]map[string]any{}
		if err == nil {
			for _, v := range values {
				if v.ID == "" {
					err = errors.New("classification ID is required")
					break
				}
				key := v.Name
				if key == "" {
					key = name
				}
				payload := map[string]any{"id": v.ID}
				if v.Label != "" {
					payload["label"] = v.Label
				}
				if v.Metadata != nil {
					payload["metadata"] = v.Metadata
				}
				grouped[key] = append(grouped[key], payload)
			}
		}
		if err != nil {
			c.ClassifierErrors[name] = err.Error()
		} else {
			for k, v := range grouped {
				c.Classifications[k] = append(c.Classifications[k], v...)
			}
		}
		c.Done[name] = true
		if w.caseDone(c) {
			c.FinishedAt = time.Now().UTC()
		}
		c.LogPending = true
		if err = w.saveCase(ctx, run.ID, c); err != nil {
			return err
		}
		*original = updated
		c = original
	}
	return nil
}

func (w *WorkflowEval[I, R]) caseDone(c *workflowCase[I, R]) bool {
	if !c.Done["task"] {
		return false
	}
	if c.TaskError != "" {
		return true
	}
	for _, s := range w.WorkflowScorers {
		if !c.Done[s.workflowScorer().name] {
			return false
		}
	}
	for _, s := range w.Scorers {
		if !c.Done[s.Name()] {
			return false
		}
	}
	for _, s := range w.Classifiers {
		if !c.Done[s.Name()] {
			return false
		}
	}
	return true
}

func (w *WorkflowEval[I, R]) result(run *workflowRun[I, R]) *WorkflowResult {
	r := &WorkflowResult{RunID: run.ID, ExperimentID: run.ExperimentID, Status: WorkflowRunWaiting, Scores: map[string]float64{}}
	if w.evaluator != nil {
		r.Permalink = experimentPermalink(w.evaluator.session, run.ExperimentID)
	}
	counts := map[string]int{}
	for i := range run.Cases {
		c := &run.Cases[i]
		if w.caseDone(c) {
			r.Completed++
		}
		if c.TaskError != "" || len(c.Errors) > 0 || len(c.ClassifierErrors) > 0 {
			r.Failed++
		}
		// Match experiment rows: each score name contributes once per case.
		row := w.caseScores(c)
		for k, v := range row {
			r.Scores[k] += v
			counts[k]++
		}
	}
	for k, n := range counts {
		r.Scores[k] /= float64(n)
	}
	for _, sub := range run.Submissions {
		if !sub.Done {
			stage := w.Task.workflowTask()
			if sub.Stage > 0 {
				stage = w.WorkflowScorers[sub.Stage-1].workflowScorer()
			}
			if stage.completion.GetExternalID != nil {
				r.PendingWebhook++
			} else {
				r.PendingPoll++
			}
		}
	}
	if r.Completed == len(run.Cases) && r.PendingPoll == 0 && r.PendingWebhook == 0 {
		r.Status = WorkflowRunCompleted
	}
	return r
}

func (w *WorkflowEval[I, R]) finish(ctx context.Context, run *workflowRun[I, R], err error) (*WorkflowResult, error) {
	result := w.result(run)
	// A known run ID is returned even on callback failures, so callers can resume.
	if !run.NoSendLogs {
		err = errors.Join(err, w.logRun(ctx, run))
	}
	return result, err
}

func (w *WorkflowEval[I, R]) caseContext(ctx context.Context, run *workflowRun[I, R], c *workflowCase[I, R]) context.Context {
	traceID, spanID := workflowTraceIDs(c.Item.ID)
	ctx = oteltrace.ContextWithSpanContext(ctx, oteltrace.NewSpanContext(oteltrace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.FlagsSampled}))
	if run.ExperimentID != "" {
		ctx = bttrace.SetParent(ctx, bttrace.NewParent(bttrace.ParentTypeExperimentID, run.ExperimentID))
	}
	return ctx
}

func workflowTraceIDs(id string) (oteltrace.TraceID, oteltrace.SpanID) {
	hash := sha256.Sum256([]byte(id))
	var traceID oteltrace.TraceID
	var spanID oteltrace.SpanID
	copy(traceID[:], hash[:16])
	copy(spanID[:], hash[16:24])
	return traceID, spanID
}

func (w *WorkflowEval[I, R]) caseScores(c *workflowCase[I, R]) map[string]float64 {
	result := map[string]float64{}
	add := func(name string) {
		for _, score := range c.Scores[name] {
			result[score.Name] = score.Score
		}
	}
	// Preserve declaration order instead of depending on Go map iteration.
	for _, s := range w.Scorers {
		add(s.Name())
	}
	for _, s := range w.WorkflowScorers {
		add(s.workflowScorer().name)
	}
	return result
}
