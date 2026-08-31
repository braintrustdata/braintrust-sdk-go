package prompt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// playgroundPromptJSON is the shape the Braintrust playground sends as the
// value of a prompt eval parameter.
const playgroundPromptJSON = `{
  "prompt": {
    "type": "chat",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Summarize:\n{{input}}"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_1", "content": "ok"}
    ],
    "tools": "[{\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"parameters\":{\"type\":\"object\"}}}]"
  },
  "options": {
    "model": "gpt-4o-mini",
    "params": {"temperature": 0.7, "max_tokens": 512, "use_cache": true}
  },
  "template_format": "mustache",
  "origin": {"prompt_id": "p-1", "project_id": "proj-1", "prompt_version": "v-1"}
}`

func TestData_UnmarshalPlaygroundValue(t *testing.T) {
	var data Data
	require.NoError(t, json.Unmarshal([]byte(playgroundPromptJSON), &data))

	require.NotNil(t, data.Prompt)
	assert.Equal(t, BlockChat, data.Prompt.Type)
	require.Len(t, data.Prompt.Messages, 4)

	assert.Equal(t, "You are a helpful assistant.", data.Prompt.Messages[0].Content.String())
	assert.Equal(t, "Summarize:\n{{input}}", data.Prompt.Messages[1].Content.String())

	assistant := data.Prompt.Messages[2]
	assert.True(t, assistant.Content.IsZero(), "null content decodes as empty")
	require.Len(t, assistant.ToolCalls, 1)
	assert.Equal(t, "lookup", assistant.ToolCalls[0].Function.Name)

	assert.Equal(t, "call_1", data.Prompt.Messages[3].ToolCallID)

	require.NotNil(t, data.Options)
	assert.Equal(t, "gpt-4o-mini", data.Options.Model)
	assert.Equal(t, 0.7, data.Options.Params["temperature"])

	assert.Equal(t, FormatMustache, data.TemplateFormat)
	require.NotNil(t, data.Origin)
	assert.Equal(t, "p-1", data.Origin.PromptID)
}

func TestData_RoundTrip(t *testing.T) {
	var data Data
	require.NoError(t, json.Unmarshal([]byte(playgroundPromptJSON), &data))

	encoded, err := json.Marshal(data)
	require.NoError(t, err)

	var again Data
	require.NoError(t, json.Unmarshal(encoded, &again))
	assert.Equal(t, data, again)
}

func TestContent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		content Content
		want    string
	}{
		{"text", TextContent("hi"), `"hi"`},
		{"empty", Content{}, `""`},
		{
			name:    "parts",
			content: PartsContent(ContentPart{Type: "text", Text: "hi"}),
			want:    `[{"type":"text","text":"hi"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.content)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(encoded))
		})
	}
}

func TestContent_UnmarshalJSON(t *testing.T) {
	t.Run("rejects a non-string, non-array value", func(t *testing.T) {
		var c Content
		err := json.Unmarshal([]byte(`42`), &c)
		require.Error(t, err)
	})

	t.Run("parts round-trip", func(t *testing.T) {
		var c Content
		require.NoError(t, json.Unmarshal([]byte(
			`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"u","detail":"low"}}]`), &c))
		require.Len(t, c.Parts, 2)
		assert.Equal(t, "a", c.Parts[0].Text)
		require.NotNil(t, c.Parts[1].ImageURL)
		assert.Equal(t, "low", c.Parts[1].ImageURL.Detail)
		assert.Equal(t, "a", c.String())
	})
}

func TestMessage_OmitsEmptyContent(t *testing.T) {
	encoded, err := json.Marshal(Message{Role: "assistant"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"role":"assistant"}`, string(encoded))
}

func TestDefinition_Data(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		data := Definition{
			Model:    "gpt-4o-mini",
			Messages: []Message{System("be terse"), User("hi {{name}}")},
			Params:   map[string]any{"temperature": 0},
			Tools:    []Tool{{Type: "function", Function: ToolFunction{Name: "lookup"}}},
		}.Data()

		require.NotNil(t, data.Prompt)
		assert.Equal(t, BlockChat, data.Prompt.Type)
		require.Len(t, data.Prompt.Messages, 2)
		assert.Equal(t, "system", data.Prompt.Messages[0].Role)
		assert.Equal(t, "hi {{name}}", data.Prompt.Messages[1].Content.String())
		assert.Contains(t, data.Prompt.Tools, `"name":"lookup"`, "tools are stored JSON-encoded")
		require.NotNil(t, data.Options)
		assert.Equal(t, "gpt-4o-mini", data.Options.Model)
	})

	t.Run("completion", func(t *testing.T) {
		data := Definition{Model: "m", Prompt: "continue {{x}}"}.Data()

		require.NotNil(t, data.Prompt)
		assert.Equal(t, BlockCompletion, data.Prompt.Type)
		assert.Equal(t, "continue {{x}}", data.Prompt.Content)
	})
}

func TestDefinition_MarshalsAsPromptData(t *testing.T) {
	// A Definition used as a parameter default has to reach Braintrust as
	// prompt data, not as its own Go shape.
	encoded, err := json.Marshal(Definition{
		Model:    "gpt-4o-mini",
		Messages: []Message{User("hi")},
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"prompt": {"type":"chat","messages":[{"role":"user","content":"hi"}]},
		"options": {"model":"gpt-4o-mini"}
	}`, string(encoded))
}
