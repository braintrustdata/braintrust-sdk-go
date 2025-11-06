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
	ID       string      `json:"id,omitempty"`
	Input    interface{} `json:"input"`
	Expected interface{} `json:"expected,omitempty"`
	Metadata interface{} `json:"metadata,omitempty"`
	Tags     []string    `json:"tags,omitempty"`
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
