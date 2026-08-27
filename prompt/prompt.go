package prompt

import (
	"encoding/json"
	"fmt"
)

// braintrustParams are parameters Braintrust interprets itself. They are
// stripped from the built parameters so they never reach a model provider.
var braintrustParams = map[string]struct{}{
	"use_cache":         {},
	"reasoning_enabled": {},
	"reasoning_budget":  {},
}

// Prompt is a renderable Braintrust prompt: a template, a model, and the
// parameters to call it with. Build it with [Prompt.Build].
//
// The identity fields are set when the prompt was loaded from Braintrust; a
// prompt constructed from raw data with [FromData] has none.
type Prompt struct {
	// ID is the prompt's Braintrust ID.
	ID string

	// Name is the prompt's display name.
	Name string

	// Slug is the prompt's slug.
	Slug string

	// ProjectID is the ID of the project holding the prompt.
	ProjectID string

	// Version is the transaction ID of the loaded version.
	Version string

	// Data is the prompt payload: body, model, options.
	Data Data
}

// FromData returns a prompt built from raw prompt data, named name. Use it when
// prompt data arrives from somewhere other than a Braintrust API call — a
// playground parameter, a config file, a test fixture.
func FromData(name string, data Data) *Prompt {
	return &Prompt{Name: name, Slug: name, Data: data}
}

// FromValue converts a value into a prompt, named name.
//
// It accepts anything a prompt can arrive as: a [*Prompt], a [Definition] or
// [Data] declared in Go, or prompt data decoded from JSON (a map[string]any or
// [json.RawMessage], which is how the Braintrust playground sends a prompt
// parameter). This is what makes a prompt parameter's Go default and its
// playground value end up as the same type.
func FromValue(name string, value any) (*Prompt, error) {
	switch v := value.(type) {
	case nil:
		return nil, missingPromptError(name)
	case *Prompt:
		if v == nil {
			return nil, missingPromptError(name)
		}
		return v, nil
	case Prompt:
		copied := v
		return &copied, nil
	case Definition:
		return FromData(name, v.Data()), nil
	case *Definition:
		if v == nil {
			return nil, missingPromptError(name)
		}
		return FromData(name, v.Data()), nil
	case Data:
		return FromData(name, v), nil
	case *Data:
		if v == nil {
			return nil, missingPromptError(name)
		}
		return FromData(name, *v), nil
	}

	// Anything else has to look like prompt data once encoded: a decoded JSON
	// object, or raw JSON bytes.
	//nolint:gocritic // the type switch above handles the typed cases
	var encoded []byte
	var err error
	switch v := value.(type) {
	case json.RawMessage:
		encoded = v
	case []byte:
		encoded = v
	case string:
		encoded = []byte(v)
	default:
		encoded, err = json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("prompt %q: cannot read a value of type %T: %w", name, value, err)
		}
	}

	var data Data
	if err := json.Unmarshal(encoded, &data); err != nil {
		return nil, fmt.Errorf("prompt %q: value is not prompt data: %w", name, err)
	}
	if data.Prompt == nil {
		return nil, fmt.Errorf("prompt %q: value is not prompt data: it has no prompt body", name)
	}

	return FromData(name, data), nil
}

// missingPromptError explains what to do about a prompt parameter that has
// neither a value nor a default, which is otherwise a confusing failure.
func missingPromptError(name string) error {
	return fmt.Errorf(
		"prompt %q: no value and no default; select a prompt in the playground "+
			"or give the parameter a Default", name)
}

// Model returns the model the prompt targets, or "" if it declares none.
func (p *Prompt) Model() string {
	if p.Data.Options == nil {
		return ""
	}
	return p.Data.Options.Model
}

// TemplateFormat returns the prompt's template format, defaulting to
// [FormatMustache].
func (p *Prompt) TemplateFormat() string {
	if p.Data.TemplateFormat == "" {
		return FormatMustache
	}
	return p.Data.TemplateFormat
}

// Messages returns the prompt's unrendered chat messages, or nil for a
// completion prompt.
func (p *Prompt) Messages() []Message {
	if p.Data.Prompt == nil {
		return nil
	}
	return p.Data.Prompt.Messages
}

// buildConfig holds the options a single [Prompt.Build] call was given.
type buildConfig struct {
	defaults       map[string]any
	templateFormat string
	strict         bool
}

// BuildOption configures [Prompt.Build].
type BuildOption func(*buildConfig)

// WithDefaults supplies default model parameters, used for any parameter the
// prompt itself does not set.
func WithDefaults(defaults map[string]any) BuildOption {
	return func(c *buildConfig) { c.defaults = defaults }
}

// WithTemplateFormat overrides the template format saved on the prompt.
func WithTemplateFormat(format string) BuildOption {
	return func(c *buildConfig) { c.templateFormat = format }
}

// WithStrict makes Build fail when the template references a variable that was
// not supplied. By default a missing variable renders as an empty string, which
// is what the Braintrust playground does.
func WithStrict() BuildOption {
	return func(c *buildConfig) { c.strict = true }
}

// Build renders the prompt with vars and returns the result.
//
// Variables are addressed by name ({{name}}); the whole map is also bound to
// "input", so a prompt written against {{input}} in the playground works
// without wrapping. Values that are not strings are interpolated as JSON.
//
//	built, err := p.Build(map[string]any{"article": text})
//	if err != nil {
//		return err
//	}
//	resp, err := client.Chat.Completions.New(ctx, traceopenai.ChatCompletionParams(built))
//
// Build fails if the prompt has no body or no model, if the template is
// malformed, or if the prompt uses a template format the SDK cannot render.
func (p *Prompt) Build(vars map[string]any, opts ...BuildOption) (*Built, error) {
	cfg := buildConfig{templateFormat: p.TemplateFormat()}
	for _, opt := range opts {
		opt(&cfg)
	}

	if p.Data.Prompt == nil {
		return nil, fmt.Errorf("prompt %q: has no messages or content", p.Name)
	}

	model := p.Model()
	if model == "" {
		if fallback, ok := cfg.defaults["model"].(string); ok {
			model = fallback
		}
	}
	if model == "" {
		return nil, fmt.Errorf("prompt %q: no model; set one on the prompt or pass WithDefaults", p.Name)
	}

	renderer, err := newRenderer(cfg.templateFormat, templateVars(vars), cfg.strict)
	if err != nil {
		return nil, err
	}

	params, err := renderer.renderParams(p.buildParams(cfg.defaults))
	if err != nil {
		return nil, err
	}

	built := &Built{
		Model:    model,
		Params:   params,
		Metadata: p.metadata(vars),
	}

	switch p.Data.Prompt.Type {
	case BlockCompletion:
		built.chat = false
		content, err := renderer.render(p.Data.Prompt.Content)
		if err != nil {
			return nil, err
		}
		built.Prompt = content

	case BlockChat, "":
		built.chat = true
		messages := make([]Message, 0, len(p.Data.Prompt.Messages))
		for _, msg := range p.Data.Prompt.Messages {
			rendered, err := renderer.renderMessage(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, rendered)
		}
		built.Messages = messages

		tools, err := p.buildTools(renderer)
		if err != nil {
			return nil, err
		}
		built.Tools = tools

	default:
		return nil, fmt.Errorf("prompt %q: unknown prompt type %q", p.Name, p.Data.Prompt.Type)
	}

	return built, nil
}

// buildParams merges defaults under the prompt's own parameters and drops the
// parameters Braintrust handles itself.
func (p *Prompt) buildParams(defaults map[string]any) map[string]any {
	params := make(map[string]any, len(defaults))
	for key, value := range defaults {
		if key == "model" {
			continue
		}
		params[key] = value
	}

	if p.Data.Options != nil {
		for key, value := range p.Data.Options.Params {
			if _, skip := braintrustParams[key]; skip {
				continue
			}
			// A null parameter is the playground saying "unset". Passing it on
			// makes providers reject the request, so drop it as the JavaScript
			// SDK does.
			if value == nil {
				continue
			}
			params[key] = value
		}
	}

	return params
}

// buildTools renders and parses the prompt's tool definitions.
func (p *Prompt) buildTools(r *renderer) ([]Tool, error) {
	raw := p.Data.Prompt.Tools
	if raw == "" {
		return nil, nil
	}

	rendered, err := r.render(raw)
	if err != nil {
		return nil, err
	}
	if rendered == "" {
		return nil, nil
	}

	var tools []Tool
	if err := json.Unmarshal([]byte(rendered), &tools); err != nil {
		return nil, fmt.Errorf("prompt %q: tools are not valid JSON: %w", p.Name, err)
	}
	return tools, nil
}

// metadata describes the prompt for tracing, so a rendered prompt links back to
// Braintrust. It returns nil when the prompt has no identity to link to.
func (p *Prompt) metadata(vars map[string]any) *Metadata {
	meta := Metadata{
		ID:        p.ID,
		ProjectID: p.ProjectID,
		Version:   p.Version,
		Variables: vars,
	}

	// A prompt that arrived as a playground parameter has no identity of its
	// own, but its data records where it came from.
	if origin := p.Data.Origin; origin != nil {
		if meta.ID == "" {
			meta.ID = origin.PromptID
		}
		if meta.ProjectID == "" {
			meta.ProjectID = origin.ProjectID
		}
		if meta.Version == "" {
			meta.Version = origin.PromptVersion
		}
	}

	if meta.ID == "" && meta.ProjectID == "" && meta.Version == "" {
		return nil
	}
	return &meta
}

// templateVars binds the whole variable map to "input" as well as exposing each
// variable by name, matching the JavaScript SDK. An explicit "input" wins.
func templateVars(vars map[string]any) map[string]any {
	normalized := normalizeVars(vars)

	out := make(map[string]any, len(normalized)+1)
	out["input"] = normalized
	for key, value := range normalized {
		out[key] = value
	}
	return out
}

// normalizeVars converts the variables to plain JSON values -- maps, slices,
// strings, numbers, bools -- so that dotted paths such as {{user.name}} resolve
// against any value a caller passes, including structs and typed maps, and so
// that interpolating a value produces the same text every other Braintrust SDK
// produces. Field names come from `json` tags, as they do everywhere else in
// the SDK.
//
// One consequence: interpolating a whole struct or map yields JSON with keys in
// alphabetical order, because the value has become a map by then.
//
// Values that cannot be encoded are left as they are: rendering something is
// better than failing the whole build over one variable.
func normalizeVars(vars map[string]any) map[string]any {
	if len(vars) == 0 {
		return vars
	}

	encoded, err := json.Marshal(vars)
	if err != nil {
		return vars
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return vars
	}
	return out
}
