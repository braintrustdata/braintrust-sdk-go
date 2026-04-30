package attachment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	apiattachment "github.com/braintrustdata/braintrust-sdk-go/api/attachment"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

const defaultQueueSize = 1000

type uploadJob struct {
	ctx    context.Context
	upload Upload
	batch  *uploadBatch
}

type uploadBatch struct {
	pending int
	idle    chan struct{}
}

// Uploader manages background attachment uploads for a Braintrust session.
type Uploader struct {
	session    *auth.Session
	logger     logger.Logger
	ctx        context.Context
	cancel     context.CancelFunc
	queue      chan uploadJob
	done       chan struct{}
	workerDone chan struct{}
	once       sync.Once

	mu            sync.Mutex
	batch         *uploadBatch
	err           error
	closed        bool
	client        *https.Client
	attachmentAPI *apiattachment.API
}

// NewUploader starts a background worker for attachment uploads.
func NewUploader(session *auth.Session, log logger.Logger) *Uploader {
	if log == nil {
		log = logger.Discard()
	}
	ctx, cancel := context.WithCancel(context.Background())
	u := &Uploader{
		session:    session,
		logger:     log,
		ctx:        ctx,
		cancel:     cancel,
		queue:      make(chan uploadJob, defaultQueueSize),
		done:       make(chan struct{}),
		workerDone: make(chan struct{}),
	}
	go u.worker()
	return u
}

func (u *Uploader) uploadContext(ctx context.Context) context.Context {
	return uploaderContext{Context: context.WithoutCancel(ctx), done: u.ctx.Done()}
}

type uploaderContext struct {
	context.Context
	done <-chan struct{}
}

func (c uploaderContext) Done() <-chan struct{} { return c.done }

func (c uploaderContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// Enqueue schedules an attachment upload.
func (u *Uploader) Enqueue(ctx context.Context, upload Upload) error {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return fmt.Errorf("attachment uploader is shut down")
	}
	if u.batch == nil {
		u.batch = &uploadBatch{idle: make(chan struct{})}
	}
	batch := u.batch
	batch.pending++
	u.mu.Unlock()

	select {
	case u.queue <- uploadJob{ctx: u.uploadContext(ctx), upload: upload, batch: batch}:
		return nil
	default:
		u.finishPending(batch)
		return fmt.Errorf("attachment upload queue is full")
	}
}

// ForceFlush waits for all uploads enqueued before the call to complete.
func (u *Uploader) ForceFlush(ctx context.Context) error {
	u.mu.Lock()
	batch := u.batch
	u.mu.Unlock()
	if batch == nil {
		return u.takeErr()
	}

	select {
	case <-batch.idle:
		return u.takeErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *Uploader) worker() {
	defer close(u.workerDone)
	for {
		select {
		case job := <-u.queue:
			func() {
				defer func() {
					if r := recover(); r != nil {
						u.logger.Error("attachment upload panic", "key", job.upload.Reference.Key, "panic", r)
					}
					u.finishPending(job.batch)
				}()
				if err := u.upload(job.ctx, job.upload); err != nil {
					u.recordError(err)
					u.logger.Warn("failed to upload attachment", "key", job.upload.Reference.Key, "error", err)
				}
			}()
		case <-u.done:
			u.discardQueued()
			return
		}
	}
}

func (u *Uploader) discardQueued() {
	for {
		select {
		case job := <-u.queue:
			u.finishPending(job.batch)
		default:
			return
		}
	}
}

func (u *Uploader) recordError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.err = errors.Join(u.err, err)
}

func (u *Uploader) takeErr() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	err := u.err
	u.err = nil
	return err
}

func (u *Uploader) finishPending(batch *uploadBatch) {
	u.mu.Lock()
	defer u.mu.Unlock()
	batch.pending--
	if batch.pending == 0 {
		close(batch.idle)
		if u.batch == batch {
			u.batch = nil
		}
	}
}

// Shutdown stops accepting uploads and shuts down the background worker.
func (u *Uploader) Shutdown(ctx context.Context) error {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
	flushErr := u.ForceFlush(ctx)
	u.once.Do(func() {
		u.cancel()
		close(u.done)
	})

	select {
	case <-u.workerDone:
		return flushErr
	case <-ctx.Done():
		return errors.Join(flushErr, ctx.Err())
	}
}

func (u *Uploader) upload(ctx context.Context, upload Upload) error {
	if err := u.session.Login(ctx); err != nil {
		return fmt.Errorf("failed to login before attachment upload: %w", err)
	}

	attachmentAPI, client, orgID := u.attachmentClient()

	status := map[string]any{"upload_status": "done"}
	if err := u.doUpload(ctx, attachmentAPI, client, orgID, upload); err != nil {
		status["upload_status"] = "error"
		status["error_message"] = err.Error()
		if statusErr := u.postStatus(ctx, attachmentAPI, orgID, upload.Reference.Key, status); statusErr != nil {
			return fmt.Errorf("%w; additionally failed to log attachment status: %v", err, statusErr)
		}
		return err
	}

	if err := u.postStatus(ctx, attachmentAPI, orgID, upload.Reference.Key, status); err != nil {
		return fmt.Errorf("failed to log attachment status: %w", err)
	}
	return nil
}

func (u *Uploader) attachmentClient() (*apiattachment.API, *https.Client, string) {
	apiInfo := u.session.APIInfo()
	orgInfo := u.session.OrgInfo()

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.client == nil {
		u.client = https.NewClient(apiInfo.APIKey, apiInfo.APIURL, u.logger)
		u.attachmentAPI = apiattachment.New(u.client)
	}
	return u.attachmentAPI, u.client, orgInfo.ID
}

func (u *Uploader) doUpload(ctx context.Context, attachmentAPI *apiattachment.API, client *https.Client, orgID string, upload Upload) error {
	metadata, err := attachmentAPI.RequestUploadURL(ctx, apiattachment.UploadURLParams{
		Key:         upload.Reference.Key,
		Filename:    upload.Reference.Filename,
		ContentType: upload.Reference.ContentType,
		OrgID:       orgID,
	})
	if err != nil {
		return fmt.Errorf("failed to request signed URL: %w", err)
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, metadata.SignedURL, bytes.NewReader(upload.Data))
	if err != nil {
		return fmt.Errorf("failed to create object store request: %w", err)
	}
	addAzureBlobHeaders(metadata.Headers, metadata.SignedURL)
	for k, v := range metadata.Headers {
		putReq.Header.Set(k, v)
	}

	putResp, err := client.Client().Do(putReq)
	if err != nil {
		return fmt.Errorf("failed to upload attachment to object store: %w", err)
	}
	defer func() { _ = putResp.Body.Close() }()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		body, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("failed to upload attachment to object store: status %d body %s", putResp.StatusCode, string(body))
	}

	return nil
}

func addAzureBlobHeaders(headers map[string]string, signedURL string) {
	u, err := url.Parse(signedURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	if host == "blob.core.windows.net" || strings.HasSuffix(host, ".blob.core.windows.net") {
		headers["x-ms-blob-type"] = "BlockBlob"
	}
}

func (u *Uploader) postStatus(ctx context.Context, attachmentAPI *apiattachment.API, orgID string, key string, status map[string]any) error {
	return attachmentAPI.UpdateStatus(ctx, apiattachment.StatusParams{
		Key:    key,
		OrgID:  orgID,
		Status: status,
	})
}
