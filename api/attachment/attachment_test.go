package attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestRequestUploadURL(t *testing.T) {
	var req map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/attachment", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer api-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"signedUrl": "https://object-store/upload",
			"headers":   map[string]string{"X-Test": "yes"},
		})
	}))
	defer server.Close()

	api := New(https.NewClient("api-key", server.URL, logger.Discard()))
	resp, err := api.RequestUploadURL(context.Background(), UploadURLParams{
		Key:         "key-1",
		Filename:    "input.json",
		ContentType: "application/json",
		OrgID:       "org-id",
	})
	require.NoError(t, err)
	require.Equal(t, "https://object-store/upload", resp.SignedURL)
	require.Equal(t, "yes", resp.Headers["X-Test"])
	require.Equal(t, "key-1", req["key"])
	require.Equal(t, "input.json", req["filename"])
	require.Equal(t, "application/json", req["content_type"])
	require.Equal(t, "org-id", req["org_id"])
}

func TestUpdateStatus(t *testing.T) {
	var req map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/attachment/status", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer api-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	api := New(https.NewClient("api-key", server.URL, logger.Discard()))
	err := api.UpdateStatus(context.Background(), StatusParams{
		Key:   "key-1",
		OrgID: "org-id",
		Status: map[string]any{
			"upload_status": "done",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "key-1", req["key"])
	require.Equal(t, "org-id", req["org_id"])
	status, ok := req["status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "done", status["upload_status"])
}
