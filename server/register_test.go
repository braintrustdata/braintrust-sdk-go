package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveParameters_DefaultsOnly(t *testing.T) {
	schema := &Parameters{
		Schema: map[string]ParameterDef{
			"model":      {Type: "model", Default: "gpt-4o"},
			"max_length": {Type: "number", Default: 100.0},
		},
	}

	// No request values -> declared defaults apply.
	got := resolveParameters(schema, nil)

	assert.Equal(t, "gpt-4o", got.String("model"))
	assert.Equal(t, 100, got.Int("max_length"))
}

func TestResolveParameters_RequestOverridesDefault(t *testing.T) {
	schema := &Parameters{
		Schema: map[string]ParameterDef{
			"model": {Type: "model", Default: "gpt-4o"},
		},
	}

	got := resolveParameters(schema, map[string]any{"model": "claude"})

	assert.Equal(t, "claude", got.String("model"))
}

func TestResolveParameters_PassesThroughUnknownKeys(t *testing.T) {
	// A value not in the schema is still delivered (matches Ruby's merge semantics).
	got := resolveParameters(nil, map[string]any{"adhoc": "value"})

	assert.Equal(t, "value", got.String("adhoc"))
}

func TestResolveParameters_EmptyIsNil(t *testing.T) {
	assert.Nil(t, resolveParameters(nil, nil))
}
