package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

// registeredEval is the non-generic interface stored in the server's evaluator map.
// It hides the type parameters behind JSON-based I/O.
type registeredEval interface {
	scorerNames() []string
	parameters() *Parameters
	projectName() string
	run(ctx context.Context, cfg *evalRunConfig) error
}

// evalRunConfig holds the per-request configuration for running an evaluation.
type evalRunConfig struct {
	req            *EvalRequest
	auth           *authResult
	sse            *sseWriter
	noAuth         bool
	tracerProvider *sdktrace.TracerProvider // nil means create per-request
}

// RegisterOpts configures a registered evaluator.
type RegisterOpts struct {
	// Parameters defines the parameter schema shown in the Braintrust UI.
	Parameters *Parameters

	// ProjectName is the default project for this evaluator.
	ProjectName string
}

// Register adds an eval definition to the server. The type parameters I and R
// are the input and result types of the evaluation. Go does not allow generic
// methods on non-generic types, so this is a package-level function.
//
// Example:
//
//	classify := &eval.Eval[string, string]{
//	    Name:    "classify",
//	    Task:    eval.T(classifyTask),
//	    Scorers: []eval.Scorer[string, string]{scorer},
//	}
//	server.Register(srv, classify, server.RegisterOpts{})
func Register[I, R any](s *Server, ev *eval.Eval[I, R], opts RegisterOpts) {
	impl := &registeredEvalImpl[I, R]{
		def:  ev,
		opts: opts,
	}

	s.evalsMu.Lock()
	defer s.evalsMu.Unlock()
	s.evaluators[ev.Name] = impl
}

// registeredEvalImpl implements registeredEval by wrapping an [eval.Eval] definition.
type registeredEvalImpl[I, R any] struct {
	def  *eval.Eval[I, R]
	opts RegisterOpts
}

func (r *registeredEvalImpl[I, R]) scorerNames() []string {
	names := make([]string, len(r.def.Scorers))
	for i, s := range r.def.Scorers {
		names[i] = s.Name()
	}
	return names
}

func (r *registeredEvalImpl[I, R]) parameters() *Parameters {
	return r.opts.Parameters
}

func (r *registeredEvalImpl[I, R]) projectName() string {
	if r.opts.ProjectName != "" {
		return r.opts.ProjectName
	}
	return r.def.ProjectName
}

func (r *registeredEvalImpl[I, R]) run(ctx context.Context, cfg *evalRunConfig) error {
	req := cfg.req

	// Resolve inline data into typed cases
	dataset, err := r.resolveDataset(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to resolve dataset: %w", err)
	}

	// Determine experiment name
	experimentName := req.ExperimentName
	if experimentName == "" {
		experimentName = r.def.Name
	}

	// Use the shared TracerProvider if one was provided, otherwise create a
	// per-request provider. A shared provider allows user-instrumented code
	// (LLM clients, custom spans) to appear in the same trace as eval spans.
	tp := cfg.tracerProvider
	if tp == nil {
		tp = sdktrace.NewTracerProvider()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = tp.Shutdown(shutdownCtx)
		}()

		// Per-request provider needs its own Braintrust span processor
		traceCfg := bttrace.Config{
			DefaultProjectName: r.projectName(),
		}
		if err := bttrace.AddSpanProcessor(tp, cfg.auth.session, traceCfg); err != nil {
			return fmt.Errorf("failed to setup tracing: %w", err)
		}
	}

	apiClient := cfg.auth.api

	// Cancel eval if the client disconnects (SSE write fails)
	evalCtx, cancelEval := context.WithCancel(ctx)
	defer cancelEval()

	// Track scores across cases for the summary
	var scoresMu sync.Mutex
	scoreSums := make(map[string]float64)
	scoreCounts := make(map[string]int)

	// Build OnCaseComplete callback to stream progress via SSE
	onComplete := func(cp eval.CaseProgress) {
		// Skip score accumulation and progress on error
		if cp.Error != nil {
			return
		}

		// Accumulate scores for summary
		scoresMu.Lock()
		for name, val := range cp.Scores {
			scoreSums[name] += val
			scoreCounts[name]++
		}
		scoresMu.Unlock()

		// JSON-encode just the output, matching Ruby's protocol.
		// Scores are delivered via OTLP spans, not SSE progress events.
		outputJSON, _ := json.Marshal(cp.Output)

		// Stream progress event; cancel eval if write fails (client disconnected)
		if err := cfg.sse.writeProgress(progressEvent{
			ID:         cp.ID,
			ObjectType: "task",
			Name:       r.def.Name,
			Format:     "code",
			OutputType: "completion",
			Event:      "json_delta",
			Data:       string(outputJSON),
			Origin:     cp.Origin,
		}); err != nil {
			cancelEval()
			return
		}

		// Signal per-cell completion so the UI marks the task as done.
		if err := cfg.sse.writeProgress(progressEvent{
			ID:         cp.ID,
			ObjectType: "task",
			Name:       r.def.Name,
			Format:     "code",
			OutputType: "completion",
			Event:      "done",
			Data:       "",
			Origin:     cp.Origin,
		}); err != nil {
			cancelEval()
			return
		}

	}

	// Resolve parent span context from the request (links traces to the playground)
	var spanParent bttrace.Parent
	var generation any
	if req.Parent != nil && req.Parent.ObjectID != "" {
		// Always use "playground_id" as the parent type, matching Ruby/Java behavior.
		// The request sends object_type "playground_logs" but the span parent must
		// be "playground_id" for the UI to find the spans.
		spanParent = bttrace.NewParent("playground_id", req.Parent.ObjectID)
		// Extract generation from propagated_event.span_attributes.generation
		if len(req.Parent.PropagatedEvent) > 0 {
			var pe struct {
				SpanAttributes struct {
					Generation any `json:"generation"`
				} `json:"span_attributes"`
			}
			if json.Unmarshal(req.Parent.PropagatedEvent, &pe) == nil {
				generation = pe.SpanAttributes.Generation
			}
		}
	}

	evaluator := eval.NewEvaluator[I, R](cfg.auth.session, tp, apiClient, r.projectName())
	result, evalErr := evaluator.RunEval(evalCtx, r.def, eval.RunOpts[I, R]{
		Experiment:     experimentName,
		Dataset:        dataset,
		ProjectName:    r.projectName(),
		Update:         true,
		Quiet:          true,
		OnCaseComplete: onComplete,
		SpanParent:     spanParent,
		Generation:     generation,
	})

	// Flush traces before sending summary so the UI can poll for scores immediately.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer flushCancel()
	_ = tp.ForceFlush(flushCtx)

	// Build average scores for summary
	avgScores := make(map[string]float64, len(scoreSums))
	scoresMu.Lock()
	for name, sum := range scoreSums {
		if count := scoreCounts[name]; count > 0 {
			avgScores[name] = sum / float64(count)
		}
	}
	scoresMu.Unlock()

	// Send summary event
	summary := summaryEvent{
		Scores: avgScores,
	}
	if result != nil {
		summary.ExperimentID = result.ID()
		summary.ExperimentName = result.Name()
		summary.ProjectName = result.ProjectName()
		summary.ProjectID = result.ProjectID()
		if permalink, err := result.Permalink(); err == nil && permalink != "" {
			summary.ExperimentURL = permalink
		}
	}
	if err := cfg.sse.writeSummary(summary); err != nil {
		return fmt.Errorf("failed to write summary: %w", err)
	}

	return evalErr
}

// resolveDataset resolves the request data into a typed Dataset.
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
	if sourceCount != 1 {
		return nil, fmt.Errorf("exactly one of data, dataset_id, or dataset_name must be specified")
	}

	// Inline data
	if len(data.Data) > 0 {
		return r.parseInlineData(data.Data)
	}

	// Dataset by ID or name requires an API client
	if cfg.auth == nil {
		return nil, fmt.Errorf("dataset resolution requires authentication")
	}

	// Use the authenticated API client via evaluator's Datasets()
	evaluator := eval.NewEvaluator[I, R](cfg.auth.session, nil, cfg.auth.api, r.projectName())
	dsAPI := evaluator.Datasets()

	if data.DatasetID != "" {
		return dsAPI.Get(ctx, data.DatasetID)
	}

	return dsAPI.Query(ctx, eval.DatasetQueryOpts{
		Name: data.DatasetName,
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
