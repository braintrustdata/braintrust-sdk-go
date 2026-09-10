// Package prompt provides a local, renderable representation of a Braintrust
// prompt.
//
// A prompt is a template — messages or a completion string containing
// [mustache] placeholders — plus a model and its parameters. [Prompt.Build]
// renders the template with a set of variables and returns a [Built] that you
// can hand to any LLM client.
//
// Prompts reach a Go program two ways. A remote eval can declare a prompt
// parameter, and the value chosen in the Braintrust playground arrives on the
// task's hooks:
//
//	p, ok := hooks.Parameters.Prompt("summary_prompt")
//	built, err := p.Build(map[string]any{"input": input})
//
// Or a prompt saved in Braintrust can be loaded by slug:
//
//	p, err := bt.LoadPrompt(ctx, prompt.LoadOpts{Slug: "summarizer"})
//	built, err := p.Build(map[string]any{"input": article})
//
// [mustache]: https://mustache.github.io/mustache.5.html
package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Template formats a prompt can declare. Mustache is the default and the only
// one this SDK renders; see [Prompt.Build].
const (
	// FormatMustache renders {{variable}} placeholders. The default.
	FormatMustache = "mustache"

	// FormatNone treats the prompt as literal text.
	FormatNone = "none"

	// FormatNunjucks is a Braintrust playground feature the Go SDK cannot
	// render. Building such a prompt returns an error.
	FormatNunjucks = "nunjucks"
)

// Data is the prompt payload Braintrust stores and the playground sends: the
// prompt body, the model and its parameters, and how the body should be
// templated. It mirrors the PromptData object in the Braintrust API.
type Data struct {
	// Prompt is the prompt body: chat messages or a completion string.
	Prompt *Block `json:"prompt,omitempty"`

	// Options holds the model and its parameters.
	Options *Options `json:"options,omitempty"`

	// TemplateFormat is one of [FormatMustache], [FormatNone] or
	// [FormatNunjucks]. Empty means mustache.
	TemplateFormat string `json:"template_format,omitempty"`

	// Origin identifies the saved prompt this data came from, when it was sent
	// by the Braintrust playground.
	Origin *Origin `json:"origin,omitempty"`

	// Parser, ToolFunctions and MCP are server-side features the Go SDK does
	// not act on. They are preserved so prompt data survives a round trip.
	Parser        json.RawMessage `json:"parser,omitempty"`
	ToolFunctions json.RawMessage `json:"tool_functions,omitempty"`
	MCP           json.RawMessage `json:"mcp,omitempty"`
}

// Origin identifies the saved prompt a [Data] value came from.
type Origin struct {
	PromptID      string `json:"prompt_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

// Block is the body of a prompt. Type is either "chat", in which case Messages
// is set, or "completion", in which case Content is.
type Block struct {
	Type string `json:"type"`

	// Messages is the chat message list, for Type "chat".
	Messages []Message `json:"messages,omitempty"`

	// Tools is a JSON-encoded array of tool definitions, for Type "chat". It is
	// a string because that is how Braintrust stores it: the whole array is
	// templated before being parsed.
	Tools string `json:"tools,omitempty"`

	// Content is the completion template, for Type "completion".
	Content string `json:"content,omitempty"`
}

// Block types.
const (
	BlockChat       = "chat"
	BlockCompletion = "completion"
)

// Options holds the model a prompt targets and the parameters to call it with.
type Options struct {
	Model string `json:"model,omitempty"`

	// Params are model parameters such as temperature or max_tokens. They are
	// untyped because the shape differs per provider and Braintrust accepts
	// provider-specific keys.
	Params map[string]any `json:"params,omitempty"`

	Position string `json:"position,omitempty"`
}

// Message is a single chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content,omitzero"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// System returns a system message with the given template text.
func System(text string) Message { return Message{Role: "system", Content: TextContent(text)} }

// User returns a user message with the given template text.
func User(text string) Message { return Message{Role: "user", Content: TextContent(text)} }

// Assistant returns an assistant message with the given template text.
func Assistant(text string) Message { return Message{Role: "assistant", Content: TextContent(text)} }

// Content is the content of a [Message]. It is either plain text or a list of
// parts (text, images, files), because that is what the underlying API allows.
// Use [TextContent] or [PartsContent] to build one.
type Content struct {
	// Text is set when the content is plain text.
	Text string

	// Parts is set when the content is a list of parts.
	Parts []ContentPart
}

// TextContent returns plain text content.
func TextContent(text string) Content { return Content{Text: text} }

// PartsContent returns multi-part content.
func PartsContent(parts ...ContentPart) Content { return Content{Parts: parts} }

// String returns the content as text. Multi-part content is joined from its
// text parts; non-text parts are skipped.
func (c Content) String() string {
	if len(c.Parts) == 0 {
		return c.Text
	}

	var b strings.Builder
	for _, part := range c.Parts {
		if part.Type == "text" || part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// IsZero reports whether the content is empty. It also makes `omitzero` work on
// [Message.Content].
func (c Content) IsZero() bool { return c.Text == "" && len(c.Parts) == 0 }

// MarshalJSON encodes content as a string or an array of parts, matching the
// Braintrust API.
func (c Content) MarshalJSON() ([]byte, error) {
	if len(c.Parts) > 0 {
		return json.Marshal(c.Parts)
	}
	return json.Marshal(c.Text)
}

// UnmarshalJSON accepts a string, an array of parts, or null.
func (c *Content) UnmarshalJSON(data []byte) error {
	*c = Content{}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		return nil
	}

	if trimmed[0] == '[' {
		return json.Unmarshal(data, &c.Parts)
	}

	if trimmed[0] == '"' {
		return json.Unmarshal(data, &c.Text)
	}

	return fmt.Errorf("prompt: message content must be a string or an array, got %s", trimmed)
}

// ContentPart is one part of multi-part message content.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	File     *File     `json:"file,omitempty"`
}

// ImageURL is the image payload of a content part.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// File is the file payload of a content part.
type File struct {
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// ToolCall is a tool call recorded on an assistant message.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the function a [ToolCall] invokes.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes the function a [Tool] exposes.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}
