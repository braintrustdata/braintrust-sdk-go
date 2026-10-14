package langchaingo

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// TestContextIdentityOnTransportError verifies the actual LangChainGo callback
// contract when a request fails before receiving a response.
func TestContextIdentityOnTransportError(t *testing.T) {
	var startCtx, endCtx context.Context

	// Create a custom handler that captures context pointers
	handler := &contextCapturingHandler{
		onStart: func(ctx context.Context) { startCtx = ctx },
		onEnd:   func(ctx context.Context) { endCtx = ctx },
	}

	// Fail immediately so the test never touches the network.
	fastFail := &http.Client{Transport: &errorTransport{}}

	llm, err := openai.New(
		openai.WithToken("fake-token"),
		openai.WithHTTPClient(fastFail),
		openai.WithCallback(handler),
	)
	if err != nil {
		t.Fatalf("Failed to create LLM: %v", err)
	}

	ctx := context.Background()

	// A transport failure triggers Start but no End or Error callback in LangChainGo.
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "test"),
	}
	_, err = llm.GenerateContent(ctx, messages)
	if err == nil {
		t.Fatal("Expected transport failure")
	}

	// Verify the same context pointer was used
	if startCtx == nil {
		t.Fatal("Start callback was not called")
	}

	if startCtx != ctx {
		t.Errorf("Start context differs from original: got %p, want %p", startCtx, ctx)
	}
	if endCtx != nil {
		t.Error("Unexpected End or Error callback after transport failure")
	}
}

// errorTransport is an http.RoundTripper that immediately returns an error,
// used to test the dependency's transport-failure callback contract. VCR records
// HTTP responses and cannot reproduce a failure before any response exists.
type errorTransport struct{}

func (e *errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

// contextCapturingHandler implements callbacks.Handler but only tracks contexts
type contextCapturingHandler struct {
	callbacks.SimpleHandler
	onStart func(context.Context)
	onEnd   func(context.Context)
}

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
