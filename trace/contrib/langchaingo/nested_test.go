package langchaingo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestLLMSpanStackLIFO(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
	ctx := context.Background()
	calls := []struct{ input, output string }{
		{"Call 1", "Response 1"},
		{"Call 2", "Response 2"},
	}

	// These callbacks run sequentially on one context, with nested LLM spans.
	// The real chain → LLM hierarchy is covered by TestAgentExecutorSpanHierarchy.
	for _, call := range calls {
		handler.HandleLLMGenerateContentStart(ctx, []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, call.input)})
	}
	for i := len(calls) - 1; i >= 0; i-- {
		handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: calls[i].output}}})
	}

	spans := exporter.Flush()
	require.Len(t, spans, len(calls))
	for i := range spans {
		call := calls[len(calls)-1-i]
		spans[i].AssertNameIs("langchain.llm.generate_content")
		assert.Equal(t, []any{map[string]any{"role": "user", "content": call.input}}, spans[i].Input())
		assert.Equal(t, []any{map[string]any{
			"index": float64(0), "message": map[string]any{"role": "assistant", "content": call.output},
		}}, spans[i].Output())
	}
	spans[0].AssertChildOf(&spans[1])
	assert.Empty(t, handler.spans)
}
