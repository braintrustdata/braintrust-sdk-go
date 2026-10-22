package openapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/oapi-codegen/nullable"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func TestGeneratedClientInvoke(t *testing.T) {
	server, options := plainClientOptions(t)
	client, err := openapi.NewClient(server, options...)
	require.NoError(t, err)
	ctx := context.Background()

	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	function := (recordedDecoder{t}).object(client.PutFunctionWithBody(ctx, "application/json", recordedBody(t, map[string]any{
		"project_id": project.ID, "name": "test-prompt", "slug": "test-prompt",
		"function_data": map[string]string{"type": "prompt"},
		"prompt_data": map[string]any{
			"prompt": map[string]any{"type": "chat", "messages": []map[string]string{
				{"role": "system", "content": `You are a helpful assistant. Respond with valid JSON in the format: {"answer": "your answer here"}`},
				{"role": "user", "content": "{{input.question}}"},
			}},
			"options": map[string]any{"model": "gpt-4o-mini", "params": map[string]any{
				"temperature": 0, "max_tokens": 50, "response_format": map[string]string{"type": "json_object"},
			}},
		},
	})))

	response, err := client.PostFunctionIdInvoke(ctx, function.ID, openapi.InvokeApi{
		Input: nullable.NewNullableWithValue[any](map[string]any{"question": "What is 2+2?"}),
	})
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var output struct {
		Answer string `json:"answer"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&output))
	require.Equal(t, "4", output.Answer)
}
