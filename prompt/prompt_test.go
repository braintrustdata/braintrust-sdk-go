package prompt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromValue(t *testing.T) {
	definition := Definition{
		Model:    "gpt-4o-mini",
		Messages: []Message{User("hi {{name}}")},
	}

	// Everything a prompt can arrive as has to produce the same usable prompt:
	// a Go-declared default, or JSON from the playground.
	tests := []struct {
		name  string
		value any
	}{
		{"definition", definition},
		{"definition pointer", &definition},
		{"data", definition.Data()},
		{"prompt", FromData("p", definition.Data())},
		{"decoded JSON object", decodeToMap(t, definition)},
		{"raw JSON", json.RawMessage(mustMarshal(t, definition))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := FromValue("p", tt.value)
			require.NoError(t, err)

			built, err := p.Build(map[string]any{"name": "Ada"})
			require.NoError(t, err)
			assert.Equal(t, "gpt-4o-mini", built.Model)
			require.Len(t, built.Messages, 1)
			assert.Equal(t, "hi Ada", built.Messages[0].Content.String())
		})
	}
}

func TestFromValue_TypedNilPointers(t *testing.T) {
	// A typed nil pointer through the `any` interface (e.g. a
	// ParameterDef.Default holding (*prompt.Definition)(nil)) must return the
	// missing-prompt error, not panic on a nil dereference.
	for _, value := range []any{(*Definition)(nil), (*Data)(nil), (*Prompt)(nil)} {
		_, err := FromValue("my_prompt", value)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "my_prompt")
	}
}

func TestFromValue_Errors(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"nil", nil},
		{"nil prompt", (*Prompt)(nil)},
		{"a string that is not prompt data", "gpt-4o-mini"},
		{"a number", 42},
		{"an object without a prompt body", map[string]any{"options": map[string]any{"model": "m"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromValue("my_prompt", tt.value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "my_prompt", "the error names the parameter")
		})
	}
}

func TestFromData(t *testing.T) {
	p := FromData("greeter", Data{Prompt: &Block{Type: BlockChat}})
	assert.Equal(t, "greeter", p.Name)
	assert.Equal(t, "greeter", p.Slug)
	assert.Empty(t, p.ID)
}

func TestPromptAccessors(t *testing.T) {
	p := FromData("p", Definition{Model: "m", Messages: []Message{User("hi")}}.Data())

	assert.Equal(t, "m", p.Model())
	assert.Equal(t, FormatMustache, p.TemplateFormat(), "an unset format means mustache")
	require.Len(t, p.Messages(), 1)

	empty := FromData("p", Data{})
	assert.Empty(t, empty.Model())
	assert.Nil(t, empty.Messages())
}

func decodeToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(mustMarshal(t, v), &out))
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	return encoded
}
