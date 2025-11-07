// Package api provides a client for interacting with the Braintrust API.
package api

import (
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
	"github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
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
// The apiKey must be non-empty (validated at config level).
func NewClient(apiKey string, opts ...Option) *API {
	options := &options{
		apiURL: "https://api.braintrust.dev", // default
		logger: nil,
	}

	for _, opt := range opts {
		opt(options)
	}

	client := https.NewClient(apiKey, options.apiURL, options.logger)

	return &API{
		client: client,
	}
}

// Projects returns a client for project operations
func (a *API) Projects() *projects.API {
	return projects.New(a.client)
}

// Experiments is used to access the Experiments API
func (a *API) Experiments() *experiments.API {
	return experiments.New(a.client)
}

// Datasets returns a client for dataset operations
func (a *API) Datasets() *datasets.API {
	return datasets.New(a.client)
}

// Functions is used to access the Functions API
func (a *API) Functions() *functions.API {
	return functions.New(a.client)
}
