package internal

import (
	"encoding/json"
	"strings"
)

// ClaudeStreamAccumulator incrementally reduces Claude Messages API stream
// events into one assistant message and merged usage data.
type ClaudeStreamAccumulator struct {
	contentBlocks map[int]map[string]any
	builders      map[int]*strings.Builder
	usage         map[string]any
	stopReason    any
	model         string
	maxIndex      int
}

// NewClaudeStreamAccumulator creates an empty Claude stream accumulator.
func NewClaudeStreamAccumulator() *ClaudeStreamAccumulator {
	return &ClaudeStreamAccumulator{
		contentBlocks: make(map[int]map[string]any),
		builders:      make(map[int]*strings.Builder),
		usage:         make(map[string]any),
		maxIndex:      -1,
	}
}

// Add consumes one decoded Claude event. It reports whether the event contains
// generated text, reasoning, or tool-input content suitable for TTFT.
func (a *ClaudeStreamAccumulator) Add(event map[string]any) bool {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "message_start":
		if message, ok := event["message"].(map[string]any); ok {
			if model, ok := message["model"].(string); ok {
				a.model = model
			}
			a.mergeUsage(message["usage"])
		}
	case "content_block_start":
		index, ok := claudeEventIndex(event)
		if !ok {
			return false
		}
		block, _ := event["content_block"].(map[string]any)
		a.contentBlocks[index] = cloneClaudeMap(block)
		a.builders[index] = &strings.Builder{}
		a.trackIndex(index)
	case "content_block_delta":
		return a.addDelta(event)
	case "message_delta":
		if delta, ok := event["delta"].(map[string]any); ok {
			if stopReason, exists := delta["stop_reason"]; exists {
				a.stopReason = stopReason
			}
		}
		a.mergeUsage(event["usage"])
	}
	return false
}

func (a *ClaudeStreamAccumulator) addDelta(event map[string]any) bool {
	index, ok := claudeEventIndex(event)
	if !ok {
		return false
	}
	a.trackIndex(index)
	block := a.contentBlocks[index]
	if block == nil {
		block = make(map[string]any)
		a.contentBlocks[index] = block
	}
	builder := a.builders[index]
	if builder == nil {
		builder = &strings.Builder{}
		a.builders[index] = builder
	}

	delta, _ := event["delta"].(map[string]any)
	switch delta["type"] {
	case "text_delta":
		text, ok := delta["text"].(string)
		if !ok {
			return false
		}
		block["type"] = "text"
		builder.WriteString(text)
		return text != ""
	case "input_json_delta":
		partial, ok := delta["partial_json"].(string)
		if !ok {
			return false
		}
		block["type"] = "tool_use"
		builder.WriteString(partial)
		return partial != ""
	case "thinking_delta":
		thinking, ok := delta["thinking"].(string)
		if !ok {
			return false
		}
		block["type"] = "thinking"
		builder.WriteString(thinking)
		return thinking != ""
	case "signature_delta":
		if signature, ok := delta["signature"].(string); ok {
			block["signature"] = signature
		}
	case "citations_delta":
		if citation, ok := delta["citation"].(map[string]any); ok {
			citations, _ := block["citations"].([]any)
			block["citations"] = append(citations, citation)
			block["type"] = "text"
		}
	}
	return false
}

// Output returns the accumulated assistant message, or nil when no content
// blocks were observed.
func (a *ClaudeStreamAccumulator) Output() []map[string]any {
	if len(a.contentBlocks) == 0 {
		return nil
	}
	content := make([]any, 0, len(a.contentBlocks))
	for index := 0; index <= a.maxIndex; index++ {
		block := a.contentBlocks[index]
		if block == nil {
			continue
		}
		text := ""
		if builder := a.builders[index]; builder != nil {
			text = builder.String()
		}
		switch block["type"] {
		case "text":
			block["text"] = text
		case "tool_use":
			var input any
			if err := json.Unmarshal([]byte(text), &input); err == nil {
				block["input"] = input
			} else {
				block["input"] = text
			}
		case "thinking":
			block["thinking"] = text
		}
		content = append(content, block)
	}
	return []map[string]any{{"role": "assistant", "content": content}}
}

// Usage returns the merged usage fields from message_start and message_delta.
func (a *ClaudeStreamAccumulator) Usage() map[string]any { return a.usage }

// StopReason returns the final provider stop reason, if present.
func (a *ClaudeStreamAccumulator) StopReason() any { return a.stopReason }

// Model returns the model resolved from message_start, if present.
func (a *ClaudeStreamAccumulator) Model() string { return a.model }

func (a *ClaudeStreamAccumulator) mergeUsage(value any) {
	usage, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, item := range usage {
		a.usage[key] = item
	}
}

func (a *ClaudeStreamAccumulator) trackIndex(index int) {
	if index > a.maxIndex {
		a.maxIndex = index
	}
}

func claudeEventIndex(event map[string]any) (int, bool) {
	ok, value := ToInt64(event["index"])
	return int(value), ok
}

func cloneClaudeMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
