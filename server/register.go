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
	req    *EvalRequest
	auth   *authResult
	sse    *sseWriter
	noAuth bool
}

// RegisterOpts configures a registered evaluator.
type RegisterOpts struct {
	// Parameters defines the parameter schema shown in the Braintrust UI.
	Parameters *Parameters

	// ProjectName is the default project for this evaluator.
	ProjectName string
}

// Register adds an evaluator to the server. The type parameters I and R are
// the input and result types of the evaluation. Go does not allow generic
// methods on non-generic types, so this is a package-level function.
//
// Example:
//
//	server.Register(srv, "classify",
//	    eval.T(classifyTask),
//	    []eval.Scorer[string, string]{scorer},
//	)
func Register[I, R any](s *Server, name string, task eval.TaskFunc[I, R], scorers []eval.Scorer[I, R], opts ...RegisterOpts) {
	var opt RegisterOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	impl := &registeredEvalImpl[I, R]{
		name:    name,
		task:    task,
		scorers: scorers,
		opts:    opt,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evaluators[name] = impl
}

// registeredEvalImpl implements registeredEval by wrapping generic eval types.
type registeredEvalImpl[I, R any] struct {
	name    string
	task    eval.TaskFunc[I, R]
	scorers []eval.Scorer[I, R]
	opts    RegisterOpts
}

func (r *registeredEvalImpl[I, R]) scorerNames() []string {
	names := make([]string, len(r.scorers))
	for i, s := range r.scorers {
		names[i] = s.Name()
	}
	return names
}

func (r *registeredEvalImpl[I, R]) parameters() *Parameters {
	return r.opts.Parameters
}

func (r *registeredEvalImpl[I, R]) projectName() string {
	return r.opts.ProjectName
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
		experimentName = r.name
	}

	// Build per-request tracing with a dedicated shutdown timeout
	tp := sdktrace.NewTracerProvider()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutdownCtx)
	}()

	session := cfg.auth.session
	apiClient := cfg.auth.api

	// Add Braintrust span processor for this request
	traceCfg := bttrace.Config{
		DefaultProjectName: r.projectName(),
	}
	if err := bttrace.AddSpanProcessor(tp, session, traceCfg); err != nil {
		return fmt.Errorf("failed to setup tracing: %w", err)
	}

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

		// Stream progress event; cancel eval if write fails (client disconnected)
		if err := cfg.sse.writeProgress(progressEvent{
			ObjectType: "task",
			Name:       r.name,
			Format:     "code",
			OutputType: "completion",
			Event:      "json_delta",
			Data:       cp.Output,
		}); err != nil {
			cancelEval()
		}
	}

	// Create evaluator and run
	evaluator := eval.NewEvaluator[I, R](session, tp, apiClient, r.projectName())
	result, evalErr := evaluator.Run(evalCtx, eval.Opts[I, R]{
		Experiment:     experimentName,
		Dataset:        dataset,
		Task:           r.task,
		Scorers:        r.scorers,
		ProjectName:    r.projectName(),
		Update:         true,
		Quiet:          true,
		OnCaseComplete: onComplete,
	})

	// Flush traces before sending summary
	_ = tp.ForceFlush(ctx)

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
