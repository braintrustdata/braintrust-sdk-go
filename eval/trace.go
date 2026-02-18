package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
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

	session            *auth.Session
	ensureSpansFlushed func() error

	flushOnce sync.Once
	flushErr  error
}

func newEvalTrace(
	session *auth.Session,
	objectType string,
	objectID string,
	rootSpanID string,
	ensureSpansFlushed func() error,
) Trace {
	return &traceImpl{
		objectType:         objectType,
		objectID:           objectID,
		rootSpanID:         rootSpanID,
		session:            session,
		ensureSpansFlushed: ensureSpansFlushed,
	}
}

func (t *traceImpl) GetSpans(spanTypes []string) []JSONObject {
	if t.objectType == "" || t.objectID == "" || t.rootSpanID == "" || t.session == nil {
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
	if t.objectType == "" || t.objectID == "" || t.rootSpanID == "" || t.session == nil {
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
	loginCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = t.session.Login(loginCtx)

	apiInfo := t.session.APIInfo()
	client := https.NewClient(apiInfo.APIKey, apiInfo.APIURL, logger.Discard())

	var all []JSONObject
	cursor := ""

	for {
		reqBody := map[string]any{
			"limit":  1000,
			"filter": buildSpanFilter(t.rootSpanID, spanTypes),
		}
		if cursor != "" {
			reqBody["cursor"] = cursor
		}

		resp, err := client.POST(context.Background(), fmt.Sprintf("/v1/%s/%s/fetch", t.objectType, t.objectID), reqBody)
		if err != nil {
			return nil, err
		}

		var payload struct {
			Events  []JSONObject `json:"events"`
			Rows    []JSONObject `json:"rows"`
			Objects []JSONObject `json:"objects"`
			Cursor  string       `json:"cursor"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
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
	loginCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = t.session.Login(loginCtx)

	apiInfo := t.session.APIInfo()
	client := https.NewClient(apiInfo.APIKey, apiInfo.APIURL, logger.Discard())

	reqBody := map[string]any{
		"global_function": "project_default",
		"function_type":   "preprocessor",
		"mode":            "json",
		"input": map[string]any{
			"trace_ref": map[string]any{
				"object_type":  t.objectType,
				"object_id":    t.objectID,
				"root_span_id": t.rootSpanID,
			},
		},
	}

	resp, err := client.POST(context.Background(), "/v1/function/invoke", reqBody)
	if err != nil {
		return nil, err
	}

	var payload any
	err = json.NewDecoder(resp.Body).Decode(&payload)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	// The invoke response may be either {"output": ...} or a raw JSON value.
	if outputWrapper, ok := payload.(map[string]any); ok {
		if output, hasOutput := outputWrapper["output"]; hasOutput {
			payload = output
		}
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
