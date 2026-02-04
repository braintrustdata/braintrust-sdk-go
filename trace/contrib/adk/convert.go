package adk

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// convertLLMRequest converts a model.LLMRequest to a more standardized format.
//
// Handles system instruction, role mapping, tool_calls, tool-role messages, text and images.
func convertLLMRequest(req *model.LLMRequest) []map[string]any {
	if req == nil || req.Contents == nil {
		return nil
	}

	result := make([]map[string]any, 0, len(req.Contents)+1)

	// Add system instruction first if present in Config
	if req.Config != nil && req.Config.SystemInstruction != nil {
		systemMsg := convertSystemInstruction(req.Config.SystemInstruction)
		if systemMsg != nil {
			result = append(result, systemMsg)
		}
	}

	if len(req.Contents) == 0 && len(result) == 0 {
		return nil
	}

	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		msg := convertContent(content)
		if msg != nil {
			result = append(result, msg)
		}
	}

	return result
}

// convertSystemInstruction converts system instruction content to a system message.
func convertSystemInstruction(content *genai.Content) map[string]any {
	if content == nil {
		return nil
	}
	var buf strings.Builder
	for _, part := range content.Parts {
		if part != nil && part.Text != "" {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(part.Text)
		}
	}
	if buf.Len() == 0 {
		return nil
	}
	return map[string]any{"role": "system", "content": buf.String()}
}

// convertContent converts one genai.Content to one message (tool, assistant with tool_calls, or content).
func convertContent(content *genai.Content) map[string]any {
	if content == nil {
		return nil
	}

	// Function response → tool-role message (single part drives the message)
	for _, part := range content.Parts {
		if part != nil && part.FunctionResponse != nil {
			name := part.FunctionResponse.Name
			if name == "" {
				name = "unknown"
			}
			respStr := ""
			if len(part.FunctionResponse.Response) > 0 {
				if b, err := json.Marshal(part.FunctionResponse.Response); err == nil {
					respStr = string(b)
				}
			}
			return map[string]any{
				"role":         "tool",
				"tool_call_id": name,
				"content":      respStr,
			}
		}
	}

	contentVal, _ := partsToContent(content.Parts)
	result := map[string]any{
		"role":    content.Role,
		"content": contentVal,
	}

	// Assistant with function calls → message with tool_calls (no function_call in content)
	if result["role"] == "model" {
		result["role"] = "assistant"
		toolCalls := extractToolCallsFromParts(content.Parts)
		if len(toolCalls) > 0 {
			result["tool_calls"] = toolCalls
		}
	}

	// Normal content message
	if result["content"] == nil && result["tool_calls"] == nil {
		return nil
	}
	return result
}

// extractToolCallsFromParts builds tool_calls array from non-nil function call parts.
// Only includes parts that have a non-nil FunctionCall (no spurious tool_calls for null fields).
func extractToolCallsFromParts(parts []*genai.Part) []map[string]any {
	var toolCalls []map[string]any
	for _, part := range parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		argsStr := ""
		if len(part.FunctionCall.Args) > 0 {
			if b, err := json.Marshal(part.FunctionCall.Args); err == nil {
				argsStr = string(b)
			}
		}
		tc := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":      part.FunctionCall.Name,
				"arguments": argsStr,
			},
		}
		id := part.FunctionCall.ID
		if id == "" {
			id = part.FunctionCall.Name
		}
		tc["id"] = id
		toolCalls = append(toolCalls, tc)
	}
	return toolCalls
}

// convertLLMResponse converts a model.LLMResponse to a more standardized format.
// Returns one message with role "assistant", content, optional tool_calls and reasoning.
func convertLLMResponse(resp *model.LLMResponse) []map[string]any {
	if resp == nil || resp.Content == nil {
		return nil
	}

	parts := resp.Content.Parts
	// Do not generate spurious tool_calls when parts have null function_call fields
	toolCalls := extractToolCallsFromParts(parts)
	contentVal, reasoning := partsToContent(parts)

	message := map[string]any{
		"role":    "assistant",
		"content": contentVal,
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if len(reasoning) > 0 {
		message["reasoning"] = reasoning
	}

	return []map[string]any{message}
}

// partsToContent converts parts into content (text + images) and reasoning (thought parts).
// Thought parts are extracted into the reasoning array, while text and images form the content.
// Returns content as a string if text-only, or as an array for mixed content.
func partsToContent(parts []*genai.Part) (content any, reasoning []map[string]any) {
	var contentParts []map[string]any
	var textBuf strings.Builder
	useArray := false

	flushText := func() {
		if textBuf.Len() > 0 {
			contentParts = append(contentParts, map[string]any{"type": "text", "text": textBuf.String()})
			textBuf.Reset()
		}
	}

	for i, part := range parts {
		if part == nil {
			continue
		}
		// Function call/response handled elsewhere (tool_calls or tool message)
		if part.FunctionCall != nil || part.FunctionResponse != nil {
			continue
		}
		if part.Thought && part.Text != "" {
			useArray = true
			flushText()
			reasoning = append(reasoning, map[string]any{
				"id":      fmt.Sprintf("reasoning-%d", i),
				"content": part.Text,
			})
			continue
		}
		if part.Text != "" {
			if useArray {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": part.Text})
			} else {
				if textBuf.Len() > 0 {
					textBuf.WriteByte('\n')
				}
				textBuf.WriteString(part.Text)
			}
			continue
		}
		if part.InlineData != nil {
			useArray = true
			flushText()
			contentParts = append(contentParts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": fmt.Sprintf("data:%s;base64,%s",
						part.InlineData.MIMEType,
						base64.StdEncoding.EncodeToString(part.InlineData.Data)),
				},
			})
		}
	}

	if useArray {
		flushText()
		if len(contentParts) == 0 {
			return "", reasoning
		}
		if len(contentParts) == 1 && contentParts[0]["type"] == "text" {
			return contentParts[0]["text"], reasoning
		}
		return contentParts, reasoning
	}
	return textBuf.String(), reasoning
}
