package eval

import (
	"context"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/objects"
)

// JSONObject represents a JSON object for trace payloads.
type JSONObject = map[string]any

// Trace provides access to trace data for scorers.
type Trace interface {
	// GetSpans returns spans for the provided span types.
	GetSpans(spanTypes []string) []JSONObject
	// GetThread returns thread entries associated with the case.
	GetThread() []JSONObject
}

type noopTrace struct{}

func newTrace() Trace {
	return noopTrace{}
}

func (t noopTrace) GetSpans(spanTypes []string) []JSONObject {
	return []JSONObject{}
}

func (t noopTrace) GetThread() []JSONObject {
	return []JSONObject{}
}

type traceImpl struct {
	objectType string
	objectID   string
	rootSpanID string

	apiClient          *api.API
	ensureSpansFlushed func() error

	flushOnce sync.Once
	flushErr  error
}

func newEvalTrace(
	apiClient *api.API,
	objectType string,
	objectID string,
	rootSpanID string,
	ensureSpansFlushed func() error,
) Trace {
	return &traceImpl{
		objectType:         objectType,
		objectID:           objectID,
		rootSpanID:         rootSpanID,
		apiClient:          apiClient,
		ensureSpansFlushed: ensureSpansFlushed,
	}
}

func (t *traceImpl) GetSpans(spanTypes []string) []JSONObject {
	if t.objectType == "" || t.objectID == "" || t.rootSpanID == "" || t.apiClient == nil {
		return []JSONObject{}
	}

	if err := t.ensureSpansReady(); err != nil {
		return []JSONObject{}
	}

	spans, err := t.fetchSpans(spanTypes)
	if err != nil {
		return []JSONObject{}
	}
	return spans
}

func (t *traceImpl) GetThread() []JSONObject {
	if t.objectType == "" || t.objectID == "" || t.rootSpanID == "" || t.apiClient == nil {
		return []JSONObject{}
	}

	if err := t.ensureSpansReady(); err != nil {
		return []JSONObject{}
	}

	thread, err := t.fetchThread()
	if err != nil {
		return []JSONObject{}
	}
	return thread
}

func (t *traceImpl) ensureSpansReady() error {
	t.flushOnce.Do(func() {
		if t.ensureSpansFlushed == nil {
			return
		}
		t.flushErr = t.ensureSpansFlushed()
	})
	return t.flushErr
}

func (t *traceImpl) fetchSpans(spanTypes []string) ([]JSONObject, error) {
	var all []JSONObject
	cursor := ""

	for {
		req := objects.FetchParams{
			Limit:  1000,
			Filter: buildSpanFilter(t.rootSpanID, spanTypes),
		}
		if cursor != "" {
			req.Cursor = cursor
		}

		payload, err := t.apiClient.Objects().Fetch(context.Background(), t.objectType, t.objectID, req)
		if err != nil {
			return nil, err
		}

		rows := payload.Events
		if len(rows) == 0 {
			rows = payload.Rows
		}
		if len(rows) == 0 {
			rows = payload.Objects
		}

		for _, row := range rows {
			if isScorerPurpose(row) {
				continue
			}
			all = append(all, projectSpanRow(row))
		}

		if payload.Cursor == "" {
			break
		}
		cursor = payload.Cursor
	}

	return all, nil
}

func (t *traceImpl) fetchThread() ([]JSONObject, error) {
	payload, err := t.apiClient.Functions().InvokeGlobal(context.Background(), functionsapi.InvokeGlobalParams{
		GlobalFunction: "project_default",
		FunctionType:   "preprocessor",
		Mode:           "json",
		Input: map[string]any{
			"trace_ref": map[string]any{
				"object_type":  t.objectType,
				"object_id":    t.objectID,
				"root_span_id": t.rootSpanID,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	values, ok := payload.([]any)
	if !ok {
		return []JSONObject{}, nil
	}

	thread := make([]JSONObject, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			thread = append(thread, item)
		}
	}
	return thread, nil
}

func buildSpanFilter(rootSpanID string, spanTypeFilter []string) JSONObject {
	children := []JSONObject{
		{
			"op": "eq",
			"left": JSONObject{
				"op":   "ident",
				"name": []string{"root_span_id"},
			},
			"right": JSONObject{
				"op":    "literal",
				"value": rootSpanID,
			},
		},
		{
			"op": "or",
			"children": []JSONObject{
				{
					"op": "isnull",
					"expr": JSONObject{
						"op":   "ident",
						"name": []string{"span_attributes", "purpose"},
					},
				},
				{
					"op": "ne",
					"left": JSONObject{
						"op":   "ident",
						"name": []string{"span_attributes", "purpose"},
					},
					"right": JSONObject{
						"op":    "literal",
						"value": "scorer",
					},
				},
			},
		},
	}

	if len(spanTypeFilter) > 0 {
		children = append(children, JSONObject{
			"op": "in",
			"left": JSONObject{
				"op":   "ident",
				"name": []string{"span_attributes", "type"},
			},
			"right": JSONObject{
				"op":    "literal",
				"value": spanTypeFilter,
			},
		})
	}

	return JSONObject{
		"op":       "and",
		"children": children,
	}
}

func isScorerPurpose(row JSONObject) bool {
	attrs, ok := row["span_attributes"].(map[string]any)
	if !ok || attrs == nil {
		return false
	}
	purpose, ok := attrs["purpose"].(string)
	return ok && purpose == "scorer"
}

func projectSpanRow(row JSONObject) JSONObject {
	out := JSONObject{}
	for _, key := range []string{
		"input",
		"output",
		"metadata",
		"span_id",
		"span_parents",
		"span_attributes",
		"id",
		"_xact_id",
		"_pagination_key",
		"root_span_id",
	} {
		if value, ok := row[key]; ok {
			out[key] = value
		}
	}
	return out
}
