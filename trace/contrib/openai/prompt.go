package openai

import (
	"encoding/json"
	"fmt"

	openaigo "github.com/openai/openai-go"

	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

// ChatCompletionParams converts a rendered Braintrust prompt into request
// parameters for the OpenAI Go client.
//
//	built, err := p.Build(map[string]any{"input": article})
//	if err != nil {
//		return err
//	}
//
//	params, err := traceopenai.ChatCompletionParams(built)
//	if err != nil {
//		return err
//	}
//	resp, err := client.Chat.Completions.New(ctx, params)
//
// The prompt's model, messages, tools and every model parameter it carries are
// copied across. It returns an error for a completion-style prompt, which the
// chat completions endpoint cannot take.
func ChatCompletionParams(built *prompt.Built) (openaigo.ChatCompletionNewParams, error) {
	var params openaigo.ChatCompletionNewParams

	if built == nil {
		return params, fmt.Errorf("traceopenai: nil prompt")
	}
	if !built.IsChat() {
		return params, fmt.Errorf(
			"traceopenai: this is a completion prompt; use built.Prompt with the completions API")
	}

	// The built prompt already knows the OpenAI request shape, and the client's
	// own types decode it.
	request := built.Map()
	encoded, err := json.Marshal(request)
	if err != nil {
		return params, fmt.Errorf("traceopenai: encoding prompt: %w", err)
	}
	if err := json.Unmarshal(encoded, &params); err != nil {
		return params, fmt.Errorf("traceopenai: converting prompt to request parameters: %w", err)
	}

	// Braintrust prompts can carry model parameters newer than the version of
	// openai-go in use, and those decode into nothing. Carry them over as extra
	// fields so they still reach the API.
	if extra := unmappedFields(request, params); len(extra) > 0 {
		params.SetExtraFields(extra)
	}

	return params, nil
}

// unmappedFields returns the request fields that the client's own parameter
// type dropped, by round-tripping what it did keep.
func unmappedFields(request map[string]any, params openaigo.ChatCompletionNewParams) map[string]any {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil
	}

	var kept map[string]any
	if err := json.Unmarshal(encoded, &kept); err != nil {
		return nil
	}

	var extra map[string]any
	for key, value := range request {
		if _, ok := kept[key]; ok {
			continue
		}
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[key] = value
	}
	return extra
}
