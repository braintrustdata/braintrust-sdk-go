package attachment

import "context"

// Reference is the JSON object stored on a span in place of uploaded attachment data.
type Reference struct {
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Key         string `json:"key"`
}

// Upload is a pending uploaded attachment.
type Upload struct {
	Reference Reference
	Data      []byte
}

// SpanUploader uploads attachments and tracks pending work for flush/shutdown.
type SpanUploader interface {
	Enqueue(ctx context.Context, upload Upload) error
	ForceFlush(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
