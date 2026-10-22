// Package experiments provides operations for managing Braintrust experiments.
//
// New REST integrations should use github.com/braintrustdata/braintrust-sdk-go/openapi instead.
package experiments

import "github.com/braintrustdata/braintrust-sdk-go/internal/https"

// API provides methods for experiment operations
//
// Deprecated: Use the openapi package's Client or ClientWithResponses.
type API struct {
	client *https.OpenAPIClient
}

// Experiment represents an experiment from the API
type Experiment struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	ProjectID      string                 `json:"project_id"`
	Description    string                 `json:"description,omitempty"`
	BaseExpID      string                 `json:"base_exp_id,omitempty"`
	DatasetID      string                 `json:"dataset_id,omitempty"`
	DatasetVersion string                 `json:"dataset_version,omitempty"`
	Public         bool                   `json:"public,omitempty"`
	Tags           []string               `json:"tags,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// CreateParams represents parameters for creating an experiment
type CreateParams struct {
	ProjectID      string                 `json:"project_id"`
	Name           string                 `json:"name,omitempty"`
	Description    string                 `json:"description,omitempty"`
	BaseExpID      string                 `json:"base_exp_id,omitempty"`
	DatasetID      string                 `json:"dataset_id,omitempty"`
	DatasetVersion string                 `json:"dataset_version,omitempty"`
	Public         bool                   `json:"public,omitempty"`
	EnsureNew      bool                   `json:"ensure_new,omitempty"`
	Tags           []string               `json:"tags,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// RegisterOpts contains optional parameters for registering an experiment.
// This is a convenience wrapper that provides backward compatibility.
type RegisterOpts struct {
	Tags           []string
	Metadata       map[string]interface{}
	Update         bool   // If true, allow reusing existing experiment instead of creating new one
	DatasetID      string // Optional dataset ID to link to this experiment
	DatasetVersion string // Optional dataset version
}

// ListParams represents parameters for listing experiments
type ListParams struct {
	// ProjectID filters experiments by project
	ProjectID string
	// ExperimentName filters by specific experiment name
	ExperimentName string
	// OrgName filters by organization name
	OrgName string
	// Limit maximum number of objects to return (default 25, max 1000)
	Limit int
}

// ListResponse represents a paginated list of experiments
type ListResponse struct {
	Objects []Experiment `json:"objects"`
}

// Event is an experiment row to insert. Set IsMerge to update fields of an
// existing row with the same ID instead of replacing it.
type Event struct {
	ID              string                      `json:"id,omitempty"`
	Input           any                         `json:"input,omitempty"`
	Output          any                         `json:"output,omitempty"`
	Expected        any                         `json:"expected,omitempty"`
	Error           any                         `json:"error,omitempty"`
	Scores          map[string]float64          `json:"scores,omitempty"`
	Metadata        map[string]any              `json:"metadata,omitempty"`
	Tags            []string                    `json:"tags,omitempty"`
	Metrics         map[string]float64          `json:"metrics,omitempty"`
	Classifications map[string][]map[string]any `json:"classifications,omitempty"`
	DatasetID       string                      `json:"dataset_id,omitempty"`
	DatasetRecordID string                      `json:"dataset_record_id,omitempty"`

	// Tracing fields
	SpanID         string         `json:"span_id,omitempty"`
	RootSpanID     string         `json:"root_span_id,omitempty"`
	SpanParents    []string       `json:"span_parents,omitempty"`
	SpanAttributes map[string]any `json:"span_attributes,omitempty"`

	// Merge controls
	IsMerge    *bool      `json:"_is_merge,omitempty"`
	MergePaths [][]string `json:"_merge_paths,omitempty"`
}
