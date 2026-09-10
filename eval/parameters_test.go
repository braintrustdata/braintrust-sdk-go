package eval

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

func TestParameters_String(t *testing.T) {
	p := Parameters{"model": "gpt-4o", "n": 5.0}

	if got := p.String("model"); got != "gpt-4o" {
		t.Errorf("String(model) = %q, want %q", got, "gpt-4o")
	}
	if got := p.String("missing"); got != "" {
		t.Errorf("String(missing) = %q, want empty", got)
	}
	if got := p.String("n"); got != "" {
		t.Errorf("String(n) = %q, want empty for type mismatch", got)
	}
}

func TestParameters_Int(t *testing.T) {
	// JSON numbers decode as float64; also accept int and json.Number.
	p := Parameters{
		"float":  100.0,
		"int":    42,
		"number": json.Number("7"),
		"str":    "nope",
	}

	if got := p.Int("float"); got != 100 {
		t.Errorf("Int(float) = %d, want 100", got)
	}
	if got := p.Int("int"); got != 42 {
		t.Errorf("Int(int) = %d, want 42", got)
	}
	if got := p.Int("number"); got != 7 {
		t.Errorf("Int(number) = %d, want 7", got)
	}
	if got := p.Int("str"); got != 0 {
		t.Errorf("Int(str) = %d, want 0 for type mismatch", got)
	}
	if got := p.Int("missing"); got != 0 {
		t.Errorf("Int(missing) = %d, want 0", got)
	}
}

func TestParameters_Float(t *testing.T) {
	p := Parameters{
		"float":  1.5,
		"int":    3,
		"number": json.Number("2.25"),
		"str":    "nope",
	}

	if got := p.Float64("float"); got != 1.5 {
		t.Errorf("Float(float) = %v, want 1.5", got)
	}
	if got := p.Float64("int"); got != 3.0 {
		t.Errorf("Float(int) = %v, want 3.0", got)
	}
	if got := p.Float64("number"); got != 2.25 {
		t.Errorf("Float(number) = %v, want 2.25", got)
	}
	if got := p.Float64("str"); got != 0 {
		t.Errorf("Float(str) = %v, want 0 for type mismatch", got)
	}
}

func TestParameters_Bool(t *testing.T) {
	p := Parameters{"yes": true, "no": false, "str": "true"}

	if got := p.Bool("yes"); got != true {
		t.Errorf("Bool(yes) = %v, want true", got)
	}
	if got := p.Bool("no"); got != false {
		t.Errorf("Bool(no) = %v, want false", got)
	}
	if got := p.Bool("str"); got != false {
		t.Errorf("Bool(str) = %v, want false for type mismatch", got)
	}
	if got := p.Bool("missing"); got != false {
		t.Errorf("Bool(missing) = %v, want false", got)
	}
}

func TestParameters_HasAndGet(t *testing.T) {
	p := Parameters{"model": "gpt-4o"}

	if !p.Has("model") {
		t.Error("Has(model) = false, want true")
	}
	if p.Has("missing") {
		t.Error("Has(missing) = true, want false")
	}

	v, ok := p.Get("model")
	if !ok || v != "gpt-4o" {
		t.Errorf("Get(model) = (%v, %v), want (gpt-4o, true)", v, ok)
	}
	if _, ok := p.Get("missing"); ok {
		t.Error("Get(missing) ok = true, want false")
	}
}

// A nil Parameters map must be safe to read from (local runs pass no parameters).
func TestParameters_NilSafe(t *testing.T) {
	var p Parameters

	if got := p.String("model"); got != "" {
		t.Errorf("nil String = %q, want empty", got)
	}
	if got := p.Int("n"); got != 0 {
		t.Errorf("nil Int = %d, want 0", got)
	}
	if p.Has("model") {
		t.Error("nil Has = true, want false")
	}
	if _, ok := p.Get("model"); ok {
		t.Error("nil Get ok = true, want false")
	}
}

func TestParameters_Prompt(t *testing.T) {
	definition := prompt.Definition{
		Model:    "gpt-4o-mini",
		Messages: []prompt.Message{prompt.User("hi {{name}}")},
	}

	// However the value arrives -- already converted by the remote eval runner,
	// or set by hand for a local run -- the task reads it the same way.
	tests := []struct {
		name  string
		value any
	}{
		{"converted prompt", prompt.FromData("greeting", definition.Data())},
		{"definition", definition},
		{"prompt data", definition.Data()},
		{"decoded JSON", promptDataMap(t, definition)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := Parameters{"greeting": tt.value}

			p, ok := params.Prompt("greeting")
			require.True(t, ok)

			built, err := p.Build(map[string]any{"name": "Ada"})
			require.NoError(t, err)
			assert.Equal(t, "hi Ada", built.Messages[0].Content.String())
		})
	}
}

func TestParameters_PromptMissingOrWrongType(t *testing.T) {
	params := Parameters{"model": "gpt-4o", "nothing": nil}

	for _, name := range []string{"model", "nothing", "absent"} {
		p, ok := params.Prompt(name)
		assert.False(t, ok, "%s should not read as a prompt", name)
		assert.Nil(t, p)
	}
}

func promptDataMap(t *testing.T, definition prompt.Definition) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(definition)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}
