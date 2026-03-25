// Package eval provides tools for evaluating AI model outputs.
// Evaluations help measure AI application performance (accuracy/quality) and create
// an effective feedback loop for AI development. They help teams understand if
// updates improve or regress application quality. Evaluations are a key part of
// the Braintrust platform.
//
// An evaluation consists of three main components:
//   - [Dataset]: A set of test examples with inputs and expected outputs
//   - [TaskFunc]: The unit of work we are evaluating, usually one or more calls to an LLM
//   - [Scorer]: A function that scores the result of a task against the expected result
//
// # Type Parameters
//
// This package uses two generic type parameters throughout its API:
//   - I: The input type for the task (e.g., string, struct, []byte)
//   - R: The result/output type from the task (e.g., string, struct, complex types)
//
// All of the input and result types must be JSON-encodable.
//
// For example:
//   - [Case][string, string] is a test case with string input and string output
//   - [TaskFunc][Input, Output] is a task that takes Input and returns Output
//   - [Dataset][string, bool] is an iterator over Cases with string inputs and boolean outputs
//
// See [Evaluator.Run] for running evaluations.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

var (
	// Private error variables (users don't need to check these)
	errEval         = errors.New("eval error")
	errScorer       = errors.New("scorer error")
	errClassifier   = errors.New("classifier error")
	errTaskRun      = errors.New("task run error")
	errCaseIterator = errors.New("case iterator error")
)

var (
	// braintrust "span_attributes" for each type of eval span.
	evalSpanAttrs = map[string]any{"type": "eval"}
	taskSpanAttrs = map[string]any{"type": "task"}
)

// Opts defines the options for running an evaluation.
// I is the input type and R is the result/output type.
//
// Dataset can be in-memory cases created with [NewDataset] or API-backed datasets
// loaded with [Evaluator.Datasets].
//
// Task can be a [TaskFunc], a function wrapped with [T], or a hosted task function
// loaded with [Evaluator.Functions().Task].
//
// Scorers can be local functions created with [NewScorer] or hosted scorer functions
// loaded with [Evaluator.Functions().Scorer].
type Opts[I, R any] struct {
	// Required
	Experiment string
	Dataset    Dataset[I, R]
	Task       TaskFunc[I, R]

	// At least one of Scorers or Classifiers must be provided.
	// Scorers return numeric scores; Classifiers return categorical labels.
	// They run in parallel for each case and are independent of each other.
	Scorers     []Scorer[I, R]
	Classifiers []Classifier[I, R]

	// Optional
	ProjectName string   // Project name (uses default from config if not specified)
	Tags        []string // Tags to apply to the experiment
	Metadata    Metadata // Metadata to attach to the experiment
	Update      bool     // If true, append to existing experiment (default: false)
	Parallelism int      // Number of goroutines (default: 1)
	TrialCount  int      // Number of times to run each case (default: 1)
	Quiet       bool     // Suppress result output (default: false)

	// OnCaseComplete is called after each case completes (task + scorers).
	// It is called from worker goroutines and must be safe for concurrent use.
	// Optional — nil means no callback.
	OnCaseComplete func(CaseProgress)
}

// CaseProgress contains the result of a single completed evaluation case.
// It is passed to the [Opts.OnCaseComplete] callback.
type CaseProgress struct {
	Output any
	Scores map[string]float64
	Error  error
}

// Case represents a single test case in an evaluation.
type Case[I, R any] struct {
	// Input is the input to the task function.
	Input I

	// Expected is the expected output (for scoring).
	// Optional.
	Expected R

	// Tags are labels to attach to this case.
	// Optional.
	Tags []string

	// Metadata is additional metadata for this case.
	// Optional.
	Metadata map[string]interface{}

	// TrialCount overrides Opts.TrialCount for this case.
	// Optional. Values less than 1 use the evaluation default.
	TrialCount int

	// These fields are only set if the Case is part of a Dataset.
	// They link the eval result back to the source dataset row.
	ID      string // Dataset record ID
	XactID  string // Transaction ID
	Created string // Creation timestamp
}

// Dataset is an iterator interface for evaluation datasets. It is commonly
// an in-memory slice of cases, but can also be a dataset lazily loaded from the Braintrust API.
type Dataset[I, R any] interface {
	// Next returns the next case, or io.EOF if there are no more cases.
	Next() (Case[I, R], error)

	// ID returns the dataset ID if backed by a Braintrust dataset.
	// Returns empty string for literal in-memory cases.
	ID() string

	// Version returns the dataset version if applicable.
	// Returns empty string for literal cases or unversioned datasets.
	Version() string
}

// Metadata is a map of strings to a JSON-encodable value.
type Metadata map[string]any

// Result contains the results of an evaluation.
type Result struct {
	key       key
	err       error
	elapsed   time.Duration
	permalink string
}

// key contains the data needed to uniquely identify and reference an eval.
// This is used internally by Result and is not exported.
type key struct {
	experimentID string
	name         string
	projectID    string
	projectName  string
}

// newResult creates a new Result with the given parameters.
func newResult(k key, err error, permalink string, elapsed time.Duration) *Result {
	return &Result{
		err:       err,
		permalink: permalink,
		elapsed:   elapsed,
		key:       k,
	}
}

// Permalink returns link to this eval in the Braintrust UI.
func (r *Result) Permalink() (string, error) {
	return r.permalink, nil
}

// Error returns an errors that were encountered while running the eval.
func (r *Result) Error() error {
	return r.err
}

// Name returns the experiment name.
func (r *Result) Name() string {
	return r.key.name
}

// ID returns the experiment ID.
func (r *Result) ID() string {
	return r.key.experimentID
}

// String returns a string representaton of the result for printing on the console.
//
// The format it prints will change and shouldn't be relied on for programmatic use.
func (r *Result) String() string {
	link, linkErr := r.Permalink()

	projectDisplay := r.key.projectName
	if projectDisplay == "" {
		projectDisplay = r.key.projectID
	}

	lines := []string{
		"",
		fmt.Sprintf("=== Experiment: %s ===", r.key.name),
		fmt.Sprintf("Name: %s", r.key.name),
		fmt.Sprintf("Project: %s", projectDisplay),
		fmt.Sprintf("Duration: %.1fs", r.elapsed.Seconds()),
		fmt.Sprintf("Link: %s", link),
	}
	if linkErr != nil {
		lines = append(lines, fmt.Sprintf("Warning: Failed to generate permalink: %v", linkErr))
	}

	// Error details if present
	if r.err != nil {
		lines = append(lines, "Errors:")
		lines = append(lines, "  "+r.err.Error())
	}

	lines = append(lines, "")

	// Join all lines
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

// eval (private) is the execution engine for evaluations.
// It is created by newEval() and run via Run().
type eval[I, R any] struct {
	session        *auth.Session
	parent         bttrace.Parent
	experimentID   string
	experimentName string
	projectID      string
	projectName    string
	dataset        Dataset[I, R]
	datasetID      string // For origin.object_id
	task           TaskFunc[I, R]
	scorers        []Scorer[I, R]
	classifiers    []Classifier[I, R]
	tracer         oteltrace.Tracer
	startSpanOpt   oteltrace.SpanStartOption
	goroutines     int
	trialCount     int
	quiet          bool
	onCaseComplete func(CaseProgress)
}

// nextCase is a wrapper for sending cases through a channel.
type nextCase[I, R any] struct {
	c          Case[I, R]
	trialIndex int
	iterErr    error
}

// newEval creates a new eval executor from concrete parameters (low-level constructor).
// This is the shared code path used by both newEvalOpts (production) and testNewEval (tests).
func newEval[I, R any](
	s *auth.Session,
	tracer oteltrace.Tracer,
	experimentID string,
	experimentName string,
	projectID string,
	projectName string,
	dataset Dataset[I, R],
	task TaskFunc[I, R],
	scorers []Scorer[I, R],
	classifiers []Classifier[I, R],
	parallelism int,
	trialCount int,
	quiet bool,
	onCaseComplete func(CaseProgress),
) *eval[I, R] {
	// Build parent span option
	parent := bttrace.NewParent(bttrace.ParentTypeExperimentID, experimentID)
	startSpanOpt := oteltrace.WithAttributes(parent.Attr())

	// Extract dataset ID from dataset
	datasetID := dataset.ID()

	// Set parallelism
	goroutines := parallelism
	if goroutines < 1 {
		goroutines = 1
	}

	if trialCount < 1 {
		trialCount = 1
	}

	return &eval[I, R]{
		session:        s,
		parent:         parent,
		experimentID:   experimentID,
		experimentName: experimentName,
		projectID:      projectID,
		projectName:    projectName,
		dataset:        dataset,
		datasetID:      datasetID,
		task:           task,
		scorers:        scorers,
		classifiers:    classifiers,
		tracer:         tracer,
		startSpanOpt:   startSpanOpt,
		goroutines:     goroutines,
		trialCount:     trialCount,
		quiet:          quiet,
		onCaseComplete: onCaseComplete,
	}
}

// newEvalOpts creates a new eval executor with dependency injection.
// This replaces the old New() constructor which used global state.
func newEvalOpts[I, R any](ctx context.Context, s *auth.Session, tp *trace.TracerProvider, apiClient *api.API, opts Opts[I, R], project string) (*eval[I, R], error) {
	// Determine project name (use opts.ProjectName if specified, otherwise project)
	projectName := opts.ProjectName
	if projectName == "" {
		projectName = project
	}

	// Register/get experiment (registerExperiment will validate that projectName is not empty)
	exp, err := registerExperiment(ctx, apiClient, opts.Experiment, projectName, opts.Tags, opts.Metadata, opts.Update, opts.Dataset)
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}

	// Get project ID from the experiment (it already has the project ID)
	projectID := exp.ProjectID

	// Create tracer from injected TracerProvider (instead of global)
	tracer := tp.Tracer("braintrust.eval")

	// Call low-level newEval with concrete parameters
	return newEval(
		s,
		tracer,
		exp.ID,
		exp.Name,
		projectID,
		projectName,
		opts.Dataset,
		opts.Task,
		opts.Scorers,
		opts.Classifiers,
		opts.Parallelism,
		opts.TrialCount,
		opts.Quiet,
		opts.OnCaseComplete,
	), nil
}

func (e *eval[I, R]) run(ctx context.Context) (*Result, error) {
	start := time.Now()
	if e.experimentID == "" {
		return nil, fmt.Errorf("%w: experiment ID is required", errEval)
	}

	ctx = bttrace.SetParent(ctx, e.parent)

	// Scale buffer size with parallelism to avoid blocking, but cap at 100
	bufferSize := minInt(e.goroutines*2, 100)
	nextCases := make(chan nextCase[I, R], bufferSize)
	var errs lockedErrors

	// Spawn our goroutines to run the cases.
	var wg sync.WaitGroup
	for i := 0; i < e.goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				nextCase, ok := <-nextCases
				if !ok {
					return
				}
				if err := e.runNextCase(ctx, nextCase); err != nil {
					errs.append(err)
				}
			}
		}()
	}

	// Fill our channel with the cases.
	for {
		c, err := e.dataset.Next()
		if err == io.EOF {
			close(nextCases)
			break
		}
		if err != nil {
			nextCases <- nextCase[I, R]{iterErr: err}
			continue
		}
		trialCount := c.TrialCount
		if trialCount < 1 {
			trialCount = e.trialCount
		}
		for trialIndex := range trialCount {
			nextCases <- nextCase[I, R]{c: c, trialIndex: trialIndex, iterErr: err}
		}
	}

	// Wait for all the goroutines to finish.
	wg.Wait()
	elapsed := time.Since(start)

	err := errors.Join(errs.get()...)

	permalink := e.permalink()
	result := newResult(
		key{
			experimentID: e.experimentID,
			name:         e.experimentName,
			projectID:    e.projectID,
			projectName:  e.projectName,
		},
		err,
		permalink,
		elapsed,
	)

	// Print result summary unless quiet
	if !e.quiet {
		fmt.Println(result.String())
	}

	return result, err
}

// runNextCase handles a single case from the channel.
// Copied from old package.
func (e *eval[I, R]) runNextCase(ctx context.Context, nextCase nextCase[I, R]) error {
	// if we have a case or get an error, we'll create a span.
	ctx, span := e.tracer.Start(ctx, "eval", e.startSpanOpt)
	defer span.End()

	// if our case iterator returns an error, we'll wrap it in a more
	// specific error and short circuit.
	if nextCase.iterErr != nil {
		werr := fmt.Errorf("%w: %w", errCaseIterator, nextCase.iterErr)
		recordSpanError(span, werr)
		return werr
	}

	// otherwise let's run the case (using the existing span)
	return e.runCase(ctx, span, nextCase.c, nextCase.trialIndex)
}

// runCase orchestrates task + scorers for one case.
func (e *eval[I, R]) runCase(ctx context.Context, span oteltrace.Span, c Case[I, R], trialIndex int) error {
	// Set all non-output attributes upfront so they're captured even if the task fails.
	attrs := map[string]any{
		"braintrust.span_attributes": evalSpanAttrs,
		"braintrust.input_json":      c.Input,
		"braintrust.expected":        c.Expected,
	}
	if c.Metadata != nil {
		attrs["braintrust.metadata"] = c.Metadata
	}
	if c.ID != "" && c.XactID != "" {
		attrs["braintrust.origin"] = map[string]any{
			"object_type": "dataset",
			"object_id":   e.datasetID,
			"id":          c.ID,
			"created":     c.Created,
			"_xact_id":    c.XactID,
		}
	}
	if err := setJSONAttrs(span, attrs); err != nil {
		return err
	}
	if c.Tags != nil {
		span.SetAttributes(attribute.StringSlice("braintrust.tags", c.Tags))
	}

	taskResult, err := e.runTask(ctx, span, c, trialIndex)
	if err != nil {
		if e.onCaseComplete != nil {
			e.onCaseComplete(CaseProgress{Error: err})
		}
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := setJSONAttr(span, "braintrust.output_json", taskResult.Output); err != nil {
		return err
	}

	// Run scorers and classifiers in parallel. Each pass is independent —
	// a failing scorer must not block classifiers (and vice versa), and
	// per-pass errors are aggregated rather than fatal.
	var (
		scores        []Score
		scorerErr     error
		classifierErr error
		wg            sync.WaitGroup
	)
	if len(e.scorers) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scores, scorerErr = e.runScorers(ctx, taskResult)
		}()
	}
	if len(e.classifiers) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			classifierErr = e.runClassifiers(ctx, span, c, taskResult)
		}()
	}
	wg.Wait()

	joined := errors.Join(scorerErr, classifierErr)

	if e.onCaseComplete != nil {
		scoreMap := make(map[string]float64, len(scores))
		for _, s := range scores {
			scoreMap[s.Name] = s.Score
		}
		e.onCaseComplete(CaseProgress{
			Output: taskResult.Output,
			Scores: scoreMap,
			Error:  joined,
		})
	}

	if joined != nil {
		span.SetStatus(codes.Error, joined.Error())
		return joined
	}
	return nil
}

// runTask executes the task function and creates a task span.
// Returns a TaskResult containing all task execution data.
func (e *eval[I, R]) runTask(ctx context.Context, evalSpan oteltrace.Span, c Case[I, R], trialIndex int) (TaskResult[I, R], error) {
	ctx, taskSpan := e.tracer.Start(ctx, "task", e.startSpanOpt)
	defer taskSpan.End()

	attrs := map[string]any{
		"braintrust.input_json":      c.Input,
		"braintrust.expected":        c.Expected,
		"braintrust.span_attributes": taskSpanAttrs,
	}

	var encodeErrs []error
	for key, value := range attrs {
		if err := setJSONAttr(taskSpan, key, value); err != nil {
			encodeErrs = append(encodeErrs, err)
		}
	}

	// Construct TaskHooks with both spans and case data
	hooks := &TaskHooks{
		Expected:   c.Expected,
		Metadata:   c.Metadata,
		Tags:       c.Tags,
		TrialIndex: trialIndex,
		TaskSpan:   taskSpan,
		EvalSpan:   evalSpan,
	}

	// Call task with new signature
	taskOutput, err := e.task(ctx, c.Input, hooks)
	if err != nil {
		taskErr := fmt.Errorf("%w: %w", errTaskRun, err)
		recordSpanError(taskSpan, taskErr)
		return TaskResult[I, R]{}, taskErr
	}

	// Extract value and UserData from TaskOutput
	result := taskOutput.Value
	userData := taskOutput.UserData // Not logged - for in-process use only

	if err := setJSONAttr(taskSpan, "braintrust.output_json", result); err != nil {
		encodeErrs = append(encodeErrs, err)
	}

	taskResult := TaskResult[I, R]{
		Input:    c.Input,
		Expected: c.Expected,
		Output:   result,
		Metadata: c.Metadata,
		UserData: userData,
	}

	return taskResult, errors.Join(encodeErrs...)
}

// runScorers runs each scorer in its own span and returns all scores.
func (e *eval[I, R]) runScorers(ctx context.Context, taskResult TaskResult[I, R]) ([]Score, error) {
	var allScores []Score
	var errs []error

	for _, scorer := range e.scorers {
		scores, err := e.runScorer(ctx, scorer, taskResult)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		allScores = append(allScores, scores...)
	}

	return allScores, errors.Join(errs...)
}

// runScorer executes a single scorer in its own span named after the scorer.
func (e *eval[I, R]) runScorer(ctx context.Context, scorer Scorer[I, R], taskResult TaskResult[I, R]) ([]Score, error) {
	ctx, span := e.tracer.Start(ctx, scorer.Name(), e.startSpanOpt)
	defer span.End()

	spanAttrs := map[string]any{
		"type":    "score",
		"name":    scorer.Name(),
		"purpose": "scorer",
	}
	if err := setJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		return nil, err
	}

	// Log the task result (input/expected/output) as the scorer's input.
	scorerInput := map[string]any{
		"input":    taskResult.Input,
		"expected": taskResult.Expected,
		"output":   taskResult.Output,
	}
	if err := setJSONAttr(span, "braintrust.input_json", scorerInput); err != nil {
		return nil, err
	}

	scores, err := scorer.Run(ctx, taskResult)
	if err != nil {
		werr := fmt.Errorf("%w: scorer %q failed: %w", errScorer, scorer.Name(), err)
		recordSpanError(span, werr)
		return nil, werr
	}

	// Normalize score names.
	for i := range scores {
		if scores[i].Name == "" {
			scores[i].Name = scorer.Name()
		}
	}

	// Build scores map (name -> value).
	valsByName := make(map[string]float64, len(scores))
	for _, score := range scores {
		valsByName[score.Name] = score.Score
	}
	if err := setJSONAttr(span, "braintrust.scores", valsByName); err != nil {
		return nil, err
	}

	// For a single score: flatten metadata/output to top level.
	// For multiple scores returned by one scorer: nest by name.
	if len(scores) == 1 {
		score := scores[0]
		if score.Metadata != nil {
			if err := setJSONAttr(span, "braintrust.metadata", score.Metadata); err != nil {
				return nil, err
			}
		}
		if err := setJSONAttr(span, "braintrust.output", map[string]any{"score": score.Score}); err != nil {
			return nil, err
		}
	} else if len(scores) > 1 {
		metadata := make(map[string]any, len(scores))
		output := make(map[string]any, len(scores))
		for _, score := range scores {
			if score.Metadata != nil {
				metadata[score.Name] = score.Metadata
			}
			output[score.Name] = map[string]any{"score": score.Score}
		}
		if len(metadata) > 0 {
			if err := setJSONAttr(span, "braintrust.metadata", metadata); err != nil {
				return nil, err
			}
		}
		if err := setJSONAttr(span, "braintrust.output", output); err != nil {
			return nil, err
		}
	}

	return scores, nil
}

// runClassifiers runs each classifier concurrently in its own span,
// aggregates the resulting classifications by name onto the eval span,
// and records any per-classifier errors as non-fatal aggregate errors.
//
// Per the cross-SDK spec, classifier failures must not abort the eval
// or affect other classifiers/scorers; they are surfaced via
// braintrust.metadata.classifier_errors on the eval span and joined into
// the returned error.
func (e *eval[I, R]) runClassifiers(ctx context.Context, evalSpan oteltrace.Span, c Case[I, R], taskResult TaskResult[I, R]) error {
	// Run all classifiers in parallel. Each goroutine writes to its own
	// slot in `results`, so no synchronization is needed during execution.
	// We merge results sequentially below in declaration order to keep
	// cross-classifier aggregation deterministic.
	type classifierResult struct {
		spanName string
		items    []classificationItem
		err      error
	}
	results := make([]classifierResult, len(e.classifiers))
	var wg sync.WaitGroup
	for i, classifier := range e.classifiers {
		wg.Add(1)
		go func(i int, classifier Classifier[I, R]) {
			defer wg.Done()
			spanName := classifier.Name()
			if spanName == "" {
				spanName = fmt.Sprintf("classifier_%d", i)
			}
			items, err := e.runClassifier(ctx, spanName, classifier, taskResult)
			results[i] = classifierResult{spanName: spanName, items: items, err: err}
		}(i, classifier)
	}
	wg.Wait()

	aggregated := make(map[string][]map[string]any)
	classifierErrors := make(map[string]string)
	var errs []error

	for _, r := range results {
		if r.err != nil {
			classifierErrors[r.spanName] = r.err.Error()
			errs = append(errs, r.err)
			continue
		}

		// Group items by their resolved name. Items keep their stable order;
		// duplicates with the same {name, id} are preserved.
		for _, item := range r.items {
			aggregated[item.name] = append(aggregated[item.name], item.payload)
		}
	}

	// Log aggregated classifications onto the eval span, if any.
	if len(aggregated) > 0 {
		if err := setJSONAttr(evalSpan, "braintrust.classifications", aggregated); err != nil {
			errs = append(errs, err)
		}
	}

	// Merge classifier_errors into the eval span's metadata. The eval span's
	// metadata was set in runCase before classifiers ran, so we rewrite it
	// with the merged map.
	if len(classifierErrors) > 0 {
		merged := make(map[string]any, len(c.Metadata)+1)
		for k, v := range c.Metadata {
			merged[k] = v
		}
		merged["classifier_errors"] = classifierErrors
		if err := setJSONAttr(evalSpan, "braintrust.metadata", merged); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// classificationItem is the wire-format payload (id/label?/metadata?) paired
// with the resolved grouping name, used internally during aggregation.
type classificationItem struct {
	name    string
	payload map[string]any
}

// runClassifier executes a single classifier in its own span. It mirrors
// runScorer: a child span named after the classifier, with input_json set
// to the task result, and the classifier's output written to output_json.
func (e *eval[I, R]) runClassifier(ctx context.Context, spanName string, classifier Classifier[I, R], taskResult TaskResult[I, R]) ([]classificationItem, error) {
	ctx, span := e.tracer.Start(ctx, spanName, e.startSpanOpt)
	defer span.End()

	spanAttrs := map[string]any{
		"type":    "classifier",
		"name":    spanName,
		"purpose": "scorer",
	}
	if err := setJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		return nil, err
	}

	// Log the task result (input/expected/output) as the classifier's input.
	classifierInput := map[string]any{
		"input":    taskResult.Input,
		"expected": taskResult.Expected,
		"output":   taskResult.Output,
	}
	if err := setJSONAttr(span, "braintrust.input_json", classifierInput); err != nil {
		return nil, err
	}

	classifications, err := classifier.Run(ctx, taskResult)
	if err != nil {
		werr := fmt.Errorf("%w: classifier %q failed: %w", errClassifier, spanName, err)
		recordSpanError(span, werr)
		return nil, werr
	}

	// Normalize and validate each classification:
	//   - default missing/empty Name to the classifier's resolved span name
	//   - require a non-empty ID (spec: each item must be a non-empty object)
	items := make([]classificationItem, 0, len(classifications))
	outputByName := make(map[string][]map[string]any)
	for _, c := range classifications {
		if c.ID == "" {
			werr := fmt.Errorf("%w: classifier %q returned a classification with empty ID", errClassifier, spanName)
			recordSpanError(span, werr)
			return nil, werr
		}
		name := c.Name
		if name == "" {
			name = spanName
		}
		payload := map[string]any{"id": c.ID}
		if c.Label != "" {
			payload["label"] = c.Label
		}
		if len(c.Metadata) > 0 {
			payload["metadata"] = c.Metadata
		}
		items = append(items, classificationItem{name: name, payload: payload})
		outputByName[name] = append(outputByName[name], payload)
	}

	// Log the classifier's output: always grouped by name as a slice of
	// wire-format items, matching the spec's ClassificationItem shape.
	if len(outputByName) > 0 {
		if err := setJSONAttr(span, "braintrust.output_json", outputByName); err != nil {
			return nil, err
		}
	}

	return items, nil
}

// permalink generates a URL to view the eval in Braintrust UI.
func (e *eval[I, R]) permalink() string {
	appURL := e.session.AppPublicURL()
	orgName := e.session.OrgName()

	if appURL == "" {
		appURL = "https://www.braintrust.dev"
	}

	if orgName != "" && e.experimentID != "" {
		return fmt.Sprintf("%s/app/%s/object?object_type=experiment&object_id=%s", appURL, url.PathEscape(orgName), e.experimentID)
	}

	return ""
}

// run executes an evaluation using client resources (session, tracerProvider).
// This is an internal function - users should use Evaluator.Run() instead.
// apiClient can be nil, in which case one will be created for API calls.
func run[I, R any](ctx context.Context, opts Opts[I, R], s *auth.Session, tp *trace.TracerProvider, apiClient *api.API, project string) (*Result, error) {
	// Validate required fields
	if opts.Experiment == "" {
		return nil, fmt.Errorf("%w: Experiment is required", errEval)
	}
	if opts.Dataset == nil {
		return nil, fmt.Errorf("%w: Dataset is required", errEval)
	}
	if opts.Task == nil {
		return nil, fmt.Errorf("%w: Task is required", errEval)
	}
	if len(opts.Scorers) == 0 && len(opts.Classifiers) == 0 {
		return nil, fmt.Errorf("%w: at least one of Scorers or Classifiers is required", errEval)
	}

	// Create eval executor
	e, err := newEvalOpts(ctx, s, tp, apiClient, opts, project)
	if err != nil {
		return nil, err
	}

	// Run evaluation
	return e.run(ctx)
}

// Helper functions (copied from old package)

func setJSONAttrs(span oteltrace.Span, attrs map[string]any) error {
	for key, value := range attrs {
		if err := setJSONAttr(span, key, value); err != nil {
			return err
		}
	}
	return nil
}

func setJSONAttr(span oteltrace.Span, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.String(key, string(b)))
	return nil
}

func recordSpanError(span oteltrace.Span, err error) {
	// hardcode the error type when we know what it is. there may be better ways to do this
	// but by default otel would show *fmt.wrapErrors as the type, which isn't super nice to
	// look at. this function balances us returning errors which work with errors.Is() and
	// showing the actual error type in the braintrust ui.
	var errType string
	switch {
	case errors.Is(err, errScorer):
		errType = "ErrScorer"
	case errors.Is(err, errClassifier):
		errType = "ErrClassifier"
	case errors.Is(err, errTaskRun):
		errType = "ErrTaskRun"
	case errors.Is(err, errCaseIterator):
		errType = "ErrCaseIterator"
	case errors.Is(err, errEval):
		errType = "ErrEval"
	default:
		errType = fmt.Sprintf("%T", err)
	}

	span.AddEvent("exception", oteltrace.WithAttributes(
		attribute.String("exception.type", errType),
		attribute.String("exception.message", err.Error()),
	))
	span.SetStatus(codes.Error, err.Error())
}

// lockedErrors is a thread-safe list of errors.
type lockedErrors struct {
	mu   sync.Mutex
	errs []error
}

func (e *lockedErrors) append(err error) {
	e.mu.Lock()
	e.errs = append(e.errs, err)
	e.mu.Unlock()
}

func (e *lockedErrors) get() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.errs
}

// minInt returns the minimum of two integers (Go 1.21+ has this in stdlib)
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// testNewEval creates an eval for unit testing, bypassing API calls.
// This allows tests to inject static values for experiment/project IDs.
func testNewEval[I, R any](
	s *auth.Session,
	tracer oteltrace.Tracer,
	experimentID string,
	experimentName string,
	projectID string,
	projectName string,
	dataset Dataset[I, R],
	task TaskFunc[I, R],
	scorers []Scorer[I, R],
	classifiers []Classifier[I, R],
	parallelism int,
) *eval[I, R] {
	// Call low-level newEval with quiet=true for tests
	return newEval(
		s,
		tracer,
		experimentID,
		experimentName,
		projectID,
		projectName,
		dataset,
		task,
		scorers,
		classifiers,
		parallelism,
		1,    // trialCount=1 for tests
		true, // quiet=true for tests
		nil,  // no callback for tests
	)
}
