package eval

import (
	"context"
	"errors"
	"maps"

	oteltrace "go.opentelemetry.io/otel/trace"
)

func (w *WorkflowEval[I, R]) logRun(ctx context.Context, run *workflowRun[I, R]) error {
	if w.evaluator == nil || w.evaluator.api == nil {
		return errors.New("resuming a logged workflow requires a Braintrust evaluator")
	}
	// Bound request size, including when a large batch completes at once.
	events := make([]map[string]any, 0, 100)
	flush := func() error {
		if len(events) == 0 {
			return nil
		}
		err := w.evaluator.api.Experiments().InsertEvents(ctx, run.ExperimentID, events)
		events = events[:0]
		return err
	}
	for i := range run.Cases {
		c := &run.Cases[i]
		traceID, spanID := workflowTraceIDs(c.Item.ID)
		metadata := maps.Clone(c.Item.Metadata)
		if metadata == nil {
			metadata = Metadata{}
		}
		workflow := map[string]any{"run_id": run.ID, "case_id": c.Item.ID, "trial_index": c.Item.TrialIndex}
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
		metrics := map[string]any{"start": float64(c.StartedAt.UnixNano()) / 1e9}
		if !c.FinishedAt.IsZero() {
			metrics["end"] = float64(c.FinishedAt.UnixNano()) / 1e9
		}
		event := map[string]any{"id": c.Item.ID, "_is_merge": true, "span_id": spanID.String(), "root_span_id": traceID.String(), "span_attributes": map[string]any{"name": "eval", "type": "eval"}, "input": c.Item.Input, "expected": c.Item.Expected, "metadata": metadata, "tags": c.Item.Tags, "metrics": metrics}
		if c.DatasetID != "" {
			event["dataset_record_id"] = c.DatasetRecordID
			event["dataset_id"] = c.DatasetID
		}
		if c.Done["task"] {
			if c.TaskError != "" {
				event["error"] = c.TaskError
			} else {
				event["output"] = c.Output
			}
		}
		scores := w.caseScores(c)
		if len(scores) > 0 {
			event["scores"] = scores
		}
		if len(c.Classifications) > 0 {
			event["classifications"] = c.Classifications
		}
		events = append(events, event)
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
