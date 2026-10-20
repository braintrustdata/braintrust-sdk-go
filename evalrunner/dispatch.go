package evalrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// runEval handles one playground request: the eval to run, its data and its
// parameter values all arrive in BT_EVAL_DEV_REQUEST_JSON.
//
// Errors that the playground should see are reported as `error` frames rather
// than returned, because a returned error would exit non-zero and make bt
// append a second, spurious error frame (bt/src/eval.rs:1691).
func (r *Runner) runEval(ctx context.Context) error {
	sink := dialSink(r.env, r.logger)
	// bt blocks until our end of the socket closes, with no timeout on its
	// side, so this must always run.
	defer func() { _ = sink.Close() }()

	if r.env.RequestJSON == "" {
		return r.reportError(sink, 400, fmt.Errorf("BT_EVAL_DEV_REQUEST_JSON is not set"))
	}

	var req EvalRequest
	if err := json.Unmarshal([]byte(r.env.RequestJSON), &req); err != nil {
		return r.reportError(sink, 400, fmt.Errorf("invalid BT_EVAL_DEV_REQUEST_JSON: %w", err))
	}
	if req.Name == "" {
		return r.reportError(sink, 400, fmt.Errorf("request is missing the eval name"))
	}

	target, ok := r.evaluators[req.Name]
	if !ok {
		return r.reportError(sink, 404, fmt.Errorf("evaluator %q not found", req.Name))
	}

	// Resolved up front so a bad parameter value is reported as a bad request,
	// before anything is created in Braintrust.
	parameters, err := target.resolveParams(req.Parameters)
	if err != nil {
		return r.reportError(sink, 400, err)
	}

	session, err := r.openSession(ctx)
	if err != nil {
		return r.reportError(sink, 401, err)
	}
	defer session.Close()

	if err := target.run(ctx, &evalRunConfig{
		req:            &req,
		session:        session,
		sink:           sink,
		tracerProvider: r.tracerProvider,
		parameters:     parameters,
		noSendLogs:     r.env.NoSendLogs,
	}); err != nil {
		return r.reportError(sink, 500, err)
	}

	return nil
}

// runBatch runs every registered eval against its own dataset. This is
// `bt eval <package dir>` without the dev server.
func (r *Runner) runBatch(ctx context.Context) error {
	sink := dialSink(r.env, r.logger)
	defer func() { _ = sink.Close() }()

	selected := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if matchesFilters(r.env.Filters, name) {
			selected = append(selected, name)
		}
	}

	// bt's --list asks for names only, one per line on stdout.
	if r.env.List {
		for _, name := range selected {
			if _, err := fmt.Fprintln(r.stdout, name); err != nil {
				return err
			}
		}
		return nil
	}

	if len(selected) == 0 {
		r.logger.Warn("no evals matched; nothing to run")
		return nil
	}

	session, err := r.openSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()

	concurrency := r.env.MaxConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(selected) {
		concurrency = len(selected)
	}

	// Workers claim evals only when ready, so terminate-on-failure prevents
	// additional evals from starting once a failure is observed. Evals already
	// running at that point are allowed to finish.
	var mu sync.Mutex
	next := 0
	stopped := false
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if stopped || next >= len(selected) {
					mu.Unlock()
					return
				}
				name := selected[next]
				next++
				mu.Unlock()

				parameters, err := r.evaluators[name].resolveParams(nil)
				if err == nil {
					err = r.evaluators[name].run(ctx, &evalRunConfig{
						req:            &EvalRequest{Name: name},
						session:        session,
						sink:           sink,
						tracerProvider: r.tracerProvider,
						parameters:     parameters,
						noSendLogs:     r.env.NoSendLogs,
					})
				}
				if err != nil {
					mu.Lock()
					r.allPassed = false
					if r.env.TerminateOnFailure {
						stopped = true
					}
					mu.Unlock()

					r.logger.Error("eval failed", "eval", name, "error", err)
					if err := sink.send("error", errorEvent{Message: err.Error(), Status: 500}); err != nil {
						r.logger.Warn("could not report eval failure", "error", err)
					}
				}
			}
		}()
	}
	workers.Wait()

	return nil
}

// openSession logs in to Braintrust, unless bt asked for --no-send-logs: then
// the run is local, needs no credentials, and the session is nil.
func (r *Runner) openSession(ctx context.Context) (*session, error) {
	if r.env.NoSendLogs {
		return nil, nil
	}
	return r.newSession(ctx)
}

// reportError sends an error frame and logs it, without failing the process.
//
// Exiting non-zero here would be actively harmful: bt appends its own error
// frame for any child that exits non-zero without having sent one, so a
// non-zero exit after we already reported the problem produces a second,
// duplicate error in the playground.
func (r *Runner) reportError(s sink, status int, cause error) error {
	r.allPassed = false
	r.logger.Error("eval request failed", "error", cause)

	if err := s.send("error", errorEvent{Message: cause.Error(), Status: status}); err != nil {
		// Nothing is listening, so the error would otherwise vanish entirely.
		return fmt.Errorf("%w (and the failure could not be reported: %w)", cause, err)
	}
	return nil
}
