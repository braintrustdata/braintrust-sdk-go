// Package eval provides evaluation functionality for the Braintrust SDK.
// This package is designed to work with the client-based architecture and
// does not rely on global state.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
)

var (
	// Private error variables (users don't need to check these)
	errEval         = errors.New("eval error")
	errScorer       = errors.New("scorer error")
	errTaskRun      = errors.New("task run error")
	errCaseIterator = errors.New("case iterator error")
)

var (
	// braintrust "span_attributes" for each type of eval span.
	evalSpanAttrs  = map[string]any{"type": "eval"}
	taskSpanAttrs  = map[string]any{"type": "task"}
	scoreSpanAttrs = map[string]any{"type": "score"}
)

// Opts defines the options for running an evaluation.
// I is the input type and R is the result/output type.
type Opts[I, R any] struct {
	// Experiment is the name of the experiment to create or use.
	// Required.
	Experiment string

	// ProjectName is the name of the project to create the experiment in.
	// Optional. If not specified, uses the default project from config.
	ProjectName string

	// Cases is an iterator over the test cases to evaluate.
	// Required.
	Cases Cases[I, R]

	// Task is the function to evaluate for each case.
	// It receives the input and should return the output.
	// Required.
	Task TaskFunc[I, R]

	// Scorers are the scoring functions to apply to each case result.
	// Optional. If empty, no scoring is performed.
	Scorers []Scorer[I, R]

	// Tags are labels to attach to the experiment.
	// Optional.
	Tags []string

	// Metadata is additional metadata to attach to the experiment.
	// Optional.
	Metadata map[string]interface{}

	// Update controls whether to update an existing experiment or fail if it exists.
	// Optional. Defaults to false.
	Update bool

	// Parallelism controls the number of goroutines to use for parallel execution.
	// Optional. Defaults to 1 (sequential execution).
	// Set to a value > 1 to enable parallel case evaluation.
	Parallelism int

	// Quiet controls whether to suppress printing the result summary.
	// Optional. Defaults to false (summary is printed).
	// Set to true to suppress output.
	Quiet bool
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
}

// Cases is an iterator interface for test cases.
// This allows lazy loading of cases without requiring them all in memory.
// Implementations must return io.EOF when iteration is complete.
type Cases[I, R any] interface {
	// Next returns the next case, or io.EOF if there are no more cases.
	Next() (Case[I, R], error)
}

// Metadata is a map of strings to a JSON-encodable value. It is used to store arbitrary metadata about a case.
type Metadata map[string]any

// Result contains the results of an evaluation.
type Result struct {
	key       key
	err       error
	elapsed   time.Duration
	permalink string
	// TODO: Will be populated with span data, scores, errors, etc. in future iterations
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

// Error returns the error from running the eval.
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
	config         *config.Config
	session        *auth.Session
	experimentID   string
	experimentName string
	projectID      string
	projectName    string
	cases          Cases[I, R]
	task           TaskFunc[I, R]
	scorers        []Scorer[I, R]
	tracer         oteltrace.Tracer
	startSpanOpt   oteltrace.SpanStartOption
	goroutines     int
	quiet          bool
}

// nextCase is a wrapper for sending cases through a channel.
type nextCase[I, R any] struct {
	c       Case[I, R]
	iterErr error
}

// newEval creates a new eval executor with dependency injection.
// This replaces the old New() constructor which used global state.
func newEval[I, R any](ctx context.Context, cfg *config.Config, session *auth.Session, tp *trace.TracerProvider, opts Opts[I, R]) (*eval[I, R], error) {
	// Determine project name (use opts.ProjectName if specified, otherwise cfg.DefaultProjectName)
	projectName := opts.ProjectName
	if projectName == "" {
		projectName = cfg.DefaultProjectName
	}

	// Register/get experiment (registerExperiment will validate that projectName is not empty)
	exp, err := registerExperiment(ctx, cfg, session, opts.Experiment, projectName, opts.Tags, opts.Metadata, opts.Update)
	if err != nil {
		return nil, fmt.Errorf("failed to register experiment: %w", err)
	}

	// Get project ID from the experiment (it already has the project ID)
	projectID := exp.ProjectID

	// Create tracer from injected TracerProvider (instead of global)
	tracer := tp.Tracer("braintrust.eval")

	// Build parent span option
	parentAttr := fmt.Sprintf("experiment_id:%s", exp.ID)
	startSpanOpt := oteltrace.WithAttributes(attribute.String("braintrust.parent", parentAttr))

	// Set parallelism
	goroutines := opts.Parallelism
	if goroutines < 1 {
		goroutines = 1
	}

	return &eval[I, R]{
		config:         cfg,
		session:        session,
		experimentID:   exp.ID,
		experimentName: exp.Name,
		projectID:      projectID,
		projectName:    projectName,
		cases:          opts.Cases,
		task:           opts.Task,
		scorers:        opts.Scorers,
		tracer:         tracer,
		startSpanOpt:   startSpanOpt,
		goroutines:     goroutines,
		quiet:          opts.Quiet,
	}, nil
}

// run executes the evaluation with parallelism support.
// This is copied from the old Eval.Run() method.
func (e *eval[I, R]) run(ctx context.Context) (*Result, error) {
	start := time.Now()
	if e.experimentID == "" {
		return nil, fmt.Errorf("%w: experiment ID is required", errEval)
	}

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
		c, err := e.cases.Next()
		if err == io.EOF {
			close(nextCases)
			break
		}
		nextCases <- nextCase[I, R]{c: c, iterErr: err}
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
	return e.runCase(ctx, span, nextCase.c)
}

// runCase orchestrates task + scorers for one case.
// Copied from old package.
func (e *eval[I, R]) runCase(ctx context.Context, span oteltrace.Span, c Case[I, R]) error {
	if c.Tags != nil {
		span.SetAttributes(attribute.StringSlice("braintrust.tags", c.Tags))
	}

	result, err := e.runTask(ctx, span, c)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	_, err = e.runScorers(ctx, c, result)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	meta := map[string]any{
		"braintrust.span_attributes": evalSpanAttrs,
		"braintrust.input_json":      c.Input,
		"braintrust.output_json":     result,
		"braintrust.expected":        c.Expected,
	}

	// Add case metadata if present
	if c.Metadata != nil {
		meta["braintrust.metadata"] = c.Metadata
	}

	return setJSONAttrs(span, meta)
}

// runTask executes the task function and creates a task span.
// Copied from old package.
func (e *eval[I, R]) runTask(ctx context.Context, evalSpan oteltrace.Span, c Case[I, R]) (R, error) {
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
		Expected: c.Expected,
		Metadata: c.Metadata,
		Tags:     c.Tags,
		TaskSpan: taskSpan,
		EvalSpan: evalSpan,
	}

	// Call task with new signature
	taskOutput, err := e.task(ctx, c.Input, hooks)
	if err != nil {
		// if the task fails, don't worry about the encode errors....
		taskErr := fmt.Errorf("%w: %w", errTaskRun, err)
		recordSpanError(taskSpan, taskErr)
		var zero R
		return zero, taskErr
	}

	// Extract value from TaskOutput
	result := taskOutput.Value

	if err := setJSONAttr(taskSpan, "braintrust.output_json", result); err != nil {
		encodeErrs = append(encodeErrs, err)
	}

	return result, errors.Join(encodeErrs...)
}

// runScorers executes all scorers and creates a score span.
// Copied from old package.
func (e *eval[I, R]) runScorers(ctx context.Context, c Case[I, R], result R) ([]Score, error) {
	ctx, span := e.tracer.Start(ctx, "score", e.startSpanOpt)
	defer span.End()

	if err := setJSONAttr(span, "braintrust.span_attributes", scoreSpanAttrs); err != nil {
		return nil, err
	}

	var scores []Score

	// Construct TaskResult for scorers
	taskResult := TaskResult[I, R]{
		Input:    c.Input,
		Expected: c.Expected,
		Output:   result,
		Metadata: c.Metadata,
	}

	var errs []error
	for _, scorer := range e.scorers {
		curScores, err := scorer.Run(ctx, taskResult)
		if err != nil {
			werr := fmt.Errorf("%w: scorer %q failed: %w", errScorer, scorer.Name(), err)
			recordSpanError(span, werr)
			errs = append(errs, werr)
			continue
		}
		for _, score := range curScores {
			if score.Name == "" {
				score.Name = scorer.Name()
			}
			scores = append(scores, score)
		}
	}

	// Build scores map (name -> score value)
	valsByName := make(map[string]float64, len(scores))
	for _, score := range scores {
		valsByName[score.Name] = score.Score
	}

	if err := setJSONAttr(span, "braintrust.scores", valsByName); err != nil {
		return nil, err
	}

	// Build metadata and output following Python/TypeScript conventions
	// Always build nested structure, then flatten if single score
	metadata := make(map[string]any, len(scores))
	output := make(map[string]any, len(scores))

	for _, score := range scores {
		if score.Metadata != nil {
			metadata[score.Name] = score.Metadata
		}
		output[score.Name] = map[string]any{"score": score.Score}
	}

	// For single score: flatten metadata and output to top level
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
		// Multiple scores: use nested structure
		if len(metadata) > 0 {
			if err := setJSONAttr(span, "braintrust.metadata", metadata); err != nil {
				return nil, err
			}
		}
		if err := setJSONAttr(span, "braintrust.output", output); err != nil {
			return nil, err
		}
	}

	err := errors.Join(errs...) // will be nil if there are no errors
	return scores, err
}

// permalink generates a URL to view the eval in Braintrust UI.
// Copied from old package but adapted for injected dependencies.
func (e *eval[I, R]) permalink() string {
	appURL := e.config.AppURL
	orgName := e.config.OrgName

	// Try to get from session if login is complete
	if ok, info := e.session.Info(); ok {
		if appURL == "" && info.AppPublicURL != "" {
			appURL = info.AppPublicURL
		}
		if orgName == "" && info.OrgName != "" {
			orgName = info.OrgName
		}
	}

	if appURL == "" {
		appURL = "https://www.braintrust.dev"
	}

	if orgName != "" && e.experimentID != "" {
		return fmt.Sprintf("%s/app/%s/object?object_type=experiment&object_id=%s", appURL, orgName, e.experimentID)
	}

	return ""
}

// Run executes an evaluation using client resources (config, session, tracerProvider).
// This is the main entry point for eval execution.
func Run[I, R any](ctx context.Context, opts Opts[I, R], cfg *config.Config, session *auth.Session, tp *trace.TracerProvider) (*Result, error) {
	// Validate required fields
	if opts.Experiment == "" {
		return nil, fmt.Errorf("%w: Experiment is required", errEval)
	}
	if opts.Cases == nil {
		return nil, fmt.Errorf("%w: Cases is required", errEval)
	}
	if opts.Task == nil {
		return nil, fmt.Errorf("%w: Task is required", errEval)
	}

	// Create eval executor
	e, err := newEval(ctx, cfg, session, tp, opts)
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
	cfg *config.Config,
	session *auth.Session,
	tracer oteltrace.Tracer,
	experimentID string,
	experimentName string,
	projectID string,
	projectName string,
	cases Cases[I, R],
	task TaskFunc[I, R],
	scorers []Scorer[I, R],
	parallelism int,
) *eval[I, R] {
	// Build parent span option
	parentAttr := fmt.Sprintf("experiment_id:%s", experimentID)
	startSpanOpt := oteltrace.WithAttributes(attribute.String("braintrust.parent", parentAttr))

	// Set parallelism
	goroutines := parallelism
	if goroutines < 1 {
		goroutines = 1
	}

	return &eval[I, R]{
		config:         cfg,
		session:        session,
		experimentID:   experimentID,
		experimentName: experimentName,
		projectID:      projectID,
		projectName:    projectName,
		cases:          cases,
		task:           task,
		scorers:        scorers,
		tracer:         tracer,
		startSpanOpt:   startSpanOpt,
		goroutines:     goroutines,
		quiet:          true,
	}
}
