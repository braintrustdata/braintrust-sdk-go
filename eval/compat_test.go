package eval

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func TestNewEvaluatorCompatibility(t *testing.T) {
	ctx := context.Background()
	transport := vcr.GetHTTPSClient(t)
	generated := https.NewGenerated(transport)

	// Own the fixture so re-recording another test cannot invalidate its ID.
	project, err := decodeFixtureResponse(generated.PostProject(ctx, openapi.CreateProject{Name: integrationTestProject}))
	require.NoError(t, err)
	fixture, err := decodeFixtureResponse(generated.PostDatasetWithBody(ctx, "application/json", fixtureBody(t, map[string]any{
		"project_id": project.ID,
		"name":       "test-new-evaluator-compatibility",
	})))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, closeFixtureResponse(generated.DeleteDatasetId(ctx, fixture.ID)))
	})
	events := []fixtureDatasetEvent{
		{Input: testDatasetInput{Question: "What is 2+2?"}, Expected: testDatasetOutput{Answer: "4"}},
		{Input: testDatasetInput{Question: "What is the capital of France?"}, Expected: testDatasetOutput{Answer: "Paris"}},
	}
	require.NoError(t, closeFixtureResponse(generated.PostDatasetIdInsertWithBody(ctx, fixture.ID, "application/json", fixtureBody(t, map[string]any{"events": events}))))

	legacy := api.NewWithHTTPSClient(transport)
	evaluator := NewEvaluator[testDatasetInput, testDatasetOutput](nil, nil, legacy, "")
	dataset, err := evaluator.Datasets().Get(ctx, fixture.ID)
	require.NoError(t, err)
	answers := map[string]string{}
	for {
		row, err := dataset.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		answers[row.Input.Question] = row.Expected.Answer
	}
	require.Equal(t, map[string]string{
		"What is 2+2?":                   "4",
		"What is the capital of France?": "Paris",
	}, answers)
}
