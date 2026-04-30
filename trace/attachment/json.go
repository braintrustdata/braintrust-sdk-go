package attachment

import internalattachment "github.com/braintrustdata/braintrust-sdk-go/internal/attachment"

// JSON is the MIME type for JSON attachments.
const JSON = "application/json"

// JSONAttachmentOption configures JSON attachment creation.
type JSONAttachmentOption func(*JSONAttachmentOptions)

// JSONAttachmentOptions holds configuration for JSON attachments.
type JSONAttachmentOptions struct {
	Filename string
	Pretty   bool
}

// WithFilename sets the display filename for the attachment.
func WithFilename(filename string) JSONAttachmentOption {
	return func(o *JSONAttachmentOptions) {
		o.Filename = filename
	}
}

// WithPrettyJSON pretty-prints JSON attachment contents with two-space indentation.
func WithPrettyJSON() JSONAttachmentOption {
	return func(o *JSONAttachmentOptions) {
		o.Pretty = true
	}
}

// AttachmentReference is the JSON object stored on a span in place of uploaded attachment data.
// Its shape intentionally matches Python/JavaScript Braintrust attachment references.
//
//revive:disable-next-line:exported This public name matches the cross-SDK attachment API.
type AttachmentReference = internalattachment.Reference
