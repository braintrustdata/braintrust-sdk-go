package server

import "encoding/json"

// EvalRequest is the request body for POST /eval.
type EvalRequest struct {
	// Name is the registered evaluator name (required).
	Name string `json:"name"`

	// Data specifies the evaluation dataset (required).
	Data EvalData `json:"data"`

	// ExperimentName overrides the experiment name (optional).
	ExperimentName string `json:"experiment_name,omitempty"`

	// ProjectID overrides the project ID (optional).
	ProjectID string `json:"project_id,omitempty"`

	// Parent specifies the parent span for tracing (optional).
	Parent *ParentInfo `json:"parent,omitempty"`

	// Scores lists remote scorer function references (optional).
	Scores []RemoteScore `json:"scores,omitempty"`
}

// EvalData specifies where evaluation data comes from.
// Exactly one of Data, DatasetID, or DatasetName must be set.
type EvalData struct {
	// Data is an inline array of test cases.
	Data json.RawMessage `json:"data,omitempty"`

	// DatasetID loads a dataset by ID.
	DatasetID string `json:"dataset_id,omitempty"`

	// DatasetName loads a dataset by name (optionally scoped by ProjectName).
	DatasetName string `json:"dataset_name,omitempty"`

	// ProjectName scopes DatasetName lookups (optional).
	ProjectName string `json:"project_name,omitempty"`
}

// ParentInfo specifies parent span context for tracing.
type ParentInfo struct {
	ObjectType      string          `json:"object_type,omitempty"`
	ObjectID        string          `json:"object_id,omitempty"`
	PropagatedEvent json.RawMessage `json:"propagated_event,omitempty"`
}

// RemoteScore references a Braintrust-hosted scorer function.
type RemoteScore struct {
	FunctionID string `json:"function_id"`
	Version    string `json:"version,omitempty"`
}

// Parameters defines the parameter schema for an evaluator, displayed in the Braintrust UI.
type Parameters struct {
	Schema map[string]ParameterDef `json:"schema"`
}

// ParameterDef defines a single parameter for the Braintrust UI.
type ParameterDef struct {
	Type        string `json:"type"`
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// listResponse is the response for GET/POST /list.
type listResponse map[string]evalInfo

// evalInfo describes a registered evaluator in the list response.
type evalInfo struct {
	Scores     []scoreInfo     `json:"scores"`
	Parameters *parametersMeta `json:"parameters,omitempty"`
}

// scoreInfo describes a scorer in the list response.
type scoreInfo struct {
	Name string `json:"name"`
}

// parametersMeta wraps parameters with the protocol-required metadata.
type parametersMeta struct {
	Type   string                  `json:"type"`
	Schema map[string]ParameterDef `json:"schema"`
	Source *string                 `json:"source"`
}

// progressEvent is an SSE progress event sent per evaluation case.
type progressEvent struct {
	ObjectType string `json:"object_type"`
	Name       string `json:"name"`
	Format     string `json:"format"`
	OutputType string `json:"output_type"`
	Event      string `json:"event"`
	Data       any    `json:"data,omitempty"`
	SpanID     string `json:"span_id,omitempty"`
}

// summaryEvent is the final SSE event with aggregated results.
type summaryEvent struct {
	ExperimentID   string             `json:"experiment_id,omitempty"`
	ExperimentName string             `json:"experiment_name,omitempty"`
	ProjectName    string             `json:"project_name,omitempty"`
	ProjectID      string             `json:"project_id,omitempty"`
	ExperimentURL  string             `json:"experiment_url,omitempty"`
	Scores         map[string]float64 `json:"scores"`
}
