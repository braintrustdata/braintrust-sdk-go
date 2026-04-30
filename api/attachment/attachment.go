// Package attachment provides operations for managing Braintrust attachments.
package attachment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides attachment API operations.
type API struct {
	client *https.Client
}

// New creates a new attachment API client.
func New(client *https.Client) *API {
	return &API{client: client}
}

// RequestUploadURL requests a signed object-store URL for an attachment upload.
func (a *API) RequestUploadURL(ctx context.Context, params UploadURLParams) (*UploadURLResponse, error) {
	if params.Key == "" {
		return nil, fmt.Errorf("attachment key is required")
	}
	if params.Filename == "" {
		return nil, fmt.Errorf("attachment filename is required")
	}
	if params.ContentType == "" {
		return nil, fmt.Errorf("attachment content type is required")
	}
	if params.OrgID == "" {
		return nil, fmt.Errorf("org ID is required")
	}

	resp, err := a.client.POST(ctx, "/attachment", params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result UploadURLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}
	if result.SignedURL == "" {
		return nil, fmt.Errorf("signed URL response missing signedUrl")
	}
	if result.Headers == nil {
		result.Headers = map[string]string{}
	}

	return &result, nil
}

// UpdateStatus updates attachment upload status.
func (a *API) UpdateStatus(ctx context.Context, params StatusParams) error {
	if params.Key == "" {
		return fmt.Errorf("attachment key is required")
	}
	if params.OrgID == "" {
		return fmt.Errorf("org ID is required")
	}
	if params.Status == nil {
		return fmt.Errorf("attachment status is required")
	}

	resp, err := a.client.POST(ctx, "/attachment/status", params)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}
