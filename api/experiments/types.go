// Package experiments provides operations for managing Braintrust experiments.
package experiments

import "github.com/braintrustdata/braintrust-sdk-go/internal/https"

// API provides methods for experiment operations
type API struct {
	client *https.Client
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
	Tags     []string
	Metadata map[string]interface{}
	Update   bool // If true, allow reusing existing experiment instead of creating new one
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
