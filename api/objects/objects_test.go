package objects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestObjects_Fetch_PostsExpectedRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/experiment/exp-123/fetch", r.URL.Path)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, float64(1000), body["limit"])

		filter, ok := body["filter"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "and", filter["op"])

		require.NoError(t, json.NewEncoder(w).Encode(FetchResponse{
			Events: []map[string]any{{"id": "row-1"}},
			Cursor: "next",
		}))
	}))
	defer server.Close()

	api := New(https.NewClient("test-key", server.URL, logger.Discard()))
	resp, err := api.Fetch(context.Background(), "experiment", "exp-123", FetchParams{
		Limit: 1000,
		Filter: map[string]any{
			"op": "and",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, "row-1", resp.Events[0]["id"])
	assert.Equal(t, "next", resp.Cursor)
}

func TestObjects_Fetch_Validation(t *testing.T) {
	t.Parallel()

	api := New(https.NewClient("test-key", "https://example.com", logger.Discard()))

	_, err := api.Fetch(context.Background(), "", "obj-1", FetchParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "object type is required")

	_, err = api.Fetch(context.Background(), "experiment", "", FetchParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "object ID is required")
}
