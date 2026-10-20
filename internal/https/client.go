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

// APIKey returns the API key used by this client.
func (c *Client) APIKey() string {
	return c.apiKey
}

// BaseURL returns the configured server URL, including any deployment prefix.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// JSONBody serializes a request body. Do logs it when the request is sent.
func (c *Client) JSONBody(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}
	return bytes.NewReader(data), nil
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

	return c.Do(req)
}

// POST makes a POST request with a JSON body.
// The path is appended to the base URL (e.g., "/api/apikey/login").
func (c *Client) POST(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL: %w", err)
	}

	reqBody, err := c.JSONBody(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.Do(req)
}

// Client returns the underlying http.Client.
// This is useful for extracting the client for auth.Session when using VCR.
func (c *Client) Client() *http.Client {
	return c.httpClient
}

// Do executes the HTTP request with auth, error checking, and logging.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Add auth header
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Log request
	start := time.Now()
	c.logger.Debug("http request",
		"method", req.Method,
		"url", req.URL.String())
	c.logRequestBody(req)

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
	if err := CheckResponse(resp); err != nil {
		_ = resp.Body.Close()
		c.logger.Debug("http error response",
			"method", req.Method,
			"url", req.URL.String(),
			"status", resp.StatusCode,
			"error", err)
		return nil, err
	}

	return resp, nil
}

// logRequestBody logs replayable request bodies, which covers both SDK and
// generated operations, without consuming the body that will be sent.
func (c *Client) logRequestBody(req *http.Request) {
	if req.GetBody == nil || req.ContentLength == 0 {
		return
	}
	body, err := req.GetBody()
	if err != nil {
		return
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return
	}
	c.logger.Debug("http request body", "body", string(data))
}

// DecodeJSON closes a response, rejects non-2xx statuses, and decodes the
// successful body. Pass a generated operation's results directly:
//
//	rows, err := https.DecodeJSON[rowList](client.GetDataset(ctx, params))
func DecodeJSON[T any](resp *http.Response, err error) (T, error) {
	var value T
	if err != nil {
		return value, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := CheckResponse(resp); err != nil {
		return value, err
	}
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return value, fmt.Errorf("error decoding response: %w", err)
	}
	return value, nil
}

// CheckAndClose closes a response whose body is not needed and rejects
// non-2xx statuses.
func CheckAndClose(resp *http.Response, err error) error {
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return CheckResponse(resp)
}

// CheckResponse returns an HTTPError for non-success status codes, consuming the
// error body. It leaves successful bodies untouched. The caller owns Body.Close.
// SDK loaders also use this with plain generated clients, whose transports need
// not reject non-2xx responses.
func CheckResponse(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body, readErr := io.ReadAll(resp.Body)
	err := fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	if readErr != nil {
		err = fmt.Errorf("unexpected status code: %d, reading response body: %w", resp.StatusCode, readErr)
	}
	return &HTTPError{StatusCode: resp.StatusCode, Body: string(body), err: err}
}
