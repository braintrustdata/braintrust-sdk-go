// Package functions provides operations for managing Braintrust functions (prompts, tools, scorers).
package functions

import (
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
)

// API provides methods for interacting with functions.
type API struct {
	client *https.Client
}

// Function represents a Braintrust function (prompt, tool, or scorer).
type Function struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	FunctionType string `json:"function_type"`
	Description  string `json:"description,omitempty"`
}

// QueryParams contains options for querying functions.
type QueryParams struct {
	// Project identity (either/or)
	ProjectName string // Filter by project name
	ProjectID   string // Filter by specific project ID

	// Function identity (either/or)
	Slug         string // Filter by function slug
	FunctionName string // Filter by function name

	// Query modifiers
	Version     string // Specific function version
	Environment string // Environment to load (dev/staging/production)
	Limit       int    // Max results (default: no limit)
}

// CreateParams represents the request payload for creating a function.
type CreateParams struct {
	ProjectID    string         `json:"project_id"`
	Name         string         `json:"name"`
	Slug         string         `json:"slug"`
	FunctionType string         `json:"function_type,omitempty"`
	FunctionData map[string]any `json:"function_data"`
	PromptData   map[string]any `json:"prompt_data,omitempty"`
	Description  string         `json:"description,omitempty"`
}

// InvokeParams represents the request payload for invoking a function.
type InvokeParams struct {
	Input any `json:"input"`
}

// QueryResponse represents the response from querying functions.
type QueryResponse struct {
	Objects []Function `json:"objects"`
}
