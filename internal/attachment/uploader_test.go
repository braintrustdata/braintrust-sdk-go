package attachment

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestAttachmentUploaderUploadsAndReportsDone(t *testing.T) {
	var mu sync.Mutex
	var metadataReq map[string]any
	var statusReq map[string]any
	var putBody string
	var putHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&metadataReq))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{"X-Test": "yes"},
			})
		case "/upload":
			require.Equal(t, http.MethodPut, r.Method)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			mu.Lock()
			putBody = string(body)
			putHeader = r.Header.Get("X-Test")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/attachment/status":
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&statusReq))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := auth.NewTestSession("api-key", "org-id", "org-name", server.URL, server.URL, server.URL, logger.Discard())
	uploader := NewUploader(session, logger.Discard())

	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))
	require.NoError(t, uploader.ForceFlush(context.Background()))

	require.Equal(t, map[string]any{
		"key":          "key-1",
		"filename":     "input.json",
		"content_type": "application/json",
		"org_id":       "org-id",
	}, metadataReq)

	mu.Lock()
	require.Equal(t, `{"hello":"world"}`, putBody)
	require.Equal(t, "yes", putHeader)
	mu.Unlock()

	require.Equal(t, "key-1", statusReq["key"])
	require.Equal(t, "org-id", statusReq["org_id"])
	status, ok := statusReq["status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "done", status["upload_status"])
}

func TestAttachmentUploaderReusesHTTPClient(t *testing.T) {
	var connMu sync.Mutex
	newConnections := 0
	statusCount := 0

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{},
			})
		case "/upload":
			w.WriteHeader(http.StatusOK)
		case "/attachment/status":
			connMu.Lock()
			statusCount++
			connMu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connMu.Lock()
			newConnections++
			connMu.Unlock()
		}
	}
	server.Start()
	defer server.Close()

	session := auth.NewTestSession("api-key", "org-id", "org-name", server.URL, server.URL, server.URL, logger.Discard())
	uploader := NewUploader(session, logger.Discard())

	for _, key := range []string{"key-1", "key-2"} {
		ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: key}
		require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))
	}
	require.NoError(t, uploader.ForceFlush(context.Background()))

	connMu.Lock()
	require.Equal(t, 2, statusCount)
	require.Equal(t, 1, newConnections)
	connMu.Unlock()
}

func TestAddAzureBlobHeaders(t *testing.T) {
	headers := map[string]string{}
	addAzureBlobHeaders(headers, "https://acct.blob.core.windows.net/container/blob?sas=1")
	require.Equal(t, "BlockBlob", headers["x-ms-blob-type"])

	headers = map[string]string{}
	addAzureBlobHeaders(headers, "https://blob.core.windows.net/container/blob?sas=1")
	require.Equal(t, "BlockBlob", headers["x-ms-blob-type"])

	headers = map[string]string{}
	addAzureBlobHeaders(headers, "https://example.com/upload?redirect=blob.core.windows.net")
	require.Empty(t, headers)

	headers = map[string]string{}
	addAzureBlobHeaders(headers, "https://notblob.core.windows.net.example.com/upload")
	require.Empty(t, headers)
}

func TestAttachmentUploaderRecoversFromUploadPanic(t *testing.T) {
	uploader := NewUploader(nil, logger.Discard())

	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, uploader.ForceFlush(ctx))
}

func TestAttachmentUploaderShutdownCancelsBlockingLogin(t *testing.T) {
	unblockLogin := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apikey/login":
			select {
			case <-unblockLogin:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(unblockLogin)

	session, err := auth.NewSession(context.Background(), auth.Options{
		APIKey: "api-key",
		AppURL: server.URL,
		APIURL: server.URL,
		Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer session.Close()

	uploader := NewUploader(session, logger.Discard())
	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, uploader.Shutdown(ctx), context.DeadlineExceeded)
	require.Eventually(t, func() bool {
		select {
		case <-uploader.workerDone:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

func TestAttachmentUploaderShutdownWaitsForWorkerExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{},
			})
		case "/upload":
			w.WriteHeader(http.StatusOK)
		case "/attachment/status":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := auth.NewTestSession("api-key", "org-id", "org-name", server.URL, server.URL, server.URL, logger.Discard())
	uploader := NewUploader(session, logger.Discard())

	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))
	require.NoError(t, uploader.Shutdown(context.Background()))

	select {
	case <-uploader.workerDone:
	default:
		t.Fatal("Shutdown returned before attachment uploader worker exited")
	}
}

func TestAttachmentUploaderShutdownReportsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{},
			})
		case "/upload":
			w.WriteHeader(http.StatusOK)
		case "/attachment/status":
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := auth.NewTestSession("api-key", "org-id", "org-name", server.URL, server.URL, server.URL, logger.Discard())
	uploader := NewUploader(session, logger.Discard())

	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))
	err := uploader.Shutdown(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to log attachment status")
}

func TestAttachmentUploaderReportsError(t *testing.T) {
	var statusReq map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{},
			})
		case "/upload":
			w.WriteHeader(http.StatusInternalServerError)
		case "/attachment/status":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&statusReq))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := auth.NewTestSession("api-key", "org-id", "org-name", server.URL, server.URL, server.URL, logger.Discard())
	uploader := NewUploader(session, logger.Discard())

	ref := Reference{Type: "braintrust_attachment", Filename: "input.json", ContentType: "application/json", Key: "key-1"}
	require.NoError(t, uploader.Enqueue(context.Background(), Upload{Reference: ref, Data: []byte(`{"hello":"world"}`)}))
	err := uploader.ForceFlush(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to upload attachment to object store")

	status, ok := statusReq["status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "error", status["upload_status"])
	require.NotEmpty(t, status["error_message"])
}
