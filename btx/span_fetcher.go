package btx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	btqlRetryInterval = 30 * time.Second
	btqlMaxWait       = 600 * time.Second
)

// spanFromOTel converts an in-memory OTel span (as captured by oteltest.Exporter)
// into the brainstore span format expected by the spec validator.
//
// The input is a tracetest.SpanStub (wrapped in oteltest.Span). We extract the
// braintrust.* attributes and build a map matching the brainstore schema.
func spanFromOTel(spanName string, attrs map[string]string) map[string]any {
	result := make(map[string]any)

	// Parse JSON attributes.
	jsonFields := map[string]string{
		"braintrust.input_json":      "input",
		"braintrust.output_json":     "output",
		"braintrust.metadata":        "metadata",
		"braintrust.metrics":         "metrics",
		"braintrust.span_attributes": "span_attributes",
		"braintrust.tags":            "tags",
	}

	for attrKey, fieldName := range jsonFields {
		if raw, ok := attrs[attrKey]; ok {
			var val any
			if err := json.Unmarshal([]byte(raw), &val); err == nil {
				result[fieldName] = val
			}
		}
	}

	// Inject the OTel span name into span_attributes.name, and default
	// type to "llm" when not already set by the middleware.
	sa, ok := result["span_attributes"].(map[string]any)
	if !ok {
		sa = make(map[string]any)
		result["span_attributes"] = sa
	}
	sa["name"] = spanName
	if _, ok := sa["type"]; !ok {
		sa["type"] = "llm"
	}

	return result
}

// resolveProjectID looks up a project by name and returns its ID.
func resolveProjectID(projectName string) (string, error) {
	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("BRAINTRUST_API_KEY not set")
	}
	apiURL := os.Getenv("BRAINTRUST_API_URL")
	if apiURL == "" {
		apiURL = "https://api.braintrust.dev"
	}

	req, err := http.NewRequest(http.MethodGet, apiURL+"/v1/project?project_name="+projectName, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching project: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("project lookup failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Objects []struct {
			ID string `json:"id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing project response: %w", err)
	}
	if len(result.Objects) == 0 {
		return "", fmt.Errorf("project %q not found", projectName)
	}
	return result.Objects[0].ID, nil
}

// fetchSpansBTQL fetches spans from the Braintrust API via BTQL.
// It retries until the expected number of spans are available.
func fetchSpansBTQL(rootSpanID, projectID string, numExpected int) ([]map[string]any, error) {
	apiKey := os.Getenv("BRAINTRUST_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("BRAINTRUST_API_KEY not set (required for live mode)")
	}
	apiURL := os.Getenv("BRAINTRUST_API_URL")
	if apiURL == "" {
		apiURL = "https://api.braintrust.dev"
	}

	query := buildBTQLQuery(rootSpanID, projectID)

	var totalWait time.Duration
	for totalWait < btqlMaxWait {
		spans, err := executeBTQL(apiURL, apiKey, query)
		if err != nil {
			return nil, err
		}

		// Filter out scorer spans.
		filtered := filterScorerSpans(spans)

		// Check if we have enough spans.
		if len(filtered) > numExpected {
			return nil, fmt.Errorf("too many spans: expected %d, got %d", numExpected, len(filtered))
		}

		if len(filtered) == numExpected && allSpansReady(filtered) {
			return filtered, nil
		}

		fmt.Printf("btx: waiting for spans (%d/%d ready), retrying in %v...\n",
			len(filtered), numExpected, btqlRetryInterval)
		time.Sleep(btqlRetryInterval)
		totalWait += btqlRetryInterval
	}

	return nil, fmt.Errorf("timed out waiting for %d spans after %v", numExpected, btqlMaxWait)
}

// buildBTQLQuery constructs the BTQL query JSON.
func buildBTQLQuery(rootSpanID, projectID string) map[string]any {
	return map[string]any{
		"query": map[string]any{
			"select": []any{map[string]any{"op": "star"}},
			"from": map[string]any{
				"op":   "function",
				"name": map[string]any{"op": "ident", "name": []any{"project_logs"}},
				"args": []any{map[string]any{"op": "literal", "value": projectID}},
			},
			"filter": map[string]any{
				"op": "and",
				"left": map[string]any{
					"op":    "eq",
					"left":  map[string]any{"op": "ident", "name": []any{"root_span_id"}},
					"right": map[string]any{"op": "literal", "value": rootSpanID},
				},
				"right": map[string]any{
					"op":    "ne",
					"left":  map[string]any{"op": "ident", "name": []any{"span_parents"}},
					"right": map[string]any{"op": "literal", "value": nil},
				},
			},
			"sort":  []any{map[string]any{"expr": map[string]any{"op": "ident", "name": []any{"created"}}, "dir": "asc"}},
			"limit": 1000,
		},
		"use_columnstore":     true,
		"use_brainstore":      true,
		"brainstore_realtime": true,
	}
}

// executeBTQL sends a BTQL query and returns the result rows.
func executeBTQL(apiURL, apiKey string, query map[string]any) ([]map[string]any, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("marshaling BTQL query: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL+"/btql", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating BTQL request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing BTQL query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading BTQL response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BTQL query failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing BTQL response: %w", err)
	}

	return result.Data, nil
}

// filterScorerSpans removes spans where span_attributes.purpose == "scorer".
func filterScorerSpans(spans []map[string]any) []map[string]any {
	var result []map[string]any
	for _, span := range spans {
		sa, ok := span["span_attributes"].(map[string]any)
		if ok && sa["purpose"] == "scorer" {
			continue
		}
		result = append(result, span)
	}
	return result
}

// allSpansReady checks that all spans have output or metrics populated.
func allSpansReady(spans []map[string]any) bool {
	for _, span := range spans {
		if span["output"] == nil && span["metrics"] == nil {
			return false
		}
	}
	return true
}
