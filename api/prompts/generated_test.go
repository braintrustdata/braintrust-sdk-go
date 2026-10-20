package prompts

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func TestCompatibilityPromptVersionPrecedence(t *testing.T) {
	for _, operation := range []string{"query", "get"} {
		t.Run(operation, func(t *testing.T) {
			client := New(https.NewClient("unused", "https://example.invalid", nil))
			stop := errors.New("inspect generated request before network I/O")
			// Exercise the real request builder using its request-editor hook;
			// no server response or SDK client is mocked.
			client.client.RequestEditors = []openapi.RequestEditorFn{func(_ context.Context, req *http.Request) error {
				require.Equal(t, "1000192656880881099", req.URL.Query().Get("version"))
				require.False(t, req.URL.Query().Has("environment"))
				return stop
			}}
			params := QueryParams{Version: "1000192656880881099", Environment: "production"}
			var err error
			if operation == "query" {
				_, err = client.Query(context.Background(), params)
			} else {
				_, err = client.Get(context.Background(), "opaque-id", params)
			}
			require.ErrorIs(t, err, stop)
		})
	}
}
