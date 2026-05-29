// Package attachmentprocessor scans span attributes for base64-encoded LLM
// attachments and replaces them with Braintrust attachment references after
// uploading the data to object storage.
package attachmentprocessor

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Reference is the JSON-serialisable object that replaces inline base64
// attachment data on a span. Its shape is the cross-SDK Braintrust attachment
// reference format.
type Reference struct {
	Type        string `json:"type"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	Key         string `json:"key"`
}

// NewReference creates a reference with a freshly-generated UUID key.
func NewReference(contentType string) Reference {
	return Reference{
		Type:        "braintrust_attachment",
		ContentType: contentType,
		Filename:    "attachment" + contentTypeToExtension(contentType),
		Key:         uuid.New().String(),
	}
}

// contentTypeToExtension maps a MIME type to a file extension.
func contentTypeToExtension(contentType string) string {
	switch strings.ToLower(contentType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "text/html":
		return ".html"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	default:
		parts := strings.SplitN(contentType, "/", 2)
		if len(parts) == 2 {
			sub := parts[1]
			// Strip parameters and suffixes like ";charset=utf-8" or "-xml".
			if idx := strings.IndexAny(sub, ";-"); idx >= 0 {
				sub = sub[:idx]
			}
			return fmt.Sprintf(".%s", sub)
		}
		return ""
	}
}
