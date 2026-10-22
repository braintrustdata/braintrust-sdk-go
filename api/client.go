// Package api provides the Braintrust REST facade retained for compatibility.
// New REST integrations should use the public openapi package directly.
package api

import (
	"github.com/braintrustdata/braintrust-sdk-go/api/datasets"
	"github.com/braintrustdata/braintrust-sdk-go/api/experiments"
	"github.com/braintrustdata/braintrust-sdk-go/api/functions"
	"github.com/braintrustdata/braintrust-sdk-go/api/projects"
	"github.com/braintrustdata/braintrust-sdk-go/api/prompts"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// API is the compatibility REST client for Braintrust.
// New REST resources are available through the public openapi package, not this facade.
//
// Deprecated: Use [openapi.Client] or [openapi.ClientWithResponses].
type API struct {
	client *https.Client
}

// Option configures an API client.
//
// Deprecated: Use [openapi.ClientOption] when constructing a generated client.
type Option func(*options)

// options holds configuration for creating an API client.
type options struct {
	apiURL string
	logger logger.Logger
}

// WithAPIURL sets the API URL for the client.
// If not provided, defaults to "https://api.braintrust.dev".
//
// Deprecated: Pass the server URL to [openapi.NewClientWithResponses].
func WithAPIURL(url string) Option {
	return func(o *options) {
		o.apiURL = url
	}
}

// WithLogger sets a custom logger for the client.
// If not provided, no logging will occur.
//
// Deprecated: Configure a logging HTTP transport with [openapi.WithHTTPClient].
func WithLogger(log logger.Logger) Option {
	return func(o *options) {
		o.logger = log
	}
}

// NewClient creates a new Braintrust API client with the given API key and options.
// The apiKey must be non-empty (validated at config level).
//
// Deprecated: Use [openapi.NewClientWithResponses] and configure bearer
// authentication with [openapi.WithRequestEditorFn].
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

// NewWithHTTPSClient creates a new Braintrust API client with the given https.Client.
// This is useful for tests that want to use a custom or VCR-wrapped client.
//
// Deprecated: Use [openapi.NewClientWithResponses] with [openapi.WithHTTPClient].
func NewWithHTTPSClient(client *https.Client) *API {
	return &API{
		client: client,
	}
}

// GeneratedClient bridges compatibility APIs to generated REST operations while
// retaining this client's authentication, logging, and HTTP transport.
// A nil API or transport returns nil.
//
// Deprecated: Construct new clients with [openapi.NewClient] or
// [openapi.NewClientWithResponses]. This bridge remains available to migrate
// callers that already hold an API.
func (a *API) GeneratedClient() *openapi.Client {
	if a == nil {
		return nil
	}
	return https.NewGenerated(a.client)
}

// Projects returns a client for project operations
//
// Deprecated: Use the project operations on [openapi.ClientWithResponses],
// such as [openapi.ClientWithResponses.GetProjectWithResponse].
func (a *API) Projects() *projects.API {
	return projects.New(a.client)
}

// Experiments is used to access the Experiments API
//
// Deprecated: Use the experiment operations on [openapi.ClientWithResponses],
// such as [openapi.ClientWithResponses.GetExperimentWithResponse].
func (a *API) Experiments() *experiments.API {
	return experiments.New(a.client)
}

// Datasets returns a client for dataset operations
//
// Deprecated: Use the dataset operations on [openapi.ClientWithResponses],
// such as [openapi.ClientWithResponses.GetDatasetWithResponse].
func (a *API) Datasets() *datasets.API {
	return datasets.New(a.client)
}

// Functions is used to access the Functions API
//
// Deprecated: Use the function operations on [openapi.ClientWithResponses],
// such as [openapi.ClientWithResponses.GetFunctionWithResponse].
func (a *API) Functions() *functions.API {
	return functions.New(a.client)
}

// Prompts returns a client for reading prompts
//
// Deprecated: Use [openapi.ClientWithResponses.GetPromptWithResponse] or
// [openapi.ClientWithResponses.GetPromptIdWithResponse]. For renderable prompts,
// use braintrust.Client.LoadPrompt instead.
func (a *API) Prompts() *prompts.API {
	return prompts.New(a.client)
}
