package adk

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestConvertLLMRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      *model.LLMRequest
		expected []map[string]any
	}{
		{
			name: "simple text message",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role:  "user",
						Parts: []*genai.Part{{Text: "Hello, world!"}},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "user",
					"content": "Hello, world!",
				},
			},
		},
		{
			name: "multiple messages with role mapping",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role:  "user",
						Parts: []*genai.Part{{Text: "What is 2+2?"}},
					},
					{
						Role:  "model",
						Parts: []*genai.Part{{Text: "2+2 equals 4."}},
					},
					{
						Role:  "user",
						Parts: []*genai.Part{{Text: "Thanks!"}},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "user",
					"content": "What is 2+2?",
				},
				{
					"role":    "assistant",
					"content": "2+2 equals 4.",
				},
				{
					"role":    "user",
					"content": "Thanks!",
				},
			},
		},
		{
			name: "multiple text parts",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role: "user",
						Parts: []*genai.Part{
							{Text: "First part"},
							{Text: "Second part"},
						},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "user",
					"content": "First part\nSecond part",
				},
			},
		},
		{
			name:     "nil request",
			req:      nil,
			expected: nil,
		},
		{
			name: "empty contents",
			req: &model.LLMRequest{
				Model:    "gemini-2.0-flash",
				Contents: []*genai.Content{},
			},
			expected: nil,
		},
		{
			name: "system role",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role:  "system",
						Parts: []*genai.Part{{Text: "You are a helpful assistant."}},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "system",
					"content": "You are a helpful assistant.",
				},
			},
		},
		{
			name: "system instruction in config",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Config: &genai.GenerateContentConfig{
					SystemInstruction: &genai.Content{
						Parts: []*genai.Part{{Text: "You are a helpful assistant."}},
					},
				},
				Contents: []*genai.Content{
					{
						Role:  "user",
						Parts: []*genai.Part{{Text: "Hello!"}},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "system",
					"content": "You are a helpful assistant.",
				},
				{
					"role":    "user",
					"content": "Hello!",
				},
			},
		},
		{
			name: "system instruction only (no contents)",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Config: &genai.GenerateContentConfig{
					SystemInstruction: &genai.Content{
						Parts: []*genai.Part{{Text: "You are a helpful assistant."}},
					},
				},
				Contents: []*genai.Content{},
			},
			expected: []map[string]any{
				{
					"role":    "system",
					"content": "You are a helpful assistant.",
				},
			},
		},
		{
			name: "inline data (multimodal)",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role: "user",
						Parts: []*genai.Part{
							{Text: "What's in this image?"},
							{InlineData: &genai.Blob{
								MIMEType: "image/jpeg",
								Data:     []byte("fake-image-data"),
							}},
						},
					},
				},
			},
			expected: []map[string]any{
				{
					"role": "user",
					"content": []map[string]any{
						{
							"type": "text",
							"text": "What's in this image?",
						},
						{
							"type": "image_url",
							"image_url": map[string]any{
								"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("fake-image-data")),
							},
						},
					},
				},
			},
		},
		{
			name: "reasoning (thought parts)",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role: "model",
						Parts: []*genai.Part{
							{Thought: true, Text: "Let me think step by step..."},
							{Text: "The answer is 4."},
						},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "The answer is 4.",
				},
			},
		},
		{
			name: "function calls",
			req: &model.LLMRequest{
				Model: "gemini-2.0-flash",
				Contents: []*genai.Content{
					{
						Role: "model",
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									ID:   "call_abc",
									Name: "get_weather",
									Args: map[string]any{"location": "SF"},
								},
							},
						},
					},
					{
						Role: "user",
						Parts: []*genai.Part{
							{
								FunctionResponse: &genai.FunctionResponse{
									Name:     "get_weather",
									Response: map[string]any{"temp": 72, "unit": "F"},
								},
							},
						},
					},
				},
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{
						{
							"type": "function",
							"id":   "call_abc",
							"function": map[string]any{
								"name":      "get_weather",
								"arguments": `{"location":"SF"}`,
							},
						},
					},
				},
				{
					"role":         "tool",
					"tool_call_id": "get_weather",
					"content":      `{"temp":72,"unit":"F"}`,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLLMRequest(tt.req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertLLMResponse(t *testing.T) {
	tests := []struct {
		name     string
		resp     *model.LLMResponse
		expected []map[string]any
	}{
		{
			name: "simple text response",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: "Hello, world!"}},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "Hello, world!",
				},
			},
		},
		{
			name: "multiple text parts",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "First part"},
						{Text: "Second part"},
					},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "First part\nSecond part",
				},
			},
		},
		{
			name:     "nil response",
			resp:     nil,
			expected: nil,
		},
		{
			name: "nil content",
			resp: &model.LLMResponse{
				Content:      nil,
				FinishReason: "STOP",
			},
			expected: nil,
		},
		{
			name: "empty parts",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "",
				},
			},
		},
		{
			name: "no finish reason",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: "Response"}},
				},
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "Response",
				},
			},
		},
		{
			name: "multimodal (text and image)",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "Here's the image analysis:"},
						{InlineData: &genai.Blob{
							MIMEType: "image/png",
							Data:     []byte("test-image-data"),
						}},
					},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role": "assistant",
					"content": []map[string]any{
						{
							"type": "text",
							"text": "Here's the image analysis:",
						},
						{
							"type": "image_url",
							"image_url": map[string]any{
								"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("test-image-data")),
							},
						},
					},
				},
			},
		},
		{
			name: "reasoning and function call",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Thought: true, Text: "I need to call get_weather."},
						{
							FunctionCall: &genai.FunctionCall{
								Name: "get_weather",
								Args: map[string]any{"location": "Boston"},
							},
						},
					},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "",
					"reasoning": []map[string]any{
						{
							"id":      "reasoning-0",
							"content": "I need to call get_weather.",
						},
					},
					"tool_calls": []map[string]any{
						{
							"type": "function",
							"id":   "get_weather",
							"function": map[string]any{
								"name":      "get_weather",
								"arguments": `{"location":"Boston"}`,
							},
						},
					},
				},
			},
		},
		{
			name: "no spurious tool_calls when part has null function_call",
			resp: &model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Thought: true, Text: "**Analyzing the Pattern**\n\nLet me think..."},
						{Text: "This is a classic sequence! The formula is $a_n = n^2 + n$"},
					},
				},
				FinishReason: "STOP",
			},
			expected: []map[string]any{
				{
					"role":    "assistant",
					"content": "This is a classic sequence! The formula is $a_n = n^2 + n$",
					"reasoning": []map[string]any{
						{
							"id":      "reasoning-0",
							"content": "**Analyzing the Pattern**\n\nLet me think...",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLLMResponse(tt.resp)
			assert.Equal(t, tt.expected, result)
		})
	}
}
