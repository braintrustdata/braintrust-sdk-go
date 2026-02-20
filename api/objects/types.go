// Package objects provides generic object operations across Braintrust object types.
package objects

// FetchParams represents request parameters for object fetch.
type FetchParams struct {
	Limit  int            `json:"limit,omitempty"`
	Filter map[string]any `json:"filter,omitempty"`
	Cursor string         `json:"cursor,omitempty"`
}

// FetchResponse is the response from an object fetch request.
type FetchResponse struct {
	Events  []map[string]any `json:"events"`
	Rows    []map[string]any `json:"rows"`
	Objects []map[string]any `json:"objects"`
	Cursor  string           `json:"cursor"`
}
