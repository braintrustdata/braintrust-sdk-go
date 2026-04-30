package attachment

// UploadURLParams requests a signed URL for an attachment upload.
type UploadURLParams struct {
	Key         string `json:"key"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	OrgID       string `json:"org_id"`
}

// UploadURLResponse contains signed object-store upload details.
type UploadURLResponse struct {
	SignedURL string            `json:"signedUrl"`
	Headers   map[string]string `json:"headers"`
}

// StatusParams updates attachment upload status.
type StatusParams struct {
	Key    string         `json:"key"`
	OrgID  string         `json:"org_id"`
	Status map[string]any `json:"status"`
}
