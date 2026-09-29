package attachmentprocessor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestS3UploaderEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var uploadedData []byte
	var uploadedContentType string
	var statusReported string
	var attachmentReqBody map[string]string
	var serverURL string // set after server starts

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.URL.Path == "/api/apikey/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"org_info":[{"id":"org-123","name":"test-org"}]}`))

		case r.URL.Path == "/attachment" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &attachmentReqBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedUrl":"` + serverURL + `/upload","headers":{"x-custom":"val"}}`))

		case r.URL.Path == "/upload" && r.Method == http.MethodPut:
			uploadedData, _ = io.ReadAll(r.Body)
			uploadedContentType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/attachment/status" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			status := req["status"].(map[string]any)
			statusReported = status["upload_status"].(string)
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	u := NewS3Uploader(UploaderConfig{
		APIURL:     server.URL,
		APIKey:     "test-key",
		LoginURL:   server.URL,
		HTTPClient: server.Client(),
		Logger:     logger.Discard(),
		QueueSize:  16,
	})

	ref := Reference{
		Type:        "braintrust_attachment",
		ContentType: "image/png",
		Filename:    "attachment.png",
		Key:         "test-key-123",
	}

	ok := u.Enqueue(ref, []byte("fake-png-data"))
	require.True(t, ok, "enqueue should succeed")

	flushed := u.ForceFlush(5 * time.Second)
	assert.True(t, flushed, "flush should succeed")

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []byte("fake-png-data"), uploadedData)
	assert.Equal(t, "image/png", uploadedContentType)
	assert.Equal(t, "done", statusReported)
	assert.Equal(t, "test-key-123", attachmentReqBody["key"])
	assert.Equal(t, "attachment.png", attachmentReqBody["filename"])
	assert.Equal(t, "image/png", attachmentReqBody["content_type"])
	assert.Equal(t, "org-123", attachmentReqBody["org_id"])
}

func TestS3UploaderShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apikey/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"org_info":[{"id":"org-123","name":"test-org"}]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	u := NewS3Uploader(UploaderConfig{
		APIURL:     server.URL,
		APIKey:     "test-key",
		LoginURL:   server.URL,
		HTTPClient: server.Client(),
		Logger:     logger.Discard(),
	})

	u.Shutdown()
	assert.True(t, u.IsShutdown())

	ok := u.Enqueue(Reference{Key: "k"}, []byte("data"))
	assert.False(t, ok, "enqueue after shutdown should return false")
}

func TestS3UploaderEnqueueDuringShutdown(t *testing.T) {
	var srvURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apikey/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"org_info":[{"id":"org-1"}]}`))
		case "/attachment":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedUrl":"` + srvURL + `/upload","headers":{}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	srvURL = server.URL

	u := NewS3Uploader(UploaderConfig{
		APIURL:     server.URL,
		APIKey:     "k",
		LoginURL:   server.URL,
		HTTPClient: server.Client(),
		Logger:     logger.Discard(),
		QueueSize:  4,
	})

	// Warm up the worker so queue is open.
	u.Enqueue(NewReference("image/png"), []byte("warm"))

	// Race Enqueue against Shutdown — must not panic.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		u.Shutdown()
	}()
	go func() {
		defer wg.Done()
		// May return true or false, but must not panic.
		u.Enqueue(NewReference("image/png"), []byte("race"))
	}()
	wg.Wait()
	assert.True(t, u.IsShutdown())
}

// Queue backpressure needs a controllably stalled upload, which a recorded
// server response cannot provide.
func newBlockedUploader(t *testing.T, fail bool, queueSize int) (*S3Uploader, <-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedUrl":"http://` + r.Host + `/upload","headers":{}}`))
		case "/upload":
			if uploads.Add(1) == 1 {
				close(started)
				<-release
				if fail {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	u := NewS3Uploader(UploaderConfig{
		APIURL: server.URL, APIKey: "key", OrgID: "org", HTTPClient: server.Client(),
		QueueSize: queueSize, MaxRetries: 1, ShutdownTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() {
		unblock()
		u.Shutdown()
		assert.True(t, u.ForceFlush(5*time.Second))
		server.Close()
	})
	return u, started, unblock, &uploads
}

func enqueueOne(ctx context.Context, u *S3Uploader, data string) bool {
	return u.enqueueAllContext(ctx, []uploadJob{{ref: NewReference("image/png"), data: []byte(data)}})
}

func TestS3UploaderEnqueueAllContextAdmitsWholeGroup(t *testing.T) {
	u, started, release, uploads := newBlockedUploader(t, false, 2)
	require.True(t, u.Enqueue(NewReference("image/png"), []byte("first")))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first upload did not start")
	}
	require.True(t, u.Enqueue(NewReference("image/png"), []byte("queued")))
	group := func(n int) []uploadJob {
		jobs := make([]uploadJob, n)
		for i := range jobs {
			jobs[i] = uploadJob{ref: NewReference("image/png"), data: []byte("group")}
		}
		return jobs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.False(t, u.enqueueAllContext(ctx, group(2)), "a group must not be admitted partially")
	assert.False(t, u.enqueueAllContext(context.Background(), group(3)), "a group larger than the queue never fits")
	assert.True(t, u.enqueueAllContext(ctx, group(1)), "an expired wait still admits a group that fits")
	release()
	require.True(t, u.ForceFlush(5*time.Second))
	assert.Equal(t, int32(3), uploads.Load())
}

func TestS3UploaderEnqueueAllContextBackpressure(t *testing.T) {
	u, started, release, uploads := newBlockedUploader(t, false, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, u.Enqueue(NewReference("image/png"), []byte("first")))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first upload did not start")
	}
	require.True(t, u.Enqueue(NewReference("image/png"), []byte("queued")))
	require.False(t, u.Enqueue(NewReference("image/png"), []byte("nonblocking")),
		"existing Enqueue callers must still reject a full queue")

	const waiters = 8
	results := make(chan bool, waiters)
	for range waiters {
		go func() {
			results <- enqueueOne(ctx, u, "waiting")
		}()
	}
	select {
	case <-results:
		t.Fatal("enqueue returned while the queue was full")
	case <-time.After(20 * time.Millisecond):
	}
	assert.False(t, u.ForceFlush(time.Millisecond), "queued uploads are still pending")
	release()
	for range waiters {
		select {
		case ok := <-results:
			require.True(t, ok)
		case <-ctx.Done():
			t.Fatal("enqueue did not resume after queue space became available")
		}
	}
	require.True(t, u.ForceFlush(5*time.Second))
	assert.Equal(t, int32(2+waiters), uploads.Load())
}

func TestS3UploaderEnqueueAllContextInterrupted(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown", "upload-failure"} {
		t.Run(mode, func(t *testing.T) {
			u, started, release, uploads := newBlockedUploader(t, mode == "upload-failure", 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.True(t, u.Enqueue(NewReference("image/png"), []byte("first")))
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first upload did not start")
			}
			require.True(t, u.Enqueue(NewReference("image/png"), []byte("queued")))
			result := make(chan bool, 1)
			go func() {
				result <- enqueueOne(ctx, u, "rejected")
			}()
			select {
			case <-result:
				t.Fatal("enqueue returned while the queue was full")
			case <-time.After(20 * time.Millisecond):
			}
			switch mode {
			case "cancel":
				cancel()
			case "shutdown":
				// Shutdown must acquire its lock and return even while the
				// worker is stuck and an enqueue is waiting for space.
				u.Shutdown()
			case "upload-failure":
				release()
			}
			select {
			case ok := <-result:
				require.False(t, ok)
			case <-time.After(5 * time.Second):
				t.Fatal("enqueue did not stop")
			}
			release()
			require.True(t, u.ForceFlush(5*time.Second))
			assert.Equal(t, int32(2), uploads.Load(), "only previously accepted jobs may upload")
			if mode == "cancel" {
				assert.False(t, u.IsShutdown(), "caller cancellation must not disable the uploader")
				require.True(t, enqueueOne(context.Background(), u, "later"))
				require.True(t, u.ForceFlush(5*time.Second))
				assert.Equal(t, int32(3), uploads.Load())
			} else {
				assert.True(t, u.IsShutdown())
			}
		})
	}
}

func TestS3UploaderDoubleShutdown(t *testing.T) {
	u := NewS3Uploader(UploaderConfig{
		APIURL: "http://localhost",
		APIKey: "test-key",
		Logger: logger.Discard(),
	})

	// First shutdown should succeed; second should not panic.
	u.Shutdown()
	assert.True(t, u.IsShutdown())
	u.Shutdown() // must not panic
}

// panickyTransport panics on the first request. Used to verify the worker
// goroutine survives a panic in the upload path and continues running.
type panickyTransport struct {
	calls atomic.Int32
}

func (t *panickyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.calls.Add(1) == 1 {
		panic("simulated panic in upload")
	}
	// Subsequent calls return a generic 500 so the test can assert
	// the worker is still alive.
	return &http.Response{
		StatusCode: 500,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}

func TestS3UploaderPanicRecovery(t *testing.T) {
	transport := &panickyTransport{}
	u := NewS3Uploader(UploaderConfig{
		APIURL:         "http://test",
		APIKey:         "k",
		OrgID:          "org-1", // skip login
		HTTPClient:     &http.Client{Transport: transport},
		Logger:         logger.Discard(),
		MaxRetries:     1,
		InitialBackoff: 1 * time.Millisecond,
	})

	// First enqueue triggers the panic. The worker must not die.
	ok := u.Enqueue(NewReference("image/png"), []byte("data"))
	require.True(t, ok)

	// Wait until the panic has been processed (inflight back to 0). Use
	// ForceFlush which waits on the idle condition.
	assert.True(t, u.ForceFlush(2*time.Second),
		"worker should account for the panicked job and become idle")

	// After a panic, failAndReject is called → uploader rejects further work.
	assert.True(t, u.IsShutdown(), "uploader should reject new jobs after a panic")

	// Cleanup.
	u.Shutdown()
}

func TestProviderSpecificHeaders(t *testing.T) {
	// Azure Blob Storage URL should get the x-ms-blob-type header.
	req, _ := http.NewRequest(http.MethodPut, "https://myaccount.blob.core.windows.net/container/blob?sig=xxx", nil)
	addProviderSpecificHeaders("https://myaccount.blob.core.windows.net/container/blob?sig=xxx", req)
	assert.Equal(t, "BlockBlob", req.Header.Get("x-ms-blob-type"))

	// S3 URL should not get the Azure-specific header.
	req2, _ := http.NewRequest(http.MethodPut, "https://s3.amazonaws.com/bucket/key", nil)
	addProviderSpecificHeaders("https://s3.amazonaws.com/bucket/key", req2)
	assert.Empty(t, req2.Header.Get("x-ms-blob-type"))
}

func TestS3UploaderPreConfiguredOrgID(t *testing.T) {
	var capturedOrgID string
	var srvURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/attachment" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			capturedOrgID = req["org_id"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedUrl":"` + srvURL + `/upload","headers":{}}`))
		case r.URL.Path == "/upload":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/attachment/status":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	srvURL = server.URL

	u := NewS3Uploader(UploaderConfig{
		APIURL:     server.URL,
		APIKey:     "test-key",
		OrgID:      "pre-configured-org",
		HTTPClient: server.Client(),
		Logger:     logger.Discard(),
	})

	ref := NewReference("image/png")
	ok := u.Enqueue(ref, []byte("data"))
	require.True(t, ok)

	u.ForceFlush(5 * time.Second)

	assert.Equal(t, "pre-configured-org", capturedOrgID)
}
