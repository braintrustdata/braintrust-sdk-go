package prompt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

func TestLoad_LegacyCompatibility(t *testing.T) {
	transport := vcr.GetHTTPSClient(t)
	ensureTestPrompt(t, https.NewGenerated(transport))
	p, err := prompt.Load(context.Background(), api.NewWithHTTPSClient(transport), prompt.LoadOpts{
		Slug: testSlug, Project: testProject,
	})
	require.NoError(t, err)
	assert.Equal(t, testSlug, p.Slug)
	assert.Equal(t, "gpt-4o-mini", p.Model())

	_, err = prompt.Load(context.Background(), nil, prompt.LoadOpts{Slug: testSlug, Project: testProject})
	require.Error(t, err)
}
