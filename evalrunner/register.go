package evalrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/internal/evalhooks"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

// registeredEval is the non-generic interface stored in the runner's map.
// It hides the type parameters behind JSON-based I/O.
type registeredEval interface {
	scorerNames() []string
	parameterSchema() eval.ParameterSchema
	resolveParams(values map[string]any) (eval.Parameters, error)
	projectName() string
	run(ctx context.Context, cfg *evalRunConfig) error
}

// evalRunConfig is everything one eval run needs that is not part of the
// definition itself.
type evalRunConfig struct {
	req     *EvalRequest
	session *session
	// sink receives protocol events. Never nil — a run with nothing listening
	// gets a discardSink.
	sink sink
	// tracerProvider is the user's shared provider, or nil to create one per run.
	tracerProvider *sdktrace.TracerProvider
	// parameters are this run's resolved values, produced by resolveParams
	// before the run starts so a bad value is reported without creating
	// anything in Braintrust.
	parameters eval.Parameters
	// noSendLogs keeps the run local (`bt eval --no-send-logs`): session is
	// nil, no experiment is created and no spans are exported.
	noSendLogs bool
}

// RegisterEval adds an eval definition to the runner. The type parameters I and
// R are the input and result types of the evaluation. Go does not allow generic
// methods on non-generic types, so this is a package-level function.
//
// The eval's Name is its lookup key: it is what the Braintrust playground shows
// and what bt sends back to select this eval. Registering two evals under the
// same name replaces the first.
//
// Example:
//
//	classify := &eval.Eval[string, string]{
//	    Name:    "classify",
//	    Task:    eval.T(classifyTask),
//	    Scorers: []eval.Scorer[string, string]{scorer},
//	}
//	evalrunner.RegisterEval(r, classify)
func RegisterEval[I, R any](r *Runner, ev *eval.Eval[I, R]) {
	if _, exists := r.evaluators[ev.Name]; !exists {
		r.order = append(r.order, ev.Name)
	}
	r.evaluators[ev.Name] = &registeredEvalImpl[I, R]{def: ev}
}

// registeredEvalImpl implements registeredEval by wrapping an [eval.Eval] definition.
type registeredEvalImpl[I, R any] struct {
	def *eval.Eval[I, R]
}

func (r *registeredEvalImpl[I, R]) scorerNames() []string {
	names := make([]string, len(r.def.Scorers))
	for i, s := range r.def.Scorers {
		names[i] = s.Name()
	}
	return names
}

func (r *registeredEvalImpl[I, R]) parameterSchema() eval.ParameterSchema {
	return r.def.ParameterSchema
}

func (r *registeredEvalImpl[I, R]) resolveParams(values map[string]any) (eval.Parameters, error) {
	return r.def.ParameterSchema.Resolve(values)
}

func (r *registeredEvalImpl[I, R]) projectName() string {
	return r.def.ProjectName
}

func (r *registeredEvalImpl[I, R]) run(ctx context.Context, cfg *evalRunConfig) error {
	req := cfg.req

	dataset, err := r.resolveDataset(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to resolve dataset: %w", err)
	}

	experimentName := req.ExperimentName
	if experimentName == "" {
		experimentName = r.def.Name
	}

	// Use the shared TracerProvider if one was provided, otherwise create a
	// per-run provider. A shared provider lets user-instrumented code (LLM
	// clients, custom spans) appear in the same trace as eval spans.
	tp := cfg.tracerProvider
	if tp == nil {
		tp = sdktrace.NewTracerProvider()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = tp.Shutdown(shutdownCtx)
		}()

		// A --no-send-logs run gets no exporter: its spans stay in-process.
		if !cfg.noSendLogs {
			traceCfg := bttrace.Config{DefaultProjectName: r.projectName()}
			if err := bttrace.AddSpanProcessor(tp, cfg.session.session, traceCfg); err != nil {
				return fmt.Errorf("failed to setup tracing: %w", err)
			}
		}
	}

	// A failed write means the far end is gone — under bt that means bt itself
	// died — so stop working rather than finish an eval nobody will read.
	evalCtx, cancelEval := context.WithCancel(ctx)
	defer cancelEval()

	scores := newScoreAccumulator()

	onComplete := func(cp evalhooks.CaseProgress) {
		// A task-level failure produces neither output nor scores (Scores is
		// nil); there is nothing to stream, and the error is recorded on the
		// span, which the UI reads separately.
		if cp.Scores == nil {
			return
		}

		// A case that reached scoring can still carry an error -- one scorer of
		// several may have failed -- but the scores that did succeed, and the
		// task output, are real. Accumulate before streaming so a partial
		// failure does not drop healthy scores from the summary average.
		scores.add(cp.Scores)

		// Only the output travels here. Per-case scores reach Braintrust as
		// OTLP spans, and the UI reads them from there.
		outputJSON, _ := json.Marshal(cp.Output)

		base := progressEvent{
			ID:         cp.ID,
			ObjectType: "task",
			Name:       r.def.Name,
			Format:     "code",
			OutputType: "completion",
			Origin:     cp.Origin,
		}

		delta := base
		delta.Event = "json_delta"
		delta.Data = string(outputJSON)
		if err := cfg.sink.send("progress", delta); err != nil {
			cancelEval()
			return
		}

		// Signals per-cell completion so the UI stops showing the task as running.
		done := base
		done.Event = "done"
		if err := cfg.sink.send("progress", done); err != nil {
			cancelEval()
			return
		}
	}

	// Tells the playground which experiment this run is writing to, while it is
	// still running, so the UI can link to it before the summary arrives.
	onExperimentStart := func(info evalhooks.ExperimentInfo) {
		if err := cfg.sink.send("start", startEvent{
			ProjectName:    info.ProjectName,
			ExperimentName: info.ExperimentName,
			ProjectID:      info.ProjectID,
			ExperimentID:   info.ExperimentID,
			ExperimentURL:  info.ExperimentURL,
		}); err != nil {
			cancelEval()
		}
	}

	spanParent, generation := parseParent(req.Parent)

	// Build a per-run evaluator with the caller's session rather than whatever
	// the Eval was constructed with, so traces are attributed to the user who
	// triggered this request.
	evaluator := eval.NewEvaluatorWithClient[I, R](cfg.session.authSession(), tp, cfg.session.apiClient(), r.projectName())
	e := eval.NewEval(evaluator, r.def)
	result, evalErr := e.Run(evalCtx, eval.RunOpts[I, R]{
		Experiment:  experimentName,
		Dataset:     dataset,
		ProjectID:   req.ProjectID,
		ProjectName: r.projectName(),
		Update:      true,
		Quiet:       true,
		Hooks: &evalhooks.Hooks{
			OnCaseComplete:    onComplete,
			OnExperimentStart: onExperimentStart,
		},
		SpanParent: spanParent,
		Generation: generation,
		Parameters: cfg.parameters,
		NoSendLogs: cfg.noSendLogs,
	})

	// Flush before the summary so the UI can poll for scores the moment it
	// arrives. This process is about to exit, so nothing else will drain them.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer flushCancel()
	_ = tp.ForceFlush(flushCtx)

	summary := newSummaryEvent(scores.averages(), resultRef(result))
	// bt requires these; fall back to what we asked for when the run failed
	// before the experiment existed.
	if summary.ExperimentName == "" {
		summary.ExperimentName = experimentName
	}
	if summary.ProjectName == "" {
		summary.ProjectName = r.projectName()
	}
	if err := cfg.sink.send("summary", summary); err != nil {
		return fmt.Errorf("failed to write summary: %w", err)
	}

	return evalErr
}

// resultRef adapts *eval.Result to experimentRef, mapping a nil result to a nil
// interface rather than a non-nil interface holding a nil pointer.
func resultRef(result *eval.Result) experimentRef {
	if result == nil {
		return nil
	}
	return result
}

// parseParent extracts the span parent and generation from the playground's
// parent block, which links this run's spans back to the playground session.
func parseParent(parent *ParentInfo) (bttrace.Parent, any) {
	if parent == nil || parent.ObjectID == "" {
		return bttrace.Parent{}, nil
	}

	// The request says object_type "playground_logs", but the span parent must
	// be "playground_id" for the UI to find the spans. Matches Ruby and Java.
	spanParent := bttrace.NewParent("playground_id", parent.ObjectID)

	var generation any
	if len(parent.PropagatedEvent) > 0 {
		var pe struct {
			SpanAttributes struct {
				Generation any `json:"generation"`
			} `json:"span_attributes"`
		}
		if json.Unmarshal(parent.PropagatedEvent, &pe) == nil {
			generation = pe.SpanAttributes.Generation
		}
	}

	return spanParent, generation
}

// resolveDataset resolves the request data into a typed Dataset.
//
// Returning (nil, nil) is meaningful: it means the request named no data
// source, so the eval's own [eval.Eval.Dataset] applies. That is the normal
// case for `bt eval <dir>` and for a direct run.
func (r *registeredEvalImpl[I, R]) resolveDataset(ctx context.Context, cfg *evalRunConfig) (eval.Dataset[I, R], error) {
	data := cfg.req.Data

	sourceCount := 0
	if len(data.Data) > 0 {
		sourceCount++
	}
	if data.DatasetID != "" {
		sourceCount++
	}
	if data.DatasetName != "" {
		sourceCount++
	}
	switch {
	case sourceCount == 0:
		return nil, nil
	case sourceCount > 1:
		return nil, fmt.Errorf("at most one of data, dataset_id, or dataset_name may be specified")
	}

	if len(data.Data) > 0 {
		return r.parseInlineData(data.Data)
	}

	if cfg.session == nil {
		return nil, fmt.Errorf("dataset resolution requires authentication")
	}

	evaluator := eval.NewEvaluatorWithClient[I, R](cfg.session.session, nil, cfg.session.api, r.projectName())
	dsAPI := evaluator.Datasets()

	if data.DatasetID != "" {
		return dsAPI.Get(ctx, data.DatasetID)
	}

	// bt resolves the playground's dataset_id into {project_id, dataset_name}
	// before spawning us, so project_id is the scoping key that actually
	// arrives. See bt/src/eval.rs:1256.
	return dsAPI.Query(ctx, eval.DatasetQueryOpts{
		Name:        data.DatasetName,
		ProjectID:   data.ProjectID,
		ProjectName: data.ProjectName,
	})
}

// parseInlineData unmarshals raw JSON into typed Cases.
func (r *registeredEvalImpl[I, R]) parseInlineData(raw json.RawMessage) (eval.Dataset[I, R], error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return nil, fmt.Errorf("failed to parse inline data array: %w", err)
	}

	cases := make([]eval.Case[I, R], 0, len(rawItems))
	for i, item := range rawItems {
		var c eval.Case[I, R]
		if err := json.Unmarshal(item, &c); err != nil {
			return nil, fmt.Errorf("failed to parse case %d: %w", i, err)
		}
		cases = append(cases, c)
	}

	return eval.NewDataset(cases), nil
}

// scoreAccumulator averages each score across the cases that produced it.
// Cases complete on worker goroutines, so it must be safe for concurrent use.
type scoreAccumulator struct {
	mu     sync.Mutex
	sums   map[string]float64
	counts map[string]int
}

func newScoreAccumulator() *scoreAccumulator {
	return &scoreAccumulator{
		sums:   make(map[string]float64),
		counts: make(map[string]int),
	}
}

func (a *scoreAccumulator) add(scores map[string]float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for name, val := range scores {
		a.sums[name] += val
		a.counts[name]++
	}
}

func (a *scoreAccumulator) averages() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	averages := make(map[string]float64, len(a.sums))
	for name, sum := range a.sums {
		if count := a.counts[name]; count > 0 {
			averages[name] = sum / float64(count)
		}
	}
	return averages
}
