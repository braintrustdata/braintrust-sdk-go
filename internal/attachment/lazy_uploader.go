// Package attachment uploads Braintrust attachment payloads in the background.
package attachment

import (
	"context"
	"fmt"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// LazyUploader starts an Uploader on first enqueue.
type LazyUploader struct {
	session *auth.Session
	logger  logger.Logger

	mu       sync.Mutex
	uploader *Uploader
	closed   bool
}

// NewLazyUploader creates an uploader that starts its worker on first enqueue.
func NewLazyUploader(session *auth.Session, log logger.Logger) *LazyUploader {
	return &LazyUploader{session: session, logger: log}
}

func (u *LazyUploader) get() (*Uploader, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil, fmt.Errorf("attachment uploader is shut down")
	}
	if u.uploader == nil {
		u.uploader = NewUploader(u.session, u.logger)
	}
	return u.uploader, nil
}

// Enqueue schedules an attachment upload, creating the underlying uploader if needed.
func (u *LazyUploader) Enqueue(ctx context.Context, upload Upload) error {
	uploader, err := u.get()
	if err != nil {
		return err
	}
	return uploader.Enqueue(ctx, upload)
}

// ForceFlush waits for currently pending uploads to complete.
func (u *LazyUploader) ForceFlush(ctx context.Context) error {
	u.mu.Lock()
	uploader := u.uploader
	u.mu.Unlock()
	if uploader == nil {
		return nil
	}
	return uploader.ForceFlush(ctx)
}

// Shutdown prevents future uploads and waits for pending uploads to complete.
func (u *LazyUploader) Shutdown(ctx context.Context) error {
	u.mu.Lock()
	uploader := u.uploader
	u.closed = true
	u.mu.Unlock()
	if uploader == nil {
		return nil
	}
	return uploader.Shutdown(ctx)
}
