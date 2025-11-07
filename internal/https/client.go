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

// Client is a unified HTTP client for API requests.
type Client struct {
	apiKey     string
	apiURL     string
	httpClient *http.Client
	logger     logger.Logger
}

// NewClient creates a new HTTP client with the given credentials.
func NewClient(apiKey, apiURL string, log logger.Logger) *Client {
	if log == nil {
		log = logger.Discard()
	}

	return &Client{
		apiKey: apiKey,
		apiURL: apiURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: log,
	}
}

// GET makes a GET request with query parameters.
func (c *Client) GET(ctx context.Context, path string, params map[string]string) (*http.Response, error) {
	fullURL := c.apiURL + path

	// Add query parameters if provided
	if len(params) > 0 {
		urlValues := url.Values{}
		for k, v := range params {
			urlValues.Add(k, v)
		}
		fullURL = fullURL + "?" + urlValues.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	return c.doRequest(req)
}

// POST makes a POST request with a JSON body.
func (c *Client) POST(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("error marshaling request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)

		c.logger.Debug("http request body", "body", string(jsonData))
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.doRequest(req)
}

// DELETE makes a DELETE request.
func (c *Client) DELETE(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.apiURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	return c.doRequest(req)
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

		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}
