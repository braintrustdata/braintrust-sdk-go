package openapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// plainClientOptions configures a standalone generated client, without the SDK
// transport, against the test's VCR cassette.
func plainClientOptions(t *testing.T) (string, []openapi.ClientOption) {
	t.Helper()
	transport := vcr.GetHTTPSClient(t)
	return transport.BaseURL(), []openapi.ClientOption{
		openapi.WithHTTPClient(transport.Client()),
		openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+transport.APIKey())
			return nil
		}),
	}
}

func TestGeneratedClientProject(t *testing.T) {
	server, options := plainClientOptions(t)
	client, err := openapi.NewClientWithResponses(server, options...)
	require.NoError(t, err)

	created, err := client.PostProjectWithResponse(context.Background(), openapi.CreateProject{Name: "go-sdk-tests"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, created.StatusCode())
	require.NotNil(t, created.JSON200)
	require.NotEmpty(t, created.JSON200.Id.String())
	require.NotEmpty(t, created.JSON200.OrgId.String())
	require.Equal(t, "go-sdk-tests", created.JSON200.Name)

	fetched, err := client.GetProjectIdWithResponse(context.Background(), created.JSON200.Id.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fetched.StatusCode())
	require.NotNil(t, fetched.JSON200)
	require.Equal(t, created.JSON200.Id, fetched.JSON200.Id)
	require.Equal(t, created.JSON200.OrgId, fetched.JSON200.OrgId)
	require.Equal(t, created.JSON200.Name, fetched.JSON200.Name)
}

func TestGeneratedNullableProjectPatch(t *testing.T) {
	for _, input := range []string{`{}`, `{"description":null}`, `{"description":""}`} {
		t.Run(input, func(t *testing.T) {
			var patch openapi.PatchProject
			require.NoError(t, json.Unmarshal([]byte(input), &patch))
			output, err := json.Marshal(patch)
			require.NoError(t, err)
			require.JSONEq(t, input, string(output))
		})
	}
}

func TestGeneratedScoreTypeFilter(t *testing.T) {
	var filter openapi.ProjectScoreTypeFilter
	require.NoError(t, filter.FromProjectScoreType("slider"))
	output, err := json.Marshal(filter)
	require.NoError(t, err)
	require.JSONEq(t, `"slider"`, string(output))
	value, err := filter.AsProjectScoreType()
	require.NoError(t, err)
	require.Equal(t, openapi.ProjectScoreType("slider"), value)

	// Replacing the scalar with a list must not retain the old union value.
	values := openapi.ProjectScoreTypeList{"slider", "categorical"}
	require.NoError(t, filter.FromProjectScoreTypeList(values))
	output, err = json.Marshal(filter)
	require.NoError(t, err)
	require.JSONEq(t, `["slider","categorical"]`, string(output))
	var decoded openapi.ProjectScoreTypeFilter
	require.NoError(t, json.Unmarshal(output, &decoded))
	list, err := decoded.AsProjectScoreTypeList()
	require.NoError(t, err)
	require.Equal(t, values, list)
	_, err = decoded.AsProjectScoreType()
	require.Error(t, err)
}

func TestGeneratedAdditionalProperties(t *testing.T) {
	// The spec permits arbitrary JSON alongside the known span attributes.
	// Null must remain a present map entry, distinct from an absent entry.
	input := `{"name":"operation","custom":null,"count":0,"nested":{"enabled":true}}`
	var attributes openapi.SpanAttributes
	require.NoError(t, json.Unmarshal([]byte(input), &attributes))
	value, found := attributes.Get("custom")
	require.True(t, found)
	require.Nil(t, value)
	_, found = attributes.Get("missing")
	require.False(t, found)
	output, err := json.Marshal(attributes)
	require.NoError(t, err)
	require.JSONEq(t, input, string(output))
}
