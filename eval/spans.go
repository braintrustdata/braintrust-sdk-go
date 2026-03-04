package eval

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/objects"
)

// Span represents a single span from a Braintrust trace.
type Span struct {
	ID             string         `json:"id"`
	SpanID         string         `json:"span_id"`
	RootSpanID     string         `json:"root_span_id"`
	SpanParents    []string       `json:"span_parents"`
	SpanAttributes map[string]any `json:"span_attributes"`
	Input          any            `json:"input"`
	Output         any            `json:"output"`
	Metadata       map[string]any `json:"metadata"`
}

// flushState wraps sync.Once so that spanFetcher (and therefore TaskResult)
// remains safe to copy by value.
type flushState struct {
	once sync.Once
	err  error
}

// spanFetcher retrieves span and thread data from the Braintrust API.
// It is unexported and attached to TaskResult as a pointer so that
// TaskResult stays copyable by value.
type spanFetcher struct {
	apiClient  *api.API
	objectType string
	objectID   string
	rootSpanID string
	flush      *flushState
	flushFn    func() error
}

func newSpanFetcher(
	apiClient *api.API,
	objectType string,
	objectID string,
	rootSpanID string,
	ensureSpansFlushed func() error,
) *spanFetcher {
	return &spanFetcher{
		apiClient:  apiClient,
		objectType: objectType,
		objectID:   objectID,
		rootSpanID: rootSpanID,
		flush:      &flushState{},
		flushFn:    ensureSpansFlushed,
	}
}

// Spans returns spans for the provided span types.
func (f *spanFetcher) Spans(ctx context.Context, spanTypes []string) ([]Span, error) {
	if f.objectType == "" || f.objectID == "" || f.rootSpanID == "" || f.apiClient == nil {
		return nil, nil
	}

	if err := f.ensureSpansReady(); err != nil {
		return nil, err
	}

	return f.fetchSpans(ctx, spanTypes)
}

// Thread returns thread entries associated with the case.
func (f *spanFetcher) Thread(ctx context.Context) ([]map[string]any, error) {
	if f.objectType == "" || f.objectID == "" || f.rootSpanID == "" || f.apiClient == nil {
		return nil, nil
	}

	if err := f.ensureSpansReady(); err != nil {
		return nil, err
	}

	return f.fetchThread(ctx)
}

func (f *spanFetcher) ensureSpansReady() error {
	f.flush.once.Do(func() {
		if f.flushFn == nil {
			return
		}
		f.flush.err = f.flushFn()
	})
	return f.flush.err
}

func (f *spanFetcher) fetchSpans(ctx context.Context, spanTypes []string) ([]Span, error) {
	var all []Span
	cursor := ""

	for {
		req := objects.FetchParams{
			Limit:  1000,
			Filter: buildSpanFilter(f.rootSpanID, spanTypes),
		}
		if cursor != "" {
			req.Cursor = cursor
		}

		payload, err := f.apiClient.Objects().Fetch(ctx, f.objectType, f.objectID, req)
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
			span, err := rowToSpan(row)
			if err != nil {
				return nil, err
			}
			all = append(all, span)
		}

		if payload.Cursor == "" {
			break
		}
		cursor = payload.Cursor
	}

	return all, nil
}

func (f *spanFetcher) fetchThread(ctx context.Context) ([]map[string]any, error) {
	payload, err := f.apiClient.Functions().InvokeGlobal(ctx, functionsapi.InvokeGlobalParams{
		GlobalFunction: "project_default",
		FunctionType:   "preprocessor",
		Mode:           "json",
		Input: map[string]any{
			"trace_ref": map[string]any{
				"object_type":  f.objectType,
				"object_id":    f.objectID,
				"root_span_id": f.rootSpanID,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	values, ok := payload.([]any)
	if !ok {
		return nil, nil
	}

	thread := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			thread = append(thread, item)
		}
	}
	return thread, nil
}

// rowToSpan converts a raw API row into a typed Span via JSON round-trip.
func rowToSpan(row map[string]any) (Span, error) {
	b, err := json.Marshal(row)
	if err != nil {
		return Span{}, err
	}
	var s Span
	if err := json.Unmarshal(b, &s); err != nil {
		return Span{}, err
	}
	return s, nil
}

func buildSpanFilter(rootSpanID string, spanTypeFilter []string) map[string]any {
	children := []map[string]any{
		{
			"op": "eq",
			"left": map[string]any{
				"op":   "ident",
				"name": []string{"root_span_id"},
			},
			"right": map[string]any{
				"op":    "literal",
				"value": rootSpanID,
			},
		},
		{
			"op": "or",
			"children": []map[string]any{
				{
					"op": "isnull",
					"expr": map[string]any{
						"op":   "ident",
						"name": []string{"span_attributes", "purpose"},
					},
				},
				{
					"op": "ne",
					"left": map[string]any{
						"op":   "ident",
						"name": []string{"span_attributes", "purpose"},
					},
					"right": map[string]any{
						"op":    "literal",
						"value": "scorer",
					},
				},
			},
		},
	}

	if len(spanTypeFilter) > 0 {
		children = append(children, map[string]any{
			"op": "in",
			"left": map[string]any{
				"op":   "ident",
				"name": []string{"span_attributes", "type"},
			},
			"right": map[string]any{
				"op":    "literal",
				"value": spanTypeFilter,
			},
		})
	}

	return map[string]any{
		"op":       "and",
		"children": children,
	}
}

func isScorerPurpose(row map[string]any) bool {
	attrs, ok := row["span_attributes"].(map[string]any)
	if !ok || attrs == nil {
		return false
	}
	purpose, ok := attrs["purpose"].(string)
	return ok && purpose == "scorer"
}
