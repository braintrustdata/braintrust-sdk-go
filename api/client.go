// Package api provides a client for interacting with the Braintrust API.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// API is the main API client for Braintrust.
type API struct {
	client *https.Client
}

// Option configures an API client.
type Option func(*options)

// options holds configuration for creating an API client.
type options struct {
	apiURL string
	logger logger.Logger
}

// WithAPIURL sets the API URL for the client.
// If not provided, defaults to "https://api.braintrust.dev".
func WithAPIURL(url string) Option {
	return func(o *options) {
		o.apiURL = url
	}
}

// WithLogger sets a custom logger for the client.
// If not provided, no logging will occur.
func WithLogger(log logger.Logger) Option {
	return func(o *options) {
		o.logger = log
	}
}

// NewClient creates a new Braintrust API client with the given API key and options.
func NewClient(apiKey string, opts ...Option) (*API, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("apiKey is required")
	}

	options := &options{
		apiURL: "https://api.braintrust.dev", // default
		logger: nil,
	}

	for _, opt := range opts {
		opt(options)
	}

	client, err := https.NewClient(apiKey, options.apiURL, options.logger)
	if err != nil {
		return nil, err
	}

	return &API{
		client: client,
	}, nil
}

// doRequest makes an HTTP request with authentication
func (a *API) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	return a.doRequestWithParams(ctx, method, path, body, nil)
}

// doRequestWithParams makes an HTTP request with query parameters (for GET) or body (for POST/etc)
func (a *API) doRequestWithParams(ctx context.Context, method, path string, body interface{}, params url.Values) (*http.Response, error) {
	// Convert url.Values to map[string]string for the https client
	var paramsMap map[string]string
	if params != nil {
		paramsMap = make(map[string]string)
		for key, values := range params {
			if len(values) > 0 {
				paramsMap[key] = values[0] // Use first value
			}
		}
	}

	switch method {
	case "GET":
		return a.client.GET(ctx, path, paramsMap)
	case "POST":
		return a.client.POST(ctx, path, body)
	case "DELETE":
		return a.client.DELETE(ctx, path)
	default:
		return a.client.POST(ctx, path, body)
	}
}

// Projects returns a client for project operations
func (a *API) Projects() *ProjectsClient {
	return &ProjectsClient{client: a}
}

// Experiments returns a client for experiment operations
func (a *API) Experiments() *ExperimentsClient {
	return &ExperimentsClient{client: a}
}

// Datasets returns a client for dataset operations
func (a *API) Datasets() *DatasetsClient {
	return &DatasetsClient{client: a}
}

// Functions returns a client for function operations
func (a *API) Functions() *FunctionsClient {
	return &FunctionsClient{client: a}
}
