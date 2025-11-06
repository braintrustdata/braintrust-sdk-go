// Package projects provides operations for managing Braintrust projects.
package projects

// Project represents a project in Braintrust.
type Project struct {
	// ID is the unique identifier for the project.
	ID string `json:"id"`

	// Name is the human-readable name of the project.
	Name string `json:"name"`

	// OrgID is the organization ID that owns this project.
	OrgID string `json:"org_id,omitempty"`
}

// CreateParams contains parameters for creating a project.
type CreateParams struct {
	// Name is the name of the project (required).
	Name string `json:"name"`

	// OrgID optionally specifies which organization to create the project under.
	// If empty, uses the default organization for the API key.
	OrgID string `json:"org_id,omitempty"`
}

// ListParams contains parameters for listing projects.
type ListParams struct {
	// OrgID filters projects by organization ID.
	OrgID string

	// Limit is the maximum number of projects to return.
	Limit int
}

// ListResponse represents the response from listing projects.
type ListResponse struct {
	// Objects is the list of projects returned.
	Objects []Project `json:"objects"`
}
