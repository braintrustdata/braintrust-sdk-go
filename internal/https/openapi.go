package https

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// OpenAPIClient uses generated endpoints while retaining SDK authentication, errors,
// timeouts, logging, and the caller's configured HTTP client.
type OpenAPIClient struct {
	openapi.Client
	transport *Client
}

// NewOpenAPIClient adapts an existing transport without changing its configuration.
// A nil transport stays nil so facade validation can run before any I/O.
func NewOpenAPIClient(transport *Client) *OpenAPIClient {
	if transport == nil {
		return nil
	}
	client := &OpenAPIClient{
		Client: openapi.Client{
			Server: strings.TrimRight(transport.BaseURL(), "/") + "/",
		},
		transport: transport,
	}
	client.Client.Client = client
	return client
}

// NewGenerated connects generated REST operations to the configured SDK transport.
// A nil transport returns nil.
func NewGenerated(transport *Client) *openapi.Client {
	client := NewOpenAPIClient(transport)
	if client == nil {
		return nil
	}
	return &client.Client
}

// Do sends generated requests unchanged through the shared SDK transport.
func (c *OpenAPIClient) Do(req *http.Request) (*http.Response, error) {
	return c.transport.Do(req)
}

// JSONBody preserves public request models and arbitrary JSON without converting
// through generated models. Generated WithBody methods accept the encoded body.
func (c *OpenAPIClient) JSONBody(value any) (io.Reader, error) {
	return c.transport.JSONBody(value)
}

// String represents an optional nonempty query parameter.
func String(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Positive represents an optional positive query limit.
func Positive(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

// Query preserves a public query parameter missing from the pinned specification.
// Generated parameters are retained; an empty value is omitted.
func Query(name, value string) openapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if value != "" {
			query := req.URL.Query()
			query.Set(name, value)
			req.URL.RawQuery = query.Encode()
		}
		return nil
	}
}
