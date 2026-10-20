package eval

import (
	"context"
	"errors"
	"maps"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
)

func (w *WorkflowEval[I, R]) logRun(ctx context.Context, run *workflowRun[I, R], stages []workflowStage[I, R]) error {
	if w.evaluator == nil || w.evaluator.api == nil {
		return errors.New("resuming a logged workflow requires a Braintrust evaluator")
	}
	// Upload only changed rows. Bound request size, including when a large
	// batch completes at once.
	events := make([]experiments.Event, 0, 100)
	logged := make([]int, 0, 100)
	flush := func() error {
		if len(events) == 0 {
			return nil
		}
		if err := w.evaluator.api.Experiments().InsertEvents(ctx, run.ExperimentID, events); err != nil {
			return err
		}
		for _, i := range logged {
			c := &run.Cases[i]
			c.LogPending = false
			if err := w.saveCase(ctx, run.ID, c); err != nil {
				// The row stays pending in the store; re-uploading merges harmlessly.
				return err
			}
		}
		events, logged = events[:0], logged[:0]
		return nil
	}
	merge := true
	for i := range run.Cases {
		c := &run.Cases[i]
		if !c.LogPending {
			continue
		}
		traceID, spanID := workflowTraceIDs(c.Item.ID)
		metadata := maps.Clone(c.Item.Metadata)
		if metadata == nil {
			metadata = Metadata{}
		}
		workflow := map[string]any{"run_id": run.ID, "case_id": c.Item.CaseID, "trial_index": c.Item.TrialIndex}
		if c.BatchID != "" {
			workflow["batch_submission_id"] = c.BatchID
		}
		metadata["workflow_eval"] = workflow
		if len(c.Errors) > 0 {
			metadata["scorer_errors"] = c.Errors
		}
		if len(c.ClassifierErrors) > 0 {
			metadata["classifier_errors"] = c.ClassifierErrors
		}
		metrics := map[string]float64{"start": float64(c.StartedAt.UnixNano()) / 1e9}
		if !c.FinishedAt.IsZero() {
			metrics["end"] = float64(c.FinishedAt.UnixNano()) / 1e9
		}
		event := experiments.Event{ID: c.Item.ID, IsMerge: &merge, SpanID: spanID.String(), RootSpanID: traceID.String(), SpanAttributes: map[string]any{"name": "eval", "type": "eval"}, Input: c.Item.Input, Expected: c.Item.Expected, Metadata: metadata, Tags: c.Item.Tags, Metrics: metrics, Scores: w.caseScores(c, stages), Classifications: c.Classifications}
		if c.DatasetID != "" {
			event.DatasetRecordID = c.DatasetRecordID
			event.DatasetID = c.DatasetID
		}
		if c.Done["task"] {
			if c.TaskError != "" {
				event.Error = c.TaskError
			} else {
				event.Output = c.Output
			}
		}
		events = append(events, event)
		logged = append(logged, i)
		if len(events) >= 100 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (w *WorkflowEval[I, R]) callbackSpan(ctx context.Context, run *workflowRun[I, R], c *workflowCase[I, R], name, kind string) (context.Context, oteltrace.Span) {
	ctx = w.caseContext(ctx, run, c)
	if w.evaluator == nil || w.evaluator.tracerProvider == nil {
		return ctx, oteltrace.SpanFromContext(ctx)
	}
	ctx, span := w.evaluator.tracerProvider.Tracer("braintrust.eval.workflow").Start(ctx, name)
	// These strings are owned by the definition, so encoding cannot fail.
	_ = setJSONAttr(span, "braintrust.span_attributes", map[string]string{"name": name, "type": kind})
	return ctx, span
}
