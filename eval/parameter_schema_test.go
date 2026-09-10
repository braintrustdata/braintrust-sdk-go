package eval

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

func TestParameterSchemaResolve_DefaultsOnly(t *testing.T) {
	t.Parallel()

	schema := ParameterSchema{
		"model":      {Type: "model", Default: "gpt-4o"},
		"max_length": {Type: "number", Default: 100.0},
	}

	// No supplied values -> declared defaults apply.
	got, err := schema.Resolve(nil)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o", got.String("model"))
	assert.Equal(t, 100, got.Int("max_length"))
}

func TestParameterSchemaResolve_SuppliedValueWins(t *testing.T) {
	t.Parallel()

	schema := ParameterSchema{"model": {Type: "model", Default: "gpt-4o"}}

	got, err := schema.Resolve(map[string]any{"model": "claude"})
	require.NoError(t, err)

	assert.Equal(t, "claude", got.String("model"))
}

// A value the schema does not declare is still delivered, matching the other
// SDKs: the playground may send a parameter the code has not declared, and
// dropping it silently would be worse than surfacing it.
func TestParameterSchemaResolve_PassesThroughUndeclaredKeys(t *testing.T) {
	t.Parallel()

	got, err := ParameterSchema(nil).Resolve(map[string]any{"adhoc": "value"})
	require.NoError(t, err)

	assert.Equal(t, "value", got.String("adhoc"))
}

// Nil rather than an empty map, so a task can tell "no parameters" from
// "an empty set".
func TestParameterSchemaResolve_NothingIsNil(t *testing.T) {
	t.Parallel()

	got, err := ParameterSchema(nil).Resolve(nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = ParameterSchema{}.Resolve(map[string]any{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

// A declaration with no default contributes no value, so the task sees the
// parameter as absent rather than as a zero value.
func TestParameterSchemaResolve_DeclarationWithoutDefaultIsAbsent(t *testing.T) {
	t.Parallel()

	got, err := ParameterSchema{"threshold": {Type: "number"}}.Resolve(nil)
	require.NoError(t, err)

	assert.False(t, got.Has("threshold"))
}

// Resolve runs twice per playground request -- once up front so a bad value can
// be reported before anything is created in Braintrust, and again when the run
// is assembled. Resolving an already-resolved set must therefore be a no-op.
func TestParameterSchemaResolve_IsIdempotent(t *testing.T) {
	t.Parallel()

	schema := ParameterSchema{
		"model":     {Type: ParameterTypeModel, Default: "gpt-4o"},
		"threshold": {Type: "number", Default: 0.5},
	}

	once, err := schema.Resolve(map[string]any{"threshold": 0.9})
	require.NoError(t, err)

	twice, err := schema.Resolve(once)
	require.NoError(t, err)

	assert.Equal(t, once, twice)
}

// promptParameterSchema declares one prompt parameter with a Go-written default.
func promptParameterSchema() ParameterSchema {
	return ParameterSchema{
		"summary_prompt": {
			Type: ParameterTypePrompt,
			Default: prompt.Definition{
				Model:    "gpt-4o-mini",
				Messages: []prompt.Message{prompt.User("Summarize {{input}}")},
			},
		},
	}
}

func TestParameterSchemaResolve_PromptDefaultBecomesAPrompt(t *testing.T) {
	t.Parallel()

	resolved, err := promptParameterSchema().Resolve(nil)
	require.NoError(t, err)

	p, ok := resolved.Prompt("summary_prompt")
	require.True(t, ok, "a prompt default must reach the task as a prompt, not a definition")

	built, err := p.Build(map[string]any{"input": "an article"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", built.Model)
	assert.Equal(t, "Summarize an article", built.Messages[0].Content.String())
}

func TestParameterSchemaResolve_PromptValueFromPlayground(t *testing.T) {
	t.Parallel()

	// What the playground actually sends: prompt data as decoded JSON.
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"prompt": {"type": "chat", "messages": [{"role": "user", "content": "Rewrite {{input}}"}]},
		"options": {"model": "gpt-4o"},
		"origin": {"prompt_id": "p-1", "project_id": "proj-1", "prompt_version": "v-1"}
	}`), &value))

	resolved, err := promptParameterSchema().Resolve(map[string]any{"summary_prompt": value})
	require.NoError(t, err)

	p, ok := resolved.Prompt("summary_prompt")
	require.True(t, ok)

	built, err := p.Build(map[string]any{"input": "a poem"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", built.Model, "the supplied value replaces the default")
	assert.Equal(t, "Rewrite a poem", built.Messages[0].Content.String())
	require.NotNil(t, built.Metadata)
	assert.Equal(t, "p-1", built.Metadata.ID, "the prompt still links back to Braintrust")
}

func TestParameterSchemaResolve_PromptIsIdempotent(t *testing.T) {
	t.Parallel()

	schema := promptParameterSchema()

	once, err := schema.Resolve(nil)
	require.NoError(t, err)
	twice, err := schema.Resolve(once)
	require.NoError(t, err)

	first, ok := once.Prompt("summary_prompt")
	require.True(t, ok)
	second, ok := twice.Prompt("summary_prompt")
	require.True(t, ok)
	assert.Same(t, first, second, "re-resolving must not rebuild the prompt")
}

func TestParameterSchemaResolve_InvalidPromptValue(t *testing.T) {
	t.Parallel()

	_, err := promptParameterSchema().Resolve(map[string]any{"summary_prompt": "gpt-4o"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summary_prompt")
}

func TestParameterSchemaResolve_PromptWithNoValueOrDefault(t *testing.T) {
	t.Parallel()

	// A prompt parameter with neither a value nor a default: the task cannot run.
	schema := ParameterSchema{"summary_prompt": {Type: ParameterTypePrompt}}

	_, err := schema.Resolve(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summary_prompt")
}
