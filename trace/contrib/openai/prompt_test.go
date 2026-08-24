package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

func TestChatCompletionParams(t *testing.T) {
	p := prompt.FromData("summarizer", prompt.Definition{
		Model: "gpt-4o-mini",
		Messages: []prompt.Message{
			prompt.System("You summarize articles."),
			prompt.User("Summarize:\n{{input}}"),
		},
		Params: map[string]any{"temperature": 0.5, "max_tokens": 128},
		Tools: []prompt.Tool{{
			Type: "function",
			Function: prompt.ToolFunction{
				Name:        "lookup",
				Description: "Look something up",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
	}.Data())

	built, err := p.Build(map[string]any{"input": "a long article"})
	require.NoError(t, err)

	params, err := ChatCompletionParams(built)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o-mini", string(params.Model))
	require.Len(t, params.Messages, 2)
	assert.Equal(t, 0.5, params.Temperature.Value)
	assert.Equal(t, int64(128), params.MaxTokens.Value)
	require.Len(t, params.Tools, 1)
	assert.Equal(t, "lookup", params.Tools[0].Function.Name)

	// The rendered text has to survive into the request body.
	encoded, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "Summarize:\\na long article")
	assert.Contains(t, string(encoded), "You summarize articles.")
}

func TestChatCompletionParams_KeepsUnknownParameters(t *testing.T) {
	// A model parameter this SDK does not model still has to reach the API.
	built, err := prompt.FromData("p", prompt.Definition{
		Model:    "gpt-5",
		Messages: []prompt.Message{prompt.User("hi")},
		Params:   map[string]any{"reasoning_effort": "low", "verbosity": "high"},
	}.Data()).Build(nil)
	require.NoError(t, err)

	params, err := ChatCompletionParams(built)
	require.NoError(t, err)

	encoded, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"reasoning_effort":"low"`)
	assert.Contains(t, string(encoded), `"verbosity":"high"`)
}

func TestChatCompletionParams_Errors(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		_, err := ChatCompletionParams(nil)
		require.Error(t, err)
	})

	t.Run("completion prompt", func(t *testing.T) {
		built, err := prompt.FromData("p", prompt.Definition{
			Model:  "gpt-4o-mini",
			Prompt: "continue this",
		}.Data()).Build(nil)
		require.NoError(t, err)

		_, err = ChatCompletionParams(built)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "completion prompt")
	})
}
