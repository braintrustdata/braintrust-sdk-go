package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeStreamPreservesCustomToolTypes(t *testing.T) {
	tests := []struct {
		name         string
		initialType  string
		expectedType string
	}{
		{
			name:         "preserves standard tool_use",
			initialType:  "tool_use",
			expectedType: "tool_use",
		},
		{
			name:         "preserves server_tool_use",
			initialType:  "server_tool_use",
			expectedType: "server_tool_use",
		},
		{
			name:         "preserves mcp_tool_use",
			initialType:  "mcp_tool_use",
			expectedType: "mcp_tool_use",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accumulator := NewClaudeStreamAccumulator()

			// 1. Send content_block_start
			accumulator.Add(map[string]any{
				"type":  "content_block_start",
				"index": int64(0),
				"content_block": map[string]any{
					"type": test.initialType,
					"name": "search",
				},
			})

			// 2. Send input_json_delta chunks
			accumulator.Add(map[string]any{
				"type":  "content_block_delta",
				"index": int64(0),
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"query": `,
				},
			})
			accumulator.Add(map[string]any{
				"type":  "content_block_delta",
				"index": int64(0),
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `"golang"}`,
				},
			})

			// 3. Send content_block_stop
			accumulator.Add(map[string]any{
				"type":  "content_block_stop",
				"index": int64(0),
			})

			// 4. Assert output shape, block type, and parsed JSON arguments
			output := accumulator.Output()
			require.Len(t, output, 1)

			content, ok := output[0]["content"].([]any)
			require.True(t, ok)
			require.Len(t, content, 1)

			block, ok := content[0].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, test.expectedType, block["type"])
			assert.Equal(t, map[string]any{"query": "golang"}, block["input"])
		})
	}
}
