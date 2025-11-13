package langchaingo

import (
	"context"
	"fmt"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// TestContextAnalysis inspects what's in the context during callbacks
func TestContextAnalysis(t *testing.T) {
	tp, _ := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	// Create a parent span
	ctx := context.Background()
	ctx, parentSpan := tracer.Start(ctx, "test-parent")
	defer parentSpan.End()

	fmt.Printf("Parent span ID: %s\n", parentSpan.SpanContext().SpanID().String())
	fmt.Printf("Parent trace ID: %s\n", parentSpan.SpanContext().TraceID().String())

	// Start a chain
	handler.HandleChainStart(ctx, map[string]any{"test": "chain"})

	// Check what span is in the context
	spanFromCtx := trace.SpanFromContext(ctx)
	fmt.Printf("Span from ctx after chain start: %s (parent? %v)\n",
		spanFromCtx.SpanContext().SpanID().String(),
		spanFromCtx.SpanContext().SpanID() == parentSpan.SpanContext().SpanID())

	// Now start an LLM call with the same context
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// Check what span is in the context now
	spanFromCtx2 := trace.SpanFromContext(ctx)
	fmt.Printf("Span from ctx after LLM start: %s (same as before? %v)\n",
		spanFromCtx2.SpanContext().SpanID().String(),
		spanFromCtx2.SpanContext().SpanID() == spanFromCtx.SpanContext().SpanID())

	// Clean up
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "Hi"}},
	})
	handler.HandleChainEnd(ctx, map[string]any{"result": "done"})
}

// TestNestedCalls tests that nested calls (chain → llm) work correctly
func TestNestedCalls(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	ctx, parentSpan := tracer.Start(ctx, "test-parent")
	parentSpanID := parentSpan.SpanContext().SpanID()

	// Start a chain
	handler.HandleChainStart(ctx, map[string]any{"task": "summarize"})

	// Start an LLM call within the chain (same context)
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Summarize this"),
	}
	handler.HandleLLMGenerateContentStart(ctx, messages)

	// End the LLM call
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "Summary"}},
	})

	// End the chain
	handler.HandleChainEnd(ctx, map[string]any{"result": "done"})

	parentSpan.End()

	spans := exporter.Flush()
	fmt.Printf("\nNested calls - Total spans: %d\n", len(spans))

	// Should have: parent + chain + llm = 3 spans
	if len(spans) != 3 {
		t.Errorf("Expected 3 spans (parent, chain, llm), got %d", len(spans))
	}

	// Find each span and verify hierarchy
	var chainSpan, llmSpan *oteltest.Span
	for i := range spans {
		span := &spans[i]
		switch span.Name() {
		case "langchain.chain":
			chainSpan = span
		case "langchain.llm.generate_content":
			llmSpan = span
		}
	}

	if chainSpan == nil {
		t.Fatal("Missing chain span")
	}
	if llmSpan == nil {
		t.Fatal("Missing LLM span")
	}

	// Verify parent-child relationships using the Stub's Parent field
	chainParentID := chainSpan.Stub.Parent.SpanID()
	chainSpanID := chainSpan.Stub.SpanContext.SpanID()
	llmParentID := llmSpan.Stub.Parent.SpanID()
	llmSpanID := llmSpan.Stub.SpanContext.SpanID()

	fmt.Printf("Parent span ID:     %s\n", parentSpanID.String())
	fmt.Printf("Chain parent ID:    %s (should match parent)\n", chainParentID.String())
	fmt.Printf("Chain span ID:      %s\n", chainSpanID.String())
	fmt.Printf("LLM parent ID:      %s (should match chain)\n", llmParentID.String())
	fmt.Printf("LLM span ID:        %s\n", llmSpanID.String())

	// Chain's parent should be the test parent
	if chainParentID != parentSpanID {
		t.Errorf("Chain span parent mismatch: expected %s, got %s", parentSpanID, chainParentID)
	}

	// LLM's parent should be the chain span
	if llmParentID != chainSpanID {
		t.Errorf("LLM span parent mismatch: expected %s (chain), got %s", chainSpanID, llmParentID)
	}

	fmt.Println("✓ Proper span hierarchy confirmed: parent → chain → llm")
}

// TestParallelCalls simulates parallel LLM calls with the same context.
// The stack approach preserves both spans even though they use the same context.
func TestParallelCalls(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer(t.Name())

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	ctx := context.Background()
	ctx, parentSpan := tracer.Start(ctx, "test-parent")

	// Start two LLM calls with the same context (simulating parallel calls)
	messages1 := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Call 1"),
	}
	messages2 := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Call 2"),
	}

	handler.HandleLLMGenerateContentStart(ctx, messages1)
	handler.HandleLLMGenerateContentStart(ctx, messages2)

	// End both calls - with stack approach, both should work (LIFO)
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "Response 2"}}, // Ends second call
	})
	handler.HandleLLMGenerateContentEnd(ctx, &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "Response 1"}}, // Ends first call
	})

	parentSpan.End()

	spans := exporter.Flush()
	fmt.Printf("Parallel calls - Total spans: %d\n", len(spans))
	for _, span := range spans {
		fmt.Printf("  - %s\n", span.Name())
	}

	// With stack approach, we should get both LLM spans
	if len(spans) != 3 {
		t.Errorf("Expected 3 spans (parent + 2 LLM calls), got %d", len(spans))
	}
}
