// Package https provides a unified HTTP client for making API requests
// with centralized auth, error handling, and debug logging.
package https

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// HTTPError represents an HTTP error response with status code.
type HTTPError struct {
	StatusCode int
	Body       string
	err        error
}

func (e *HTTPError) Error() string {
	return e.err.Error()
}

func (e *HTTPError) Unwrap() error {
	return e.err
}

// Client is a unified HTTP client for API requests.
type Client struct {
	apiKey     string
	baseURL    string // Base URL (e.g., apiURL or appURL)
	httpClient *http.Client
	logger     logger.Logger
}

// NewClient creates a new HTTP client with the given credentials and base URL.
// The baseURL parameter is the base URL (e.g., "https://api.braintrust.dev" or "https://www.braintrust.dev").
func NewClient(apiKey, baseURL string, log logger.Logger) *Client {
	if log == nil {
		log = logger.Discard()
	}

	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: log,
	}
}

// NewWrappedClient creates a new HTTP client with a custom http.Client.
// This is useful for tests that need to wrap the HTTP client (e.g., with VCR).
func NewWrappedClient(apiKey, baseURL string, httpClient *http.Client, log logger.Logger) *Client {
	if log == nil {
		log = logger.Discard()
	}

	return &Client{
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: httpClient,
		logger:     log,
	}
}

// GET makes a GET request with query parameters.
// The path is appended to the base URL (e.g., "/v1/project").
func (c *Client) GET(ctx context.Context, path string, params map[string]string) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL: %w", err)
	}

	// Add query parameters if provided
	if len(params) > 0 {
		urlValues := url.Values{}
		for k, v := range params {
			urlValues.Add(k, v)
		}
		u = u + "?" + urlValues.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	return c.doRequest(req)
}

// POST makes a POST request with a JSON body.
// The path is appended to the base URL (e.g., "/api/apikey/login").
func (c *Client) POST(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL: %w", err)
	}

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("error marshaling request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)

		c.logger.Debug("http request body", "body", string(jsonData))
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.doRequest(req)
}

// DELETE makes a DELETE request.
// The path is appended to the base URL (e.g., "/v1/function/123").
func (c *Client) DELETE(ctx context.Context, path string) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	return c.doRequest(req)
}

// Client returns the underlying http.Client.
// This is useful for extracting the client for auth.Session when using VCR.
func (c *Client) Client() *http.Client {
	return c.httpClient
}

// doRequest executes the HTTP request with auth, error checking, and logging.
func (c *Client) doRequest(req *http.Request) (*http.Response, error) {
	// Add auth header
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Log request
	start := time.Now()
	c.logger.Debug("http request",
		"method", req.Method,
		"url", req.URL.String())

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("http request failed",
			"method", req.Method,
			"url", req.URL.String(),
			"error", err,
			"duration", time.Since(start))
		return nil, fmt.Errorf("error making request: %w", err)
	}

	// Log response
	c.logger.Debug("http response",
		"method", req.Method,
		"url", req.URL.String(),
		"status", resp.StatusCode,
		"duration", time.Since(start))

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		c.logger.Debug("http error response",
			"method", req.Method,
			"url", req.URL.String(),
			"status", resp.StatusCode,
			"body", string(body))

		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			err:        fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body)),
		}
	}

	return resp, nil
}
