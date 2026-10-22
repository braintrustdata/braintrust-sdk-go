package prompt

import (
	"context"

	"github.com/braintrustdata/braintrust-sdk-go/api"
)

// Load fetches a prompt using the legacy API client. It is retained for source
// compatibility.
//
// Deprecated: Use [LoadWithClient] with a generated client, or
// braintrust.Client.LoadPrompt for SDK-managed configuration.
func Load(ctx context.Context, client *api.API, opts LoadOpts) (*Prompt, error) {
	if client == nil {
		return LoadWithClient(ctx, nil, opts)
	}
	return LoadWithClient(ctx, client.GeneratedClient(), opts)
}
