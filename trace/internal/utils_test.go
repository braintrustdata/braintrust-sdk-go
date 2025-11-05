package internal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func TestBufferedReader(t *testing.T) {
	content := "test content"
	src := io.NopCloser(strings.NewReader(content))

	var capturedContent string
	onDone := func(r io.Reader) {
		data, _ := io.ReadAll(r)
		capturedContent = string(data)
	}

	reader := NewBufferedReader(src, onDone)

	// Read the content
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))

	// Close to trigger onDone
	err = reader.Close()
	require.NoError(t, err)

	// Verify onDone was called with the correct content
	assert.Equal(t, content, capturedContent)
}

func TestSetJSONAttr(t *testing.T) {
	// Create a test tracer and span
	tracer := otel.GetTracerProvider().Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// Test data
	testData := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	}

	// Set the attribute
	err := SetJSONAttr(span, "test.data", testData)
	require.NoError(t, err)
}

// Mock tracer for testing
type mockTracer struct {
	startSpanCalled bool
	tagSpanCalled   bool
}

func (m *mockTracer) StartSpan(ctx context.Context, start time.Time, request io.Reader) (context.Context, trace.Span, error) {
	m.startSpanCalled = true
	tracer := otel.GetTracerProvider().Tracer("braintrust")
	newCtx, span := tracer.Start(ctx, "mock-span", trace.WithTimestamp(start))
	return newCtx, span, nil
}

func (m *mockTracer) TagSpan(span trace.Span, response io.Reader) error {
	m.tagSpanCalled = true
	return nil
}

func TestMiddleware(t *testing.T) {
	// Create mock tracers for different paths
	mockTracer1 := &mockTracer{}
	mockTracer2 := &mockTracer{}

	// Create router
	router := func(path string) MiddlewareTracer {
		switch path {
		case "/v1/test1":
			return mockTracer1
		case "/v1/test2":
			return mockTracer2
		default:
			return nil
		}
	}

	// Create middleware
	middleware := Middleware(router, nil) //nolint:bodyclose // false positive - responses are properly closed in tests

	// Test request to /v1/test1
	req := httptest.NewRequest("POST", "/v1/test1", strings.NewReader(`{"test": "data"}`))
	req.Header.Set("Content-Type", "application/json")

	// Mock next middleware
	next := func(req *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"response": "data"}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	}

	// Call middleware
	resp, err := middleware(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Verify mock tracer was called
	assert.True(t, mockTracer1.startSpanCalled)

	// Read response body to trigger TagSpan
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "response")

	// Close response body and check error
	closeErr := resp.Body.Close()
	require.NoError(t, closeErr)

	// Verify TagSpan was called
	assert.True(t, mockTracer1.tagSpanCalled)
}

func TestMiddlewareWithUnsupportedOpenAIEndpoint(t *testing.T) {
	// Create router that mimics traceopenai - supports some endpoints, not others
	router := func(path string) MiddlewareTracer {
		switch path {
		case "/v1/chat/completions", "/v1/responses":
			// These would be supported by traceopenai
			return &mockTracer{}
		default:
			// Real OpenAI endpoints not supported by traceopenai use noop tracer
			return nil
		}
	}

	// Create middleware
	middleware := Middleware(router, nil) //nolint:bodyclose // false positive - responses are properly closed in tests

	// Test request to real OpenAI embeddings endpoint (not supported by traceopenai)
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{
		"model": "text-embedding-3-small",
		"input": "The quick brown fox jumps over the lazy dog"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-key")

	// Mock next middleware with realistic embeddings response
	next := func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"object": "list",
				"data": [
					{
						"object": "embedding",
						"index": 0,
						"embedding": [0.1, 0.2, 0.3, 0.4, 0.5]
					}
				],
				"model": "text-embedding-3-small",
				"usage": {
					"prompt_tokens": 10,
					"total_tokens": 10
				}
			}`)),
		}, nil
	}

	// Call middleware - should work with noop tracer
	resp, err := middleware(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Verify response body
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "text-embedding-3-small")
	assert.Contains(t, string(body), "prompt_tokens")

	// Close the response body and check error
	closeErr := resp.Body.Close()
	require.NoError(t, closeErr)
}

func TestMiddlewareWithNilBody(t *testing.T) {
	// This test reproduces the panic that occurs when middleware encounters
	// a traced endpoint with a nil body (like GET requests)

	mockTracer := &mockTracer{}

	router := func(path string) MiddlewareTracer {
		return mockTracer
	}

	middleware := Middleware(router, nil) //nolint:bodyclose // false positive - response bodies are properly closed in tests

	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	req.Body = nil

	next := func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data": []}`)),
		}, nil
	}

	resp, err := middleware(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 200, resp.StatusCode)

	err = resp.Body.Close()
	require.NoError(t, err)
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected int64
		success  bool
	}{
		{"float64", float64(123.45), int64(123), true},
		{"int64", int64(42), int64(42), true},
		{"int", int(100), int64(100), true},
		{"string", "not a number", int64(0), false},
		{"nil", nil, int64(0), false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			success, result := ToInt64(test.input)
			assert.Equal(t, test.success, success)
			if test.success {
				assert.Equal(t, test.expected, result)
			}
		})
	}
}
