package prompt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatPrompt is the fixture most tests build on: a two-message chat prompt with
// a templated user message.
func chatPrompt(t *testing.T) *Prompt {
	t.Helper()
	return FromData("greeter", Data{
		Prompt: &Block{
			Type: "chat",
			Messages: []Message{
				{Role: "system", Content: TextContent("You greet people.")},
				{Role: "user", Content: TextContent("Say hello to {{name}}.")},
			},
		},
		Options: &Options{
			Model:  "gpt-4o-mini",
			Params: map[string]any{"temperature": 0.5},
		},
	})
}

func TestBuild_Chat(t *testing.T) {
	built, err := chatPrompt(t).Build(map[string]any{"name": "Joe"})
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o-mini", built.Model)
	assert.True(t, built.IsChat())
	assert.Empty(t, built.Prompt)
	require.Len(t, built.Messages, 2)
	assert.Equal(t, "You greet people.", built.Messages[0].Content.String())
	assert.Equal(t, "Say hello to Joe.", built.Messages[1].Content.String())
	assert.Equal(t, map[string]any{"temperature": 0.5}, built.Params)
}

func TestBuild_Completion(t *testing.T) {
	p := FromData("completer", Data{
		Prompt:  &Block{Type: "completion", Content: "Continue: {{start}}"},
		Options: &Options{Model: "gpt-4o-mini"},
	})

	built, err := p.Build(map[string]any{"start": "once upon"})
	require.NoError(t, err)

	assert.False(t, built.IsChat())
	assert.Equal(t, "Continue: once upon", built.Prompt)
	assert.Empty(t, built.Messages)
}

func TestBuild_NoHTMLEscaping(t *testing.T) {
	// chevron (Python) HTML-escapes by default, which mangles prompt text. We
	// follow the JavaScript SDK, which does not escape at all.
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: `{{a}} and {{{b}}}`},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"a": `5 > 3 & "x"`, "b": `<tag>`})
	require.NoError(t, err)
	assert.Equal(t, `5 > 3 & "x" and <tag>`, built.Prompt)
}

func TestBuild_NonStringVariablesAreJSON(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "{{obj}} {{list}} {{num}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{
		"obj":  map[string]any{"a": 1},
		"list": []any{"x", "y"},
		"num":  2,
	})
	require.NoError(t, err)
	assert.Equal(t, `{"a":1} ["x","y"] 2`, built.Prompt)
}

func TestBuild_DotPathsAndSections(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{
			Type:    "completion",
			Content: "{{user.name}}:{{#items}}[{{.}}]{{/items}}{{^empty}}!{{/empty}}",
		},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{
		"user":  map[string]any{"name": "Ada"},
		"items": []any{"a", "b"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Ada:[a][b]!", built.Prompt)
}

func TestBuild_MissingVariable(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "Hello {{name}}!"},
		Options: &Options{Model: "m"},
	})

	t.Run("renders empty by default", func(t *testing.T) {
		built, err := p.Build(nil)
		require.NoError(t, err)
		assert.Equal(t, "Hello !", built.Prompt)
	})

	t.Run("errors in strict mode", func(t *testing.T) {
		_, err := p.Build(nil, WithStrict())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "name")
	})

	t.Run("strict mode accepts a provided variable", func(t *testing.T) {
		built, err := p.Build(map[string]any{"name": "Ada"}, WithStrict())
		require.NoError(t, err)
		assert.Equal(t, "Hello Ada!", built.Prompt)
	})
}

func TestBuild_InputBinding(t *testing.T) {
	// The JavaScript SDK binds the whole argument map to `input` as well, so a
	// prompt written as {{input}} in the playground works with a plain map.
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "{{input}}|{{input.name}}|{{name}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"name": "Ada"})
	require.NoError(t, err)
	assert.Equal(t, `{"name":"Ada"}|Ada|Ada`, built.Prompt)
}

func TestBuild_ExplicitInputWins(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "{{input}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"input": "an article"})
	require.NoError(t, err)
	assert.Equal(t, "an article", built.Prompt)
}

func TestBuild_ParamsStripBraintrustKeys(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{Type: "completion", Content: "x"},
		Options: &Options{
			Model: "m",
			Params: map[string]any{
				"temperature":       0.2,
				"max_tokens":        100,
				"use_cache":         true,
				"reasoning_enabled": true,
				"reasoning_budget":  10,
			},
		},
	})

	built, err := p.Build(nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"temperature": 0.2, "max_tokens": 100}, built.Params)
}

func TestBuild_DefaultsMergeUnderPromptValues(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "x"},
		Options: &Options{Model: "m", Params: map[string]any{"temperature": 0.2}},
	})

	built, err := p.Build(nil, WithDefaults(map[string]any{
		"temperature": 0.9,
		"max_tokens":  50,
	}))
	require.NoError(t, err)
	assert.Equal(t, 0.2, built.Params["temperature"], "prompt params win over defaults")
	assert.Equal(t, 50, built.Params["max_tokens"])
}

func TestBuild_RequiresModel(t *testing.T) {
	p := FromData("p", Data{Prompt: &Block{Type: "completion", Content: "x"}})

	_, err := p.Build(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}

func TestBuild_RequiresPrompt(t *testing.T) {
	p := FromData("p", Data{Options: &Options{Model: "m"}})

	_, err := p.Build(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no messages or content")
}

func TestBuild_Tools(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{
			Type:     "chat",
			Messages: []Message{{Role: "user", Content: TextContent("hi")}},
			Tools: `[{"type":"function","function":{"name":"lookup_{{kind}}",` +
				`"description":"Look up a {{kind}}","parameters":{"type":"object"}}}]`,
		},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"kind": "city"})
	require.NoError(t, err)
	require.Len(t, built.Tools, 1)
	assert.Equal(t, "function", built.Tools[0].Type)
	assert.Equal(t, "lookup_city", built.Tools[0].Function.Name)
	assert.Equal(t, "Look up a city", built.Tools[0].Function.Description)
}

func TestBuild_ContentParts(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{
			Type: "chat",
			Messages: []Message{{
				Role: "user",
				Content: PartsContent(
					ContentPart{Type: "text", Text: "Describe {{subject}}"},
					ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: "https://x/{{id}}.png"}},
				),
			}},
		},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"subject": "the sky", "id": "42"})
	require.NoError(t, err)

	parts := built.Messages[0].Content.Parts
	require.Len(t, parts, 2)
	assert.Equal(t, "Describe the sky", parts[0].Text)
	assert.Equal(t, "https://x/42.png", parts[1].ImageURL.URL)
}

func TestBuild_ToolCallsAreRendered(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{
			Type: "chat",
			Messages: []Message{
				{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:   "call_{{n}}",
						Type: "function",
						Function: ToolCallFunction{
							Name:      "get_{{kind}}",
							Arguments: `{"q":"{{q}}"}`,
						},
					}},
				},
				{Role: "tool", ToolCallID: "call_{{n}}", Content: TextContent("done")},
			},
		},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"n": 1, "kind": "city", "q": "paris"})
	require.NoError(t, err)

	call := built.Messages[0].ToolCalls[0]
	assert.Equal(t, "call_1", call.ID)
	assert.Equal(t, "get_city", call.Function.Name)
	assert.JSONEq(t, `{"q":"paris"}`, call.Function.Arguments)
	assert.Equal(t, "call_1", built.Messages[1].ToolCallID)
}

func TestBuild_ResponseFormatJSONSchemaIsTemplated(t *testing.T) {
	p := FromData("p", Data{
		Prompt: &Block{Type: "completion", Content: "x"},
		Options: &Options{
			Model: "m",
			Params: map[string]any{
				"response_format": map[string]any{
					"type": "json_schema",
					"json_schema": map[string]any{
						"name": "out",
						"schema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"{{field}}": map[string]any{"type": "string"}},
						},
					},
				},
			},
		},
	})

	built, err := p.Build(map[string]any{"field": "summary"})
	require.NoError(t, err)

	rf, _ := built.Params["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	assert.Contains(t, props, "summary")
}

func TestBuild_TemplateFormats(t *testing.T) {
	newPrompt := func(format string) *Prompt {
		return FromData("p", Data{
			Prompt:         &Block{Type: "completion", Content: "Hello {{name}}"},
			Options:        &Options{Model: "m"},
			TemplateFormat: format,
		})
	}

	t.Run("empty defaults to mustache", func(t *testing.T) {
		built, err := newPrompt("").Build(map[string]any{"name": "Ada"})
		require.NoError(t, err)
		assert.Equal(t, "Hello Ada", built.Prompt)
	})

	t.Run("none passes the template through", func(t *testing.T) {
		built, err := newPrompt("none").Build(map[string]any{"name": "Ada"})
		require.NoError(t, err)
		assert.Equal(t, "Hello {{name}}", built.Prompt)
	})

	t.Run("nunjucks is rejected with a helpful message", func(t *testing.T) {
		_, err := newPrompt("nunjucks").Build(map[string]any{"name": "Ada"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nunjucks")
	})

	t.Run("unknown format is rejected", func(t *testing.T) {
		_, err := newPrompt("jinja").Build(nil)
		require.Error(t, err)
	})

	t.Run("WithTemplateFormat overrides the saved format", func(t *testing.T) {
		built, err := newPrompt("none").Build(
			map[string]any{"name": "Ada"},
			WithTemplateFormat(FormatMustache),
		)
		require.NoError(t, err)
		assert.Equal(t, "Hello Ada", built.Prompt)
	})
}

func TestBuild_MetadataFromLoadedPrompt(t *testing.T) {
	p := chatPrompt(t)
	p.ID = "prompt-1"
	p.ProjectID = "project-1"
	p.Version = "xact-1"

	built, err := p.Build(map[string]any{"name": "Joe"})
	require.NoError(t, err)
	require.NotNil(t, built.Metadata)

	assert.Equal(t, "prompt-1", built.Metadata.ID)
	assert.Equal(t, "project-1", built.Metadata.ProjectID)
	assert.Equal(t, "xact-1", built.Metadata.Version)
	assert.Equal(t, map[string]any{"name": "Joe"}, built.Metadata.Variables)
}

func TestBuild_MetadataFromOrigin(t *testing.T) {
	// A prompt that arrives as a playground parameter has no identity of its
	// own, but its data usually carries an origin pointing at the saved prompt.
	p := FromData("param", Data{
		Prompt:  &Block{Type: "completion", Content: "x"},
		Options: &Options{Model: "m"},
		Origin: &Origin{
			PromptID:      "prompt-9",
			ProjectID:     "project-9",
			PromptVersion: "xact-9",
		},
	})

	built, err := p.Build(nil)
	require.NoError(t, err)
	require.NotNil(t, built.Metadata)
	assert.Equal(t, "prompt-9", built.Metadata.ID)
	assert.Equal(t, "project-9", built.Metadata.ProjectID)
	assert.Equal(t, "xact-9", built.Metadata.Version)
}

func TestBuild_NoMetadataWithoutIdentity(t *testing.T) {
	built, err := chatPrompt(t).Build(nil)
	require.NoError(t, err)
	assert.Nil(t, built.Metadata)
}

func TestBuilt_Map(t *testing.T) {
	p := chatPrompt(t)
	p.ID = "prompt-1"

	built, err := p.Build(map[string]any{"name": "Joe"})
	require.NoError(t, err)

	m := built.Map()
	assert.Equal(t, "gpt-4o-mini", m["model"])
	assert.Equal(t, 0.5, m["temperature"])
	assert.NotContains(t, m, "prompt")

	msgs, ok := m["messages"].([]Message)
	require.True(t, ok)
	require.Len(t, msgs, 2)

	// It round-trips to the JSON an OpenAI-compatible endpoint expects.
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"content":"Say hello to Joe."`)
	assert.NotContains(t, string(raw), "prompt-1", "trace metadata is not part of the request")
}

func TestBuilt_MapCompletion(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: "completion", Content: "hi"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(nil)
	require.NoError(t, err)

	m := built.Map()
	assert.Equal(t, "hi", m["prompt"])
	assert.NotContains(t, m, "messages")
}

func TestBuild_StrictIgnoresSections(t *testing.T) {
	// A section that renders nothing when its variable is absent is ordinary
	// mustache, not a mistake, so strict mode must not reject it.
	p := FromData("p", Data{
		Prompt: &Block{
			Type:    BlockCompletion,
			Content: "{{#extras}}{{.}}{{/extras}}{{^extras}}none{{/extras}}",
		},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(nil, WithStrict())
	require.NoError(t, err)
	assert.Equal(t, "none", built.Prompt)
}

func TestBuild_StructVariables(t *testing.T) {
	// Go callers pass structs, not just maps. Dotted paths must resolve through
	// them, using json tags for names.
	type author struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	p := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "{{a.name}} is {{a.age}}; whole={{a}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"a": author{Name: "Ada", Age: 36}})
	require.NoError(t, err)
	// Interpolating a whole value yields JSON with keys in alphabetical order,
	// since normalization turns it into a map.
	assert.Equal(t, `Ada is 36; whole={"age":36,"name":"Ada"}`, built.Prompt)
}

func TestBuild_TypedMapVariables(t *testing.T) {
	p := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "{{m.k}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"m": map[string]string{"k": "v"}})
	require.NoError(t, err)
	assert.Equal(t, "v", built.Prompt)
}

func TestBuild_StrictSeesStructFields(t *testing.T) {
	type author struct {
		Name string `json:"name"`
	}

	p := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "{{a.name}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(map[string]any{"a": author{Name: "Ada"}}, WithStrict())
	require.NoError(t, err, "a struct field is present, so strict mode must accept it")
	assert.Equal(t, "Ada", built.Prompt)
}

func TestBuild_EmptyCompletionIsStillACompletion(t *testing.T) {
	// A completion template that renders to nothing must not be mistaken for a
	// chat prompt, or Map() would send "messages" instead of "prompt".
	p := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "{{absent}}"},
		Options: &Options{Model: "m"},
	})

	built, err := p.Build(nil)
	require.NoError(t, err)
	assert.False(t, built.IsChat())
	assert.Contains(t, built.Map(), "prompt")
	assert.NotContains(t, built.Map(), "messages")
}

func TestBuild_NullParamsAreDropped(t *testing.T) {
	// The playground sends null for an unset parameter; passing it on makes
	// providers reject the request.
	p := FromData("p", Data{
		Prompt: &Block{Type: BlockChat, Messages: []Message{User("hi")}},
		Options: &Options{
			Model:  "m",
			Params: map[string]any{"response_format": nil, "temperature": 0.4},
		},
	})

	built, err := p.Build(nil)
	require.NoError(t, err)
	assert.NotContains(t, built.Params, "response_format")
	assert.Equal(t, 0.4, built.Params["temperature"])
}
