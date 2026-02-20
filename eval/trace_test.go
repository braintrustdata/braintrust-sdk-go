package eval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestTrace_GetThread_ReturnsThreadFromPreprocessorInvoke(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/function/invoke", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		assert.Equal(t, "project_default", body["global_function"])
		assert.Equal(t, "preprocessor", body["function_type"])
		assert.Equal(t, "json", body["mode"])

		input, ok := body["input"].(map[string]any)
		require.True(t, ok)

		traceRef, ok := input["trace_ref"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "experiment", traceRef["object_type"])
		assert.Equal(t, "obj-123", traceRef["object_id"])
		assert.Equal(t, "root-456", traceRef["root_span_id"])

		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{"role": "system", "content": "hello"},
				{"role": "user", "content": "hi"},
			},
		}))
	}))
	defer server.Close()

	session := auth.NewTestSession(
		"test-key",
		"org-id",
		"org-name",
		server.URL,
		server.URL,
		server.URL,
		logger.Discard(),
	)
	apiClient := session.API()

	trace := newEvalTrace(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })

	thread := trace.GetThread()
	require.Len(t, thread, 2)
	assert.Equal(t, "system", thread[0]["role"])
	assert.Equal(t, "user", thread[1]["role"])
}

func TestTrace_GetThread_ReturnsEmptyForNonArrayOutput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"not": "an array"},
		}))
	}))
	defer server.Close()

	session := auth.NewTestSession(
		"test-key",
		"org-id",
		"org-name",
		server.URL,
		server.URL,
		server.URL,
		logger.Discard(),
	)
	apiClient := session.API()

	trace := newEvalTrace(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })

	assert.Empty(t, trace.GetThread())
}

func TestTrace_GetThread_ReturnsEmptyForNullOutput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("null"))
		require.NoError(t, err)
	}))
	defer server.Close()

	session := auth.NewTestSession(
		"test-key",
		"org-id",
		"org-name",
		server.URL,
		server.URL,
		server.URL,
		logger.Discard(),
	)
	apiClient := session.API()

	trace := newEvalTrace(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })
	assert.Empty(t, trace.GetThread())
}
