package langchaingo

import (
	"context"
	"fmt"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/schema"
)

// TestContextIdentity verifies that LangChainGo passes the same context pointer
// to both Start and End callbacks. This is critical for our span tracking approach.
func TestContextIdentity(t *testing.T) {
	var startCtx, endCtx context.Context

	// Create a custom handler that captures context pointers
	handler := &contextCapturingHandler{
		onStart: func(ctx context.Context) {
			startCtx = ctx
			fmt.Printf("Start ctx: %p\n", ctx)
		},
		onEnd: func(ctx context.Context) {
			endCtx = ctx
			fmt.Printf("End ctx: %p\n", ctx)
		},
	}

	// Create an OpenAI client with our handler
	// Note: This test doesn't actually call the API, we're just testing the callback flow
	llm, err := openai.New(
		openai.WithToken("fake-token"),
		openai.WithCallback(handler),
	)
	if err != nil {
		t.Fatalf("Failed to create LLM: %v", err)
	}

	// Create a context
	ctx := context.Background()
	fmt.Printf("Original ctx: %p\n", ctx)

	// Make a call (will fail but that's OK, we just need to trigger callbacks)
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "test"),
	}
	_, _ = llm.GenerateContent(ctx, messages)

	// Verify the same context pointer was used
	if startCtx == nil {
		t.Fatal("Start callback was not called")
	}

	// Check if contexts are the same pointer
	if startCtx != ctx {
		t.Logf("WARNING: Start context (%p) differs from original (%p)", startCtx, ctx)
	}

	if endCtx != nil && endCtx != startCtx {
		t.Errorf("PROBLEM: End context (%p) differs from start context (%p)", endCtx, startCtx)
		t.Error("Our span tracking approach will not work if LangChainGo creates derived contexts")
	}
}

// contextCapturingHandler implements callbacks.Handler but only tracks contexts
type contextCapturingHandler struct {
	onStart func(context.Context)
	onEnd   func(context.Context)
}

func (h *contextCapturingHandler) HandleText(ctx context.Context, text string)          {}
func (h *contextCapturingHandler) HandleLLMStart(ctx context.Context, prompts []string) {}
func (h *contextCapturingHandler) HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent) {
	if h.onStart != nil {
		h.onStart(ctx)
	}
}
func (h *contextCapturingHandler) HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse) {
	if h.onEnd != nil {
		h.onEnd(ctx)
	}
}
func (h *contextCapturingHandler) HandleLLMError(ctx context.Context, err error) {
	if h.onEnd != nil {
		h.onEnd(ctx)
	}
}
func (h *contextCapturingHandler) HandleChainStart(ctx context.Context, inputs map[string]any)      {}
func (h *contextCapturingHandler) HandleChainEnd(ctx context.Context, outputs map[string]any)       {}
func (h *contextCapturingHandler) HandleChainError(ctx context.Context, err error)                  {}
func (h *contextCapturingHandler) HandleToolStart(ctx context.Context, input string)                {}
func (h *contextCapturingHandler) HandleToolEnd(ctx context.Context, output string)                 {}
func (h *contextCapturingHandler) HandleToolError(ctx context.Context, err error)                   {}
func (h *contextCapturingHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {}
func (h *contextCapturingHandler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {}
func (h *contextCapturingHandler) HandleRetrieverStart(ctx context.Context, query string)           {}
func (h *contextCapturingHandler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
}
func (h *contextCapturingHandler) HandleStreamingFunc(ctx context.Context, chunk []byte) {}
