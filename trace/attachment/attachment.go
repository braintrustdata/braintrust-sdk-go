// Package attachment provides utilities for creating and managing attachments
// in Braintrust traces.
//
// Attachments allow you to log arbitrary binary data, like images, audio, video,
// PDFs, and large JSON objects, as part of your traces. This enables multimodal
// evaluations, handling substantial data structures, and advanced use cases such
// as summarizing visual content or analyzing document metadata. Attachments are
// securely stored in an object store and associated with your organization.
//
// Most users won't need to use this package directly, as the instrumentation
// middleware (traceopenai, traceanthropic) automatically handles attachment
// conversion. This package is primarily useful for:
//   - Manual logging without instrumentation
//   - Custom scenarios not covered by auto-instrumentation
//   - Writing instrumentation for new providers
//
// For more information, see: https://www.braintrust.dev/docs/guides/attachments
package attachment

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Common MIME types for attachments
const (
	ImageJPEG = "image/jpeg"
	ImagePNG  = "image/png"
	ImageGIF  = "image/gif"
	ImageWEBP = "image/webp"
	TextPlain = "text/plain"
	PDF       = "application/pdf"
)

// Attachment represents a file or binary data attachment.
// Attachments are single-use - once consumed, subsequent calls will error.
type Attachment struct {
	contentType string
	reader      io.Reader
	consumed    bool
}

// FromReader creates an attachment from an io.Reader.
// The reader is consumed lazily when Base64URL() or Base64Message() is called.
func FromReader(contentType string, r io.Reader) *Attachment {
	return &Attachment{
		contentType: contentType,
		reader:      r,
		consumed:    false,
	}
}

// FromBytes creates an attachment from raw bytes.
func FromBytes(contentType string, data []byte) *Attachment {
	return FromReader(contentType, bytes.NewReader(data))
}

// FromFile reads a file and creates an attachment.
// The file is read into memory and closed before returning.
func FromFile(contentType string, path string) (*Attachment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return FromBytes(contentType, data), nil
}

// FromURL fetches a URL and creates an attachment.
// Content type is derived from the Content-Type header.
// Returns an error if the request fails or status is not 200 OK.
func FromURL(url string) (*Attachment, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", url, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status %d", url, resp.StatusCode)
	}

	// Get content type from response header
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Read the response body into memory
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", url, err)
	}

	return FromBytes(contentType, data), nil
}

// Base64URL returns the attachment as a data URL string.
// Format: "data:image/jpeg;base64,..."
func (a *Attachment) Base64URL() (string, error) {
	if a.consumed {
		return "", fmt.Errorf("attachment already consumed")
	}
	a.consumed = true

	// Stream encode the data
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("data:%s;base64,", a.contentType))

	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	_, err := io.Copy(encoder, a.reader)
	if err != nil {
		return "", fmt.Errorf("failed to encode data: %w", err)
	}

	err = encoder.Close()
	if err != nil {
		return "", fmt.Errorf("failed to finalize encoding: %w", err)
	}

	return buf.String(), nil
}

// Base64Message returns the attachment in message format for AI providers.
// Returns: {"type": "base64_attachment", "content": "data:..."}
func (a *Attachment) Base64Message() (map[string]string, error) {
	url, err := a.Base64URL()
	if err != nil {
		return nil, err
	}

	return map[string]string{
		"type":    "base64_attachment",
		"content": url,
	}, nil
}
