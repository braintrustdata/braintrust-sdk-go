package attachmentprocessor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Uploader enqueues attachment data for background upload.
type Uploader interface {
	// Enqueue adds an upload job. Returns false if the uploader is shut down
	// or the queue is full.
	Enqueue(ref Reference, data []byte) bool
	// ForceFlush blocks until all currently-enqueued uploads complete or
	// timeout expires.
	ForceFlush(timeout time.Duration) bool
	// Shutdown stops the uploader, waiting up to a generous timeout for
	// pending uploads.
	Shutdown()
	// IsShutdown returns true if the uploader has been shut down.
	IsShutdown() bool
}

// UploaderConfig holds configuration for the S3 uploader.
type UploaderConfig struct {
	APIURL          string
	APIKey          string
	OrgID           string // If empty, resolved via login endpoint.
	HTTPClient      *http.Client
	Logger          logger.Logger
	MaxRetries      int
	InitialBackoff  time.Duration
	RequestTimeout  time.Duration
	QueueSize       int
	ShutdownTimeout time.Duration
	LoginURL        string // App URL for login endpoint (e.g. "https://www.braintrust.dev").
}

func (c *UploaderConfig) defaults() {
	if c.MaxRetries <= 0 {
		c.MaxRetries = 8
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = 500 * time.Millisecond
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 60 * time.Second
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 1024
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 120 * time.Second
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.RequestTimeout}
	}
	if c.Logger == nil {
		c.Logger = logger.Discard()
	}
}

// uploadJob is an item in the upload queue.
type uploadJob struct {
	ref  Reference
	data []byte
}

// S3Uploader uploads attachments in the background via signed URLs.
type S3Uploader struct {
	cfg UploaderConfig
	log logger.Logger

	queue chan uploadJob
	stop  chan struct{} // closed by Shutdown to tell the worker to drain and exit
	done  chan struct{} // closed when the worker exits

	mu            sync.Mutex
	rejectNewJobs bool
	workerStarted bool

	// orgID resolution: orgIDOnce ensures resolveOrgID runs at most once,
	// even if multiple goroutines call getOrgID concurrently (defensive
	// against future concurrency — today only the single worker calls it).
	orgIDOnce sync.Once
	orgID     string
	orgIDErr  error

	shutdownOnce sync.Once

	// idleMu/idleCond track when the worker is idle (queue empty AND no
	// in-flight upload). ForceFlush waits on idleCond.
	idleMu   sync.Mutex
	idleCond *sync.Cond
	inflight int // number of jobs currently being processed
}

// NewS3Uploader creates and returns a new S3 uploader. The background worker
// starts lazily on the first Enqueue call.
func NewS3Uploader(cfg UploaderConfig) *S3Uploader {
	cfg.defaults()
	u := &S3Uploader{
		cfg:   cfg,
		log:   cfg.Logger,
		queue: make(chan uploadJob, cfg.QueueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	u.idleCond = sync.NewCond(&u.idleMu)
	return u
}

// Enqueue adds an upload job. Returns false if the uploader is shut down or the queue is full.
func (u *S3Uploader) Enqueue(ref Reference, data []byte) bool {
	u.mu.Lock()
	if u.rejectNewJobs {
		u.mu.Unlock()
		return false
	}
	u.ensureWorkerStartedLocked()

	// Track this job as pending before sending to the channel so that
	// ForceFlush can't observe an idle state between the channel send
	// and the worker picking it up.
	u.idleMu.Lock()
	u.inflight++
	u.idleMu.Unlock()

	// Hold mu across the send so this can't race with Shutdown setting
	// rejectNewJobs. The send is non-blocking to avoid holding mu while
	// the queue is full. The queue channel is never closed (Shutdown uses
	// a separate stop channel instead), so there's no send-on-closed risk.
	//
	// Lock ordering note: mu is acquired first, then idleMu. Other paths
	// that touch both locks must follow this order to avoid deadlock.
	select {
	case u.queue <- uploadJob{ref: ref, data: data}:
		u.mu.Unlock()
		return true
	default:
		u.mu.Unlock()
		// Queue full — undo the inflight bump.
		u.idleMu.Lock()
		u.inflight--
		u.idleMu.Unlock()
		u.idleCond.Broadcast()
		return false
	}
}

// ForceFlush blocks until all currently-enqueued uploads complete or timeout expires.
func (u *S3Uploader) ForceFlush(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// cancelled is read/written under idleMu so the timeout path can't lose
	// a Broadcast to the waiter (sync.Cond missed-signal race).
	cancelled := false
	done := make(chan struct{})
	go func() {
		u.idleMu.Lock()
		for u.inflight > 0 && !cancelled {
			u.idleCond.Wait()
		}
		u.idleMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-timer.C:
		// Hold idleMu while setting cancelled+broadcasting so the waiter
		// can't miss the wakeup: if the waiter is between its check and
		// Wait(), it's still holding idleMu and we'll block here until it
		// enters Wait(); if it's already in Wait(), Broadcast wakes it.
		u.idleMu.Lock()
		cancelled = true
		u.idleMu.Unlock()
		u.idleCond.Broadcast()
		return false
	}
}

// Shutdown stops the uploader, waiting up to a generous timeout for pending
// uploads. Safe to call multiple times.
func (u *S3Uploader) Shutdown() {
	u.shutdownOnce.Do(func() {
		u.mu.Lock()
		u.rejectNewJobs = true
		started := u.workerStarted
		u.mu.Unlock()

		if !started {
			close(u.done)
			return
		}

		// Signal the worker to drain remaining jobs and exit.
		close(u.stop)

		// Wait for worker to finish with timeout.
		select {
		case <-u.done:
		case <-time.After(u.cfg.ShutdownTimeout):
			u.log.Warn("attachment uploader shutdown timed out")
		}
	})
}

// IsShutdown returns true if the uploader has been shut down.
func (u *S3Uploader) IsShutdown() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.rejectNewJobs
}

// ensureWorkerStartedLocked starts the worker goroutine if not already running.
// Must be called with u.mu held.
func (u *S3Uploader) ensureWorkerStartedLocked() {
	if u.workerStarted {
		return
	}
	u.workerStarted = true
	go u.workerLoop()
}

func (u *S3Uploader) workerLoop() {
	defer close(u.done)
	u.log.Debug("attachment uploader worker started")

	for {
		select {
		case job := <-u.queue:
			u.processJob(job)
		case <-u.stop:
			// Drain remaining jobs before exiting.
			for {
				select {
				case job := <-u.queue:
					u.processJob(job)
				default:
					u.idleCond.Broadcast()
					u.log.Debug("attachment uploader worker stopped")
					return
				}
			}
		}
	}
}

// processJob runs a single upload with panic recovery, then accounts for
// the completed job. A panic in upload code would otherwise kill the worker
// goroutine permanently while leaving workerStarted=true and rejectNewJobs=false —
// silently wedging the uploader.
func (u *S3Uploader) processJob(job uploadJob) {
	defer func() {
		if r := recover(); r != nil {
			u.log.Error("attachment upload panicked", "key", job.ref.Key, "panic", r)
			u.failAndReject()
		}
		// Always account for the job, even on panic, so ForceFlush can
		// make progress.
		u.idleMu.Lock()
		u.inflight--
		u.idleMu.Unlock()
		u.idleCond.Broadcast()
	}()
	u.upload(job)
}

func (u *S3Uploader) upload(job uploadJob) {
	orgID, err := u.getOrgID()
	if err != nil {
		u.log.Warn("failed to resolve org ID for attachment upload", "error", err)
		u.reportStatus(job.ref.Key, "error", err.Error())
		u.failAndReject()
		return
	}

	signedURL, headers, err := u.requestUploadURL(orgID, job.ref)
	if err != nil {
		u.log.Warn("failed to request upload URL", "key", job.ref.Key, "error", err)
		u.reportStatus(job.ref.Key, "error", err.Error())
		u.failAndReject()
		return
	}

	if err := u.uploadToSignedURL(signedURL, headers, job.ref.ContentType, job.data); err != nil {
		u.log.Warn("failed to upload to signed URL", "key", job.ref.Key, "error", err)
		u.reportStatus(job.ref.Key, "error", err.Error())
		u.failAndReject()
		return
	}

	u.reportStatus(job.ref.Key, "done", "")
}

func (u *S3Uploader) failAndReject() {
	u.mu.Lock()
	u.rejectNewJobs = true
	u.mu.Unlock()
}

func (u *S3Uploader) getOrgID() (string, error) {
	u.orgIDOnce.Do(func() {
		if u.cfg.OrgID != "" {
			u.orgID = u.cfg.OrgID
			return
		}
		u.orgID, u.orgIDErr = u.resolveOrgID()
	})
	return u.orgID, u.orgIDErr
}

func (u *S3Uploader) resolveOrgID() (string, error) {
	loginURL := u.cfg.LoginURL
	if loginURL == "" {
		loginURL = u.cfg.APIURL
	}
	reqURL := strings.TrimRight(loginURL, "/") + "/api/apikey/login"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+u.cfg.APIKey)

	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login returned status %d: %s", resp.StatusCode, string(body))
	}

	var loginResp struct {
		OrgInfo []struct {
			ID string `json:"id"`
		} `json:"org_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if len(loginResp.OrgInfo) == 0 {
		return "", fmt.Errorf("no org info returned from login")
	}
	return loginResp.OrgInfo[0].ID, nil
}

// ── S3 HTTP operations ─────────────────────────────────────────────

// uploadURLRequest is the JSON body sent to POST /attachment.
type uploadURLRequest struct {
	Key         string `json:"key"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	OrgID       string `json:"org_id"`
}

func (u *S3Uploader) requestUploadURL(orgID string, ref Reference) (signedURL string, headers map[string]string, err error) {
	body, err := json.Marshal(uploadURLRequest{
		Key:         ref.Key,
		Filename:    ref.Filename,
		ContentType: ref.ContentType,
		OrgID:       orgID,
	})
	if err != nil {
		return "", nil, err
	}

	reqURL := strings.TrimRight(u.cfg.APIURL, "/") + "/attachment"
	respBody, err := u.doWithRetry(http.MethodPost, reqURL, "application/json", body, true)
	if err != nil {
		return "", nil, err
	}

	var result struct {
		SignedURL string            `json:"signedUrl"`
		Headers   map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, fmt.Errorf("decode upload URL response: %w", err)
	}
	if result.SignedURL == "" {
		return "", nil, fmt.Errorf("signed URL response missing signedUrl")
	}
	if result.Headers == nil {
		result.Headers = map[string]string{}
	}
	return result.SignedURL, result.Headers, nil
}

func (u *S3Uploader) uploadToSignedURL(signedURL string, headers map[string]string, contentType string, data []byte) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, signedURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	addProviderSpecificHeaders(signedURL, req)

	resp, err := u.doRequestWithRetry(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload to object store: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (u *S3Uploader) reportStatus(key, status, errMsg string) {
	orgID, err := u.getOrgID()
	if err != nil {
		u.log.Warn("failed to get org ID for status report", "error", err)
		return
	}

	statusMap := map[string]any{"upload_status": status}
	if errMsg != "" {
		statusMap["error_message"] = errMsg
	}

	body, err := json.Marshal(map[string]any{
		"key":    key,
		"org_id": orgID,
		"status": statusMap,
	})
	if err != nil {
		u.log.Warn("failed to marshal status report", "error", err)
		return
	}

	reqURL := strings.TrimRight(u.cfg.APIURL, "/") + "/attachment/status"
	if _, err := u.doWithRetry(http.MethodPost, reqURL, "application/json", body, true); err != nil {
		u.log.Warn("failed to report attachment status", "key", key, "status", status, "error", err)
	}
}

// ── HTTP helpers ───────────────────────────────────────────────────

func (u *S3Uploader) doWithRetry(method, reqURL, contentType string, body []byte, auth bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if auth {
		req.Header.Set("Authorization", "Bearer "+u.cfg.APIKey)
	}

	resp, err := u.doRequestWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (u *S3Uploader) doRequestWithRetry(req *http.Request) (*http.Response, error) {
	var lastErr error
	backoff := u.cfg.InitialBackoff

	for attempt := 0; attempt <= u.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			u.log.Debug("retrying request", "url", req.URL.String(), "attempt", attempt)
			// Sleep cancellable by Shutdown so retry backoff doesn't keep
			// the worker (and thus Shutdown) running far past the user's
			// deadline.
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-u.stop:
				timer.Stop()
				return nil, fmt.Errorf("request to %s cancelled during retry backoff", req.URL.String())
			}
			backoff *= 2
		}

		// Clone body for retry.
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := u.cfg.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Don't retry client errors (4xx) or successes.
		if resp.StatusCode < 500 {
			return resp, nil
		}

		// Server error (5xx) — retry.
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("server error: HTTP %d", resp.StatusCode)
	}

	return nil, fmt.Errorf("request to %s failed after %d retries: %w",
		req.URL.String(), u.cfg.MaxRetries, lastErr)
}

// addProviderSpecificHeaders inspects the signed URL host and adds any
// headers required by that specific cloud storage provider. Braintrust's
// backend may issue signed URLs for different providers depending on the
// org's configuration.
func addProviderSpecificHeaders(signedURL string, req *http.Request) {
	u, err := url.Parse(signedURL)
	if err != nil {
		return
	}
	// Azure Blob Storage requires this header on PUT uploads or the
	// request fails with HTTP 400.
	if strings.HasSuffix(u.Host, ".blob.core.windows.net") {
		req.Header.Set("x-ms-blob-type", "BlockBlob")
	}
}

// NoopUploader is an uploader that accepts all jobs but does nothing.
// Useful for testing the processor in isolation.
type NoopUploader struct {
	shutdown atomic.Bool
}

// Enqueue accepts the job but does nothing.
func (u *NoopUploader) Enqueue(_ Reference, _ []byte) bool { return !u.shutdown.Load() }

// ForceFlush is a no-op that always succeeds.
func (u *NoopUploader) ForceFlush(_ time.Duration) bool { return true }

// Shutdown marks the uploader as shut down.
func (u *NoopUploader) Shutdown() { u.shutdown.Store(true) }

// IsShutdown returns true if the uploader has been shut down.
func (u *NoopUploader) IsShutdown() bool { return u.shutdown.Load() }
