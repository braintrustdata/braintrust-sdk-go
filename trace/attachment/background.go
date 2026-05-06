package attachment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	btlog "github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Reference is the JSON object stored in traces in place of inline attachment data.
type Reference struct {
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Key         string `json:"key"`
}

// Uploader uploads inline base64 attachments in the background.
const (
	base64AttachmentType = "base64_attachment"
	maxConcurrentUploads = 4
	uploadQueueSize      = 30
)

type uploadTask struct {
	ref  Reference
	data []byte
}

// Uploader uploads span attachment data and rewrites private attachment
// attributes into exported Braintrust attachment references.
type Uploader struct {
	session *auth.Session
	logger  btlog.Logger
	client  *http.Client
	queue   chan uploadTask

	startOnce sync.Once
	closeOnce sync.Once

	mu       sync.Mutex
	done     chan struct{}
	errs     []error
	inFlight int
}

// NewUploader creates and starts an attachment uploader for a Braintrust session.
func NewUploader(session *auth.Session, log btlog.Logger) *Uploader {
	if log == nil {
		log = btlog.Discard()
	}
	done := make(chan struct{})
	close(done)
	u := &Uploader{
		session: session,
		logger:  log,
		client:  &http.Client{Timeout: 60 * time.Second},
		queue:   make(chan uploadTask, uploadQueueSize),
		done:    done,
	}
	u.Start()
	return u
}

// Start launches the background attachment upload workers.
func (u *Uploader) Start() {
	u.startOnce.Do(func() {
		for i := 0; i < maxConcurrentUploads; i++ {
			go u.worker()
		}
	})
}

// Shutdown waits for queued uploads to finish and stops the uploader workers.
func (u *Uploader) Shutdown(ctx context.Context) error {
	err := u.Wait(ctx)
	u.closeOnce.Do(func() { close(u.queue) })
	return err
}

// Wait blocks until all in-flight attachment uploads complete.
func (u *Uploader) Wait(ctx context.Context) error {
	done, idle := u.doneChan()
	if idle {
		return u.err()
	}

	select {
	case <-done:
		return u.err()
	case <-ctx.Done():
		return errors.Join(ctx.Err(), u.err())
	}
}

// ReplaceSpanAttachmentAttrs rewrites private attachment data attributes into
// public Braintrust attachment reference attributes and queues uploads.
func (u *Uploader) ReplaceSpanAttachmentAttrs(attrs []attribute.KeyValue) ([]attribute.KeyValue, bool) {
	var rewritten []attribute.KeyValue
	changed := false

	for i, attr := range attrs {
		key := string(attr.Key)
		if !strings.HasPrefix(key, attachmentDataAttrPrefix) {
			if rewritten != nil {
				rewritten = append(rewritten, attr)
			}
			continue
		}
		if attr.Value.Type() != attribute.STRING {
			continue
		}
		ref, data, ok := attachmentFromJSON(attr.Value.AsString())
		if !ok {
			continue
		}
		if rewritten == nil {
			rewritten = make([]attribute.KeyValue, 0, len(attrs))
			rewritten = append(rewritten, attrs[:i]...)
		}
		publicKey := strings.TrimPrefix(key, attachmentDataAttrPrefix)
		refJSON, err := json.Marshal(ref)
		if err != nil {
			continue
		}
		rewritten = setOrAppendAttr(rewritten, attribute.String(publicKey, string(refJSON)))
		u.uploadAsync(ref, data)
		changed = true
	}
	if !changed {
		return attrs, false
	}
	return rewritten, true
}

func setOrAppendAttr(attrs []attribute.KeyValue, attr attribute.KeyValue) []attribute.KeyValue {
	for i := range attrs {
		if attrs[i].Key == attr.Key {
			attrs[i] = attr
			return attrs
		}
	}
	return append(attrs, attr)
}

func attachmentFromJSON(jsonStr string) (Reference, []byte, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return Reference{}, nil, false
	}
	return attachmentFromMap(m)
}

func attachmentFromMap(m map[string]any) (Reference, []byte, bool) {
	if typ, _ := m["type"].(string); typ != base64AttachmentType {
		return Reference{}, nil, false
	}
	content, _ := m["content"].(string)
	ct, data, ok := parseDataURL(content)
	if !ok {
		return Reference{}, nil, false
	}
	return newReference("attachment", ct), data, true
}

func newReference(filename, contentType string) Reference {
	return Reference{
		Type:        "braintrust_attachment",
		Filename:    filename,
		ContentType: contentType,
		Key:         uuid.NewString(),
	}
}

func parseDataURL(s string) (string, []byte, bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return "", nil, false
	}
	meta, payload := s[5:comma], s[comma+1:]
	parts := strings.Split(meta, ";")
	if len(parts) < 2 || parts[len(parts)-1] != "base64" {
		return "", nil, false
	}
	ct := parts[0]
	if ct == "" {
		ct = "application/octet-stream"
	}
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, false
	}
	return ct, data, true
}

func (u *Uploader) uploadAsync(ref Reference, data []byte) {
	u.startUpload()

	// Backpressure: block span export when the bounded upload queue is full,
	// matching Sentry's transport pattern of a fixed queue plus workers while
	// preserving Braintrust's requirement that attachment references are not
	// silently dropped.
	u.queue <- uploadTask{ref: ref, data: data}
}

func (u *Uploader) worker() {
	for task := range u.queue {
		if err := u.upload(context.Background(), task.ref, task.data); err != nil {
			err = fmt.Errorf("upload attachment %s: %w", task.ref.Key, err)
			u.addErr(err)
			u.logger.Warn("failed to upload attachment", "error", err, "key", task.ref.Key)
		}
		u.finishUpload()
	}
}

func (u *Uploader) startUpload() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.inFlight == 0 {
		u.done = make(chan struct{})
	}
	u.inFlight++
}

func (u *Uploader) finishUpload() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.inFlight--
	if u.inFlight == 0 {
		close(u.done)
	}
}

func (u *Uploader) doneChan() (<-chan struct{}, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.done, u.inFlight == 0
}

func (u *Uploader) addErr(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.errs = append(u.errs, err)
}

func (u *Uploader) err() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return errors.Join(u.errs...)
}

func (u *Uploader) upload(ctx context.Context, ref Reference, data []byte) error {
	if err := u.session.Login(ctx); err != nil {
		return fmt.Errorf("login before attachment upload: %w", err)
	}
	api := u.session.APIInfo()
	org := u.session.OrgInfo()
	body := map[string]string{"key": ref.Key, "filename": ref.Filename, "content_type": ref.ContentType, "org_id": org.ID}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal attachment request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api.APIURL, "/")+"/attachment", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+api.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("signed URL request failed: %s", resp.Status)
	}
	var meta struct {
		SignedURL string            `json:"signedUrl"`
		Headers   map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return err
	}
	if meta.SignedURL == "" {
		return fmt.Errorf("missing signedUrl")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, meta.SignedURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range meta.Headers {
		req.Header.Set(k, v)
	}
	resp, err = u.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("object upload failed: %s", resp.Status)
	}
	return nil
}
