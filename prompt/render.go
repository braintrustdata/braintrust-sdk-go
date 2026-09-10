package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/braintrustdata/braintrust-sdk-go/internal/mustache"
)

// renderer renders the templates of a single Build call.
type renderer struct {
	format string
	vars   map[string]any
	strict bool
}

// newRenderer validates the template format and returns a renderer for it.
func newRenderer(format string, vars map[string]any, strict bool) (*renderer, error) {
	switch format {
	case "", FormatMustache:
		format = FormatMustache
	case FormatNone:
	case FormatNunjucks:
		return nil, errors.New("nunjucks templates render only in Braintrust playgrounds; " +
			"use the mustache or none template format, or invoke the prompt server-side")
	default:
		return nil, fmt.Errorf("unknown template format %q, want %q, %q or %q",
			format, FormatMustache, FormatNone, FormatNunjucks)
	}

	return &renderer{format: format, vars: vars, strict: strict}, nil
}

// render renders one template string.
func (r *renderer) render(text string) (string, error) {
	if r.format == FormatNone || text == "" {
		return text, nil
	}

	tmpl, err := mustache.ParseString(text)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	// Braintrust prompts are sent to models, not browsers: interpolate values
	// as they are, and encode non-strings as JSON. This matches the JavaScript
	// SDK, which is what the playground preview shows.
	tmpl.Escape(func(s string) string { return s })
	tmpl.Value(templateValue)

	if r.strict {
		if err := r.checkMissing(tmpl); err != nil {
			return "", err
		}
	}

	out, err := tmpl.Render(r.vars)
	if err != nil {
		return "", fmt.Errorf("rendering template: %w", err)
	}
	return out, nil
}

// checkMissing reports variables the template references but the caller did not
// supply. Only plain interpolations are checked, and only at the top level:
// sections legitimately render nothing when absent, and a tag inside a section
// resolves against that section's own scope, which is only known at render
// time. This matches the JavaScript SDK's strict mode.
func (r *renderer) checkMissing(tmpl *mustache.Template) error {
	missing := map[string]struct{}{}

	for _, tag := range tmpl.Tags() {
		if tag.Type() != mustache.Variable {
			continue
		}
		name := tag.Name()
		if name == "" || name == "." {
			continue
		}
		if _, ok := lookupPath(r.vars, name); !ok {
			missing[name] = struct{}{}
		}
	}

	if len(missing) == 0 {
		return nil
	}

	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)

	return fmt.Errorf("missing template variables: %s", strings.Join(names, ", "))
}

// lookupPath resolves a dotted mustache path against the variables.
func lookupPath(vars map[string]any, path string) (any, bool) {
	var current any = vars

	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}

	return current, true
}

// templateValue converts an interpolated value to the text that replaces the
// placeholder. Strings are used as-is; anything else becomes JSON.
func templateValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// renderMessage renders every templated field of a message. Roles and names are
// not templated, matching the other Braintrust SDKs.
func (r *renderer) renderMessage(msg Message) (Message, error) {
	out := msg

	content, err := r.renderContent(msg.Content)
	if err != nil {
		return Message{}, err
	}
	out.Content = content

	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			rendered := call
			for _, field := range []struct {
				src string
				dst *string
			}{
				{call.ID, &rendered.ID},
				{call.Function.Name, &rendered.Function.Name},
				{call.Function.Arguments, &rendered.Function.Arguments},
			} {
				value, err := r.render(field.src)
				if err != nil {
					return Message{}, err
				}
				*field.dst = value
			}
			out.ToolCalls[i] = rendered
		}
	}

	if msg.ToolCallID != "" {
		id, err := r.render(msg.ToolCallID)
		if err != nil {
			return Message{}, err
		}
		out.ToolCallID = id
	}

	return out, nil
}

// renderContent renders message content, whether text or parts.
func (r *renderer) renderContent(content Content) (Content, error) {
	if len(content.Parts) == 0 {
		text, err := r.render(content.Text)
		if err != nil {
			return Content{}, err
		}
		return Content{Text: text}, nil
	}

	parts := make([]ContentPart, len(content.Parts))
	for i, part := range content.Parts {
		rendered := part

		text, err := r.render(part.Text)
		if err != nil {
			return Content{}, err
		}
		rendered.Text = text

		if part.ImageURL != nil {
			url, err := r.render(part.ImageURL.URL)
			if err != nil {
				return Content{}, err
			}
			image := *part.ImageURL
			image.URL = url
			rendered.ImageURL = &image
		}

		if part.File != nil {
			file := *part.File
			for _, field := range []struct {
				src string
				dst *string
			}{
				{part.File.FileData, &file.FileData},
				{part.File.FileID, &file.FileID},
				{part.File.Filename, &file.Filename},
			} {
				value, err := r.render(field.src)
				if err != nil {
					return Content{}, err
				}
				*field.dst = value
			}
			rendered.File = &file
		}

		parts[i] = rendered
	}

	return Content{Parts: parts}, nil
}

// renderParams templates the JSON schema inside a json_schema response format,
// which is the only model parameter Braintrust templates. Everything else is
// passed through untouched.
func (r *renderer) renderParams(params map[string]any) (map[string]any, error) {
	format, ok := params["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		return params, nil
	}

	jsonSchema, ok := format["json_schema"].(map[string]any)
	if !ok {
		return params, nil
	}

	schema, ok := jsonSchema["schema"]
	if !ok || schema == nil {
		return params, nil
	}

	rendered, err := r.renderJSONValue(schema)
	if err != nil {
		return nil, err
	}

	// A schema stored as a string renders to a string; parse it back so callers
	// always see an object.
	if text, ok := rendered.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("rendered response_format schema is not valid JSON: %w", err)
		}
		rendered = parsed
	}

	out := copyMap(params)
	newFormat := copyMap(format)
	newSchema := copyMap(jsonSchema)
	newSchema["schema"] = rendered
	newFormat["json_schema"] = newSchema
	out["response_format"] = newFormat
	return out, nil
}

// renderJSONValue renders every string in a decoded JSON value, keys included.
func (r *renderer) renderJSONValue(value any) (any, error) {
	switch v := value.(type) {
	case string:
		return r.render(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := r.renderJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			renderedKey, err := r.render(key)
			if err != nil {
				return nil, err
			}
			rendered, err := r.renderJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[renderedKey] = rendered
		}
		return out, nil
	default:
		return value, nil
	}
}

// copyMap returns a shallow copy of m.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
