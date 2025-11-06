package datasets

import (
	"encoding/json"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides methods for interacting with datasets.
type API struct {
	client *https.Client
}

// Dataset represents a dataset resource from the Braintrust API.
type Dataset struct {
	ID          string                 `json:"id"`
	ProjectID   string                 `json:"project_id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Event represents a single event/record in a dataset.
// Events contain input data, optional expected output, and metadata.
type Event struct {
	// Core data fields
	ID       string      `json:"id,omitempty"`
	Input    interface{} `json:"input,omitempty"`
	Expected interface{} `json:"expected,omitempty"`
	Metadata interface{} `json:"metadata,omitempty"`
	Tags     []string    `json:"tags,omitempty"`

	// System fields (returned by API, typically not set on insert)
	XactID        string `json:"_xact_id,omitempty"`
	Created       string `json:"created,omitempty"`
	PaginationKey string `json:"_pagination_key,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	DatasetID     string `json:"dataset_id,omitempty"`

	// Tracing fields
	SpanID      string   `json:"span_id,omitempty"`
	RootSpanID  string   `json:"root_span_id,omitempty"`
	SpanParents []string `json:"span_parents,omitempty"`
	IsRoot      *bool    `json:"is_root,omitempty"`
	ParentID    string   `json:"_parent_id,omitempty"` // Deprecated
	Origin      *Origin  `json:"origin,omitempty"`

	// Merge and deletion controls (for insert)
	IsMerge      *bool      `json:"_is_merge,omitempty"`
	MergePaths   [][]string `json:"_merge_paths,omitempty"`
	ObjectDelete *bool      `json:"_object_delete,omitempty"`
}

// Origin indicates the event was copied from another object
type Origin struct {
	ObjectType string `json:"object_type,omitempty"`
	ObjectID   string `json:"object_id,omitempty"`
}

// CreateParams contains parameters for creating a dataset.
type CreateParams struct {
	ProjectID   string                 `json:"project_id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// InsertParams contains parameters for inserting events into a dataset.
type InsertParams struct {
	Events []Event `json:"events"`
}

// QueryParams contains parameters for querying datasets.
type QueryParams struct {
	// ID filters by dataset ID
	ID string

	// Name filters by dataset name (requires ProjectName or ProjectID)
	Name string

	// Version filters by dataset version
	Version string

	// ProjectID filters by project ID
	ProjectID string

	// ProjectName filters by project name
	ProjectName string

	// Limit limits the number of datasets returned
	Limit int

	// StartingAfter is a cursor for pagination (forward)
	StartingAfter string

	// EndingBefore is a cursor for pagination (backward)
	EndingBefore string
}

// QueryResponse represents the response from querying datasets.
type QueryResponse struct {
	Objects []Dataset `json:"objects"`
}

// FetchResponse represents a paginated response from the fetch endpoint.
type FetchResponse struct {
	Events []json.RawMessage `json:"events"`
	Cursor string            `json:"cursor"`
}
