package prompt_test

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

// A caller-supplied generated client need not use the SDK's status-checking
// transport. The loader must not interpret an HTTP error object as a prompt.
func TestLoadWithClient_HTTPError(t *testing.T) {
	key := os.Getenv("BRAINTRUST_API_KEY")
	if vcr.GetVCRMode() != vcr.ModeReplay {
		require.NotEmpty(t, key)
	}
	client, err := openapi.NewClient("https://api.braintrust.dev",
		openapi.WithHTTPClient(vcr.NewHTTPClient(t)),
		openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+key)
			return nil
		}))
	require.NoError(t, err)
	loaded, err := prompt.LoadWithClient(context.Background(), client, prompt.LoadOpts{ID: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	require.Nil(t, loaded)
	var httpErr *https.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.StatusCode)
}
