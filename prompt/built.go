package prompt

import (
	"encoding/json"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// spanMetadataAttr is the span attribute Braintrust reads metadata from. It
// matches the key the eval loop writes.
const spanMetadataAttr = "braintrust.metadata"

// Built is a rendered prompt: the model to call, the rendered body, and the
// parameters to call it with. Get one from [Prompt.Build].
//
// It is deliberately provider-agnostic. Convert it to a request for your client
// — trace/contrib/openai has ChatCompletionParams for openai-go — or use [Built.Map]
// for anything that speaks OpenAI-shaped JSON.
type Built struct {
	// Model is the model to call.
	Model string

	// Messages is the rendered chat message list, for a chat prompt.
	Messages []Message

	// Prompt is the rendered completion text, for a completion prompt.
	Prompt string

	// Params are the model parameters (temperature, max_tokens, ...).
	Params map[string]any

	// Tools are the rendered tool definitions, if the prompt declares any.
	Tools []Tool

	// Metadata describes the prompt this was built from, for tracing. It is nil
	// when the prompt has no Braintrust identity. See [Built.AnnotateSpan].
	Metadata *Metadata

	// chat records which kind of prompt this was built from, rather than
	// inferring it from the rendered output -- a completion prompt whose
	// template renders to an empty string is still a completion prompt.
	chat bool
}

// Metadata identifies the prompt a [Built] came from and the variables it was
// rendered with. Recording it on a span is what links a model call back to the
// prompt in Braintrust.
type Metadata struct {
	ID        string         `json:"id,omitempty"`
	ProjectID string         `json:"project_id,omitempty"`
	Version   string         `json:"version,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
}

// IsChat reports whether the built prompt is a chat prompt (Messages) rather
// than a completion (Prompt).
func (b *Built) IsChat() bool { return b.chat }

// Map returns the built prompt as the OpenAI-shaped map the other Braintrust
// SDKs return from build(): model, messages or prompt, tools, and every model
// parameter at the top level. Use it with clients this SDK has no adapter for.
//
//	body, err := json.Marshal(built.Map())
//
// The values are the SDK's own types, not decoded JSON -- "messages" holds
// []Message, for instance -- so the map is meant for encoding rather than for
// type assertions. Tracing metadata is not included; it is not part of the
// request.
func (b *Built) Map() map[string]any {
	out := make(map[string]any, len(b.Params)+3)
	for key, value := range b.Params {
		out[key] = value
	}

	out["model"] = b.Model
	if b.IsChat() {
		out["messages"] = b.Messages
	} else {
		out["prompt"] = b.Prompt
	}
	if len(b.Tools) > 0 {
		out["tools"] = b.Tools
	}

	return out
}

// AnnotateSpan records the prompt's identity and the variables it was rendered
// with on span, which is what links the call back to the prompt in Braintrust.
// It does nothing when there is no metadata to record.
//
// Pass the span covering the model call — in an eval, hooks.TaskSpan:
//
//	built.AnnotateSpan(hooks.TaskSpan)
func (b *Built) AnnotateSpan(span oteltrace.Span) {
	if b.Metadata == nil || span == nil || !span.IsRecording() {
		return
	}

	encoded, err := json.Marshal(map[string]any{"prompt": b.Metadata})
	if err != nil {
		return
	}

	span.SetAttributes(attribute.String(spanMetadataAttr, string(encoded)))
}
