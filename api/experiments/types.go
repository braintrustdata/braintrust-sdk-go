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
	Tags           []string
	Metadata       map[string]interface{}
	Update         bool   // If true, allow reusing existing experiment instead of creating new one
	DatasetID      string // Optional dataset ID to link to this experiment
	DatasetVersion string // Optional dataset version
}

// UpdateParams represents parameters for updating an experiment.
// All fields are optional - only provided fields will be updated.
type UpdateParams struct {
	Name           string                 `json:"name,omitempty"`
	Description    *string                `json:"description,omitempty"` // Pointer to distinguish between empty string and not set
	BaseExpID      string                 `json:"base_exp_id,omitempty"`
	DatasetID      string                 `json:"dataset_id,omitempty"`
	DatasetVersion string                 `json:"dataset_version,omitempty"`
	Public         *bool                  `json:"public,omitempty"` // Pointer to distinguish between false and not set
	Tags           []string               `json:"tags,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// ListParams represents parameters for listing experiments
type ListParams struct {
	// ProjectID filters experiments by project
	ProjectID string
	// ProjectName filters by project name
	ProjectName string
	// ExperimentName filters by specific experiment name
	ExperimentName string
	// OrgName filters by organization name
	OrgName string
	// IDs filters search results to a particular set of object IDs
	IDs []string
	// Limit maximum number of objects to return (default 25, max 1000)
	Limit int
	// StartingAfter is a cursor for pagination (fetches records after this ID)
	StartingAfter string
	// EndingBefore is a cursor for pagination (fetches records before this ID)
	EndingBefore string
}

// ListResponse represents a paginated list of experiments
type ListResponse struct {
	Objects []Experiment `json:"objects"`
	// Cursor for pagination to fetch the next page of results
	Cursor string `json:"cursor,omitempty"`
}

// ExperimentEvent represents an event to be inserted into an experiment.
// All fields are optional.
type ExperimentEvent struct {
	// Input is the arguments that uniquely define a test case (JSON serializable).
	Input interface{} `json:"input,omitempty"`
	// Output is the output of your application (JSON serializable).
	Output interface{} `json:"output,omitempty"`
	// Expected is the ground truth value (JSON serializable).
	Expected interface{} `json:"expected,omitempty"`
	// Error is the error that occurred, if any.
	Error interface{} `json:"error,omitempty"`
	// Scores is a dictionary of numeric values (between 0 and 1) to log.
	Scores map[string]float64 `json:"scores,omitempty"`
	// Metadata is additional data about the test example.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	// Tags is a list of tags to log.
	Tags []string `json:"tags,omitempty"`
	// Metrics are numerical measurements tracking execution.
	Metrics map[string]interface{} `json:"metrics,omitempty"`
	// Context is additional context for the event.
	Context map[string]interface{} `json:"context,omitempty"`
	// SpanAttributes are attributes for the span.
	SpanAttributes map[string]interface{} `json:"span_attributes,omitempty"`
	// ID is a unique identifier for the event. If you don't provide one, BT will generate one.
	ID string `json:"id,omitempty"`
	// SpanID is the ID of this span.
	SpanID string `json:"span_id,omitempty"`
	// RootSpanID is the ID of the root span.
	RootSpanID string `json:"root_span_id,omitempty"`
	// SpanParents are the parent span IDs.
	SpanParents []string `json:"span_parents,omitempty"`
}

// InsertEventsRequest is the request body for inserting events.
type InsertEventsRequest struct {
	Events []ExperimentEvent `json:"events"`
}

// InsertEventsResponse is the response from inserting events.
type InsertEventsResponse struct {
	// RowIDs are the IDs of the inserted rows.
	RowIDs []string `json:"row_ids"`
}

// FetchEventsParams represents parameters for fetching experiment events.
type FetchEventsParams struct {
	// Limit controls the number of traces returned (pagination-aware).
	Limit int `json:"limit,omitempty"`
	// Cursor is an opaque pagination cursor for retrieving subsequent result pages.
	Cursor string `json:"cursor,omitempty"`
	// MaxXactID is deprecated; used for manual pagination cursor construction.
	MaxXactID string `json:"max_xact_id,omitempty"`
	// MaxRootSpanID is deprecated; used with max_xact_id for manual pagination.
	MaxRootSpanID string `json:"max_root_span_id,omitempty"`
	// Version retrieves a snapshot from a past point in time.
	Version string `json:"version,omitempty"`
}

// FetchEventsResponse is the response from fetching experiment events.
type FetchEventsResponse struct {
	// Events is the list of fetched events.
	Events []ExperimentEvent `json:"events"`
	// Cursor for pagination to fetch the next page of results.
	Cursor string `json:"cursor,omitempty"`
}

// SummarizeParams represents parameters for summarizing an experiment.
type SummarizeParams struct {
	// SummarizeScores determines whether to summarize the scores and metrics.
	// If false (or omitted), only metadata will be returned.
	SummarizeScores bool
	// ComparisonExperimentID specifies a baseline experiment for comparison.
	// Falls back to base_exp_id metadata or the most recent project experiment if omitted.
	ComparisonExperimentID string
}

// SummarizeResponse is the response from summarizing an experiment.
type SummarizeResponse struct {
	ProjectName              string                   `json:"project_name"`
	ProjectID                string                   `json:"project_id,omitempty"`
	ExperimentName           string                   `json:"experiment_name"`
	ExperimentID             string                   `json:"experiment_id,omitempty"`
	ExperimentURL            string                   `json:"experiment_url,omitempty"`
	ProjectURL               string                   `json:"project_url,omitempty"`
	ComparisonExperimentName string                   `json:"comparison_experiment_name,omitempty"`
	Scores                   map[string]ScoreSummary  `json:"scores,omitempty"`
	Metrics                  map[string]MetricSummary `json:"metrics,omitempty"`
}

// ScoreSummary contains summary statistics for a score.
type ScoreSummary struct {
	Name         string  `json:"name"`
	Score        float64 `json:"score"`          // Average score (0-1)
	Diff         float64 `json:"diff,omitempty"` // Difference from comparison
	Improvements int     `json:"improvements,omitempty"`
	Regressions  int     `json:"regressions,omitempty"`
}

// MetricSummary contains summary statistics for a metric.
type MetricSummary struct {
	Name         string  `json:"name"`
	Metric       float64 `json:"metric"`
	Unit         string  `json:"unit,omitempty"`
	Diff         float64 `json:"diff,omitempty"` // Difference from comparison
	Improvements int     `json:"improvements,omitempty"`
	Regressions  int     `json:"regressions,omitempty"`
}
