package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestSpanFetcher_Thread_ReturnsThreadFromPreprocessorInvoke(t *testing.T) {
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

	fetcher := newSpanFetcher(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })

	ctx := context.Background()
	thread, err := fetcher.Thread(ctx)
	require.NoError(t, err)
	require.Len(t, thread, 2)
	assert.Equal(t, "system", thread[0]["role"])
	assert.Equal(t, "user", thread[1]["role"])
}

func TestSpanFetcher_Thread_ReturnsNilForNonArrayOutput(t *testing.T) {
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

	fetcher := newSpanFetcher(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })

	ctx := context.Background()
	thread, err := fetcher.Thread(ctx)
	require.NoError(t, err)
	assert.Nil(t, thread)
}

func TestSpanFetcher_Thread_ReturnsNilForNullOutput(t *testing.T) {
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

	fetcher := newSpanFetcher(apiClient, "experiment", "obj-123", "root-456", func() error { return nil })

	ctx := context.Background()
	thread, err := fetcher.Thread(ctx)
	require.NoError(t, err)
	assert.Nil(t, thread)
}
