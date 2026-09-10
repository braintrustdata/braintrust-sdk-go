package prompt

import "encoding/json"

// Definition declares a prompt in Go code. It is the shape you write when
// giving a prompt eval parameter a default:
//
//	Default: prompt.Definition{
//		Model: "gpt-4o-mini",
//		Messages: []prompt.Message{
//			prompt.System("You summarize articles in one sentence."),
//			prompt.User("Summarize this:\n\n{{input}}"),
//		},
//		Params: map[string]any{"temperature": 0},
//	}
//
// Set either Messages (a chat prompt) or Prompt (a completion prompt).
// Definition serializes to the prompt data format Braintrust expects, so it can
// be used anywhere prompt data is accepted.
type Definition struct {
	// Model is the model the prompt targets. Required.
	Model string

	// Messages is the chat message list. Mutually exclusive with Prompt.
	Messages []Message

	// Prompt is the completion template. Mutually exclusive with Messages.
	Prompt string

	// Params are model parameters such as temperature or max_tokens.
	Params map[string]any

	// Tools are tool definitions offered to the model. Chat prompts only.
	Tools []Tool

	// TemplateFormat is [FormatMustache] (the default) or [FormatNone].
	TemplateFormat string
}

// Data converts the definition to prompt data.
func (d Definition) Data() Data {
	block := &Block{}
	if len(d.Messages) > 0 || d.Prompt == "" {
		block.Type = BlockChat
		block.Messages = d.Messages
		if len(d.Tools) > 0 {
			// Braintrust stores tools as a JSON string so the whole array can
			// be templated before it is parsed.
			if encoded, err := json.Marshal(d.Tools); err == nil {
				block.Tools = string(encoded)
			}
		}
	} else {
		block.Type = BlockCompletion
		block.Content = d.Prompt
	}

	return Data{
		Prompt:         block,
		Options:        &Options{Model: d.Model, Params: d.Params},
		TemplateFormat: d.TemplateFormat,
	}
}

// MarshalJSON encodes the definition as prompt data, so a Definition used as a
// parameter default reaches Braintrust in the format the playground expects.
func (d Definition) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Data())
}
