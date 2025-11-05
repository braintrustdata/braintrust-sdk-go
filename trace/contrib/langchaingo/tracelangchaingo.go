// Package langchaingo provides OpenTelemetry tracing for LangChainGo applications.
//
// First, set up tracing with Quickstart (requires BRAINTRUST_API_KEY environment variable):
//
//	// export BRAINTRUST_API_KEY="your-api-key-here"
//	teardown, err := trace.Quickstart()
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer teardown()
//
// Then create a handler and add it to your LangChainGo LLM.
// It's recommended to provide model and provider information for richer traces:
//
//	handler := tracelangchaingo.NewHandlerWithOptions(tracelangchaingo.HandlerOptions{
//		Model:    "gpt-4o-mini",  // The model you're using
//		Provider: "openai",        // The provider (e.g., "openai", "anthropic", "google")
//	})
//	llm, err := openai.New(openai.WithCallback(handler))
//
//	// Your LangChainGo calls will now be automatically traced
//	resp, err := llm.GenerateContent(ctx, []llms.MessageContent{
//		llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
//	})
//
// For basic usage without custom metadata, you can use NewHandler():
//
//	handler := tracelangchaingo.NewHandler()
//	llm, err := openai.New(openai.WithCallback(handler))
package langchaingo

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// spanEntry represents a span with its type in the stack
type spanEntry struct {
	spanType string
	span     trace.Span
	childCtx context.Context // Context with this span for creating nested spans
}

// Handler implements the LangChainGo callbacks.Handler interface to provide
// OpenTelemetry tracing for LangChainGo applications.
type Handler struct {
	mu sync.RWMutex
	// We need to track spans because we can't return the modified context from callbacks.
	// We use a stack per context to handle nested calls (e.g., chain → llm).
	// Each entry stores the span and its child context, enabling proper span hierarchy.
	// When a Start method is called, we use the most recent childCtx as the parent.
	// When an End method is called, we pop the most recent span of that type.
	spans map[context.Context][]spanEntry

	// streamBuffers accumulates streaming chunks by span ID
	// Used to build the complete response when streaming is used
	streamBuffers map[string]*strings.Builder

	// User-provided options
	opts HandlerOptions
}

// HandlerOptions configures the Handler with additional metadata.
type HandlerOptions struct {
	// Model specifies the LLM model being used (e.g., "gpt-4", "claude-3-opus")
	Model string

	// Provider specifies the LLM provider (e.g., "openai", "anthropic", "google")
	// Defaults to "langchain" if not specified
	Provider string

	// Metadata contains additional key-value pairs to include in all spans
	Metadata map[string]interface{}

	// TracerProvider is an optional custom TracerProvider for testing or custom configurations.
	// If not provided, the global otel.GetTracerProvider() is used.
	TracerProvider trace.TracerProvider
}

// NewHandler creates a new Handler for tracing LangChainGo operations.
func NewHandler() *Handler {
	return &Handler{
		spans:         make(map[context.Context][]spanEntry),
		streamBuffers: make(map[string]*strings.Builder),
		opts:          HandlerOptions{},
	}
}

// NewHandlerWithOptions creates a new Handler with custom options.
func NewHandlerWithOptions(opts HandlerOptions) *Handler {
	return &Handler{
		spans:         make(map[context.Context][]spanEntry),
		streamBuffers: make(map[string]*strings.Builder),
		opts:          opts,
	}
}

// pushSpan adds a span to the stack for the given context
func (h *Handler) pushSpan(ctx context.Context, childCtx context.Context, spanType string, span trace.Span) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spans[ctx] = append(h.spans[ctx], spanEntry{
		spanType: spanType,
		span:     span,
		childCtx: childCtx,
	})
}

// getParentContext returns the context to use as parent for new spans.
// Returns the most recent childCtx from the stack (for proper nesting),
// or the original context if the stack is empty.
func (h *Handler) getParentContext(ctx context.Context) context.Context {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stack := h.spans[ctx]
	if len(stack) == 0 {
		return ctx
	}

	// Return the most recent child context for proper nesting
	return stack[len(stack)-1].childCtx
}

// popSpan removes and returns the most recent span of the given type from the stack.
// Returns nil if no matching span is found.
func (h *Handler) popSpan(ctx context.Context, spanType string) trace.Span {
	h.mu.Lock()
	defer h.mu.Unlock()

	stack := h.spans[ctx]
	if len(stack) == 0 {
		return nil
	}

	// Find the most recent span of this type (search backwards)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].spanType == spanType {
			span := stack[i].span
			// Remove this entry from the stack
			h.spans[ctx] = append(stack[:i], stack[i+1:]...)
			// Clean up empty stacks
			if len(h.spans[ctx]) == 0 {
				delete(h.spans, ctx)
			}
			return span
		}
	}

	return nil
}

// tracer returns the configured tracer from handler options or global provider
func (h *Handler) tracer() trace.Tracer {
	tp := h.opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// HandleLLMStart is called at the start of an LLM call with simple string prompts.
func (h *Handler) HandleLLMStart(ctx context.Context, prompts []string) {
	// Use the most recent child context as parent for proper nesting
	parentCtx := h.getParentContext(ctx)
	childCtx, span := h.tracer().Start(parentCtx, "langchain.llm.call")

	// Mark this span as an LLM span
	spanAttrs := map[string]string{
		"type": "llm",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	// Store prompts as input
	if err := internal.SetJSONAttr(span, "braintrust.input_json", prompts); err != nil {
		span.RecordError(err)
	}

	// Store span and child context for later retrieval
	h.pushSpan(ctx, childCtx, "llm", span)
}

// HandleLLMGenerateContentStart is called at the start of a GenerateContent call.
func (h *Handler) HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent) {
	// Use the most recent child context as parent for proper nesting
	parentCtx := h.getParentContext(ctx)
	childCtx, span := h.tracer().Start(parentCtx, "langchain.llm.generate_content")

	// Mark this span as an LLM span
	spanAttrs := map[string]string{
		"type": "llm",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	// Convert messages to OpenAI-standard format
	messages := convertToOpenAIMessages(ms)

	if err := internal.SetJSONAttr(span, "braintrust.input_json", messages); err != nil {
		span.RecordError(err)
	}

	// Store span and child context for later retrieval
	h.pushSpan(ctx, childCtx, "llm", span)
}

// HandleLLMGenerateContentEnd is called when a GenerateContent call completes.
func (h *Handler) HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse) {
	// Retrieve the span from our stack
	span := h.popSpan(ctx, "llm")
	if span == nil {
		// No active span for this context
		return
	}

	// Check if we have accumulated streaming content for this span
	spanID := span.SpanContext().SpanID().String()
	h.mu.Lock()
	streamedContent := ""
	if buffer, exists := h.streamBuffers[spanID]; exists {
		streamedContent = buffer.String()
		delete(h.streamBuffers, spanID) // Clean up
	}
	h.mu.Unlock()

	// Extract output from response
	if res != nil && len(res.Choices) > 0 {
		// Convert to OpenAI-style choice format
		choices := make([]map[string]interface{}, len(res.Choices))
		var metadata map[string]interface{}

		for i, choice := range res.Choices {
			// Use streamed content if available, otherwise use choice.Content
			content := choice.Content
			if streamedContent != "" {
				content = streamedContent
			}

			// Build the message object
			message := map[string]interface{}{
				"role":    "assistant",
				"content": content,
			}

			// Add reasoning content for reasoning models (o1, DeepSeek, etc.)
			if choice.ReasoningContent != "" {
				message["reasoning_content"] = choice.ReasoningContent
			}

			// Add function/tool calls if present
			if choice.FuncCall != nil {
				message["function_call"] = choice.FuncCall
			}
			if len(choice.ToolCalls) > 0 {
				message["tool_calls"] = choice.ToolCalls
			}

			// Build the choice object (OpenAI format)
			choiceObj := map[string]interface{}{
				"index":   i,
				"message": message,
			}

			// Add stop_reason/finish_reason at choice level (OpenAI format)
			if choice.StopReason != "" {
				choiceObj["stop_reason"] = choice.StopReason
				choiceObj["finish_reason"] = choice.StopReason // Also use OpenAI's field name
			}

			choices[i] = choiceObj

			// Extract metadata and metrics from the first choice
			if i == 0 && choice.GenerationInfo != nil {
				metadata = h.extractMetadata(span, choice.GenerationInfo, choice.StopReason)
				h.extractMetrics(span, choice.GenerationInfo)
			}
		}

		if err := internal.SetJSONAttr(span, "braintrust.output_json", choices); err != nil {
			span.RecordError(err)
		}

		// Set metadata if we extracted any
		if metadata != nil {
			if err := internal.SetJSONAttr(span, "braintrust.metadata", metadata); err != nil {
				span.RecordError(err)
			}
		}
	}

	span.End()
}

// HandleLLMError is called when an LLM call results in an error.
func (h *Handler) HandleLLMError(ctx context.Context, err error) {
	// Retrieve the span from our stack
	span := h.popSpan(ctx, "llm")
	if span == nil {
		// No active span for this context
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.End()
}

// extractMetadata extracts rich metadata from generation info
// Returns a metadata map that includes model, provider, and various config parameters
func (h *Handler) extractMetadata(span trace.Span, genInfo map[string]any, stopReason string) map[string]interface{} {
	metadata := map[string]interface{}{}

	// Start with user-provided metadata (lowest priority)
	if h.opts.Metadata != nil {
		for k, v := range h.opts.Metadata {
			metadata[k] = v
		}
	}

	// Set provider (can be overridden by user options or genInfo)
	if h.opts.Provider != "" {
		metadata["provider"] = h.opts.Provider
	} else {
		metadata["provider"] = "langchain"
	}

	// Set model from options if provided
	if h.opts.Model != "" {
		metadata["model"] = h.opts.Model
	}

	// Common metadata fields from various providers (higher priority than user options)
	metadataFields := []string{
		"model", "model_name", "model_id",
		"temperature", "max_tokens", "top_p", "top_k",
		"frequency_penalty", "presence_penalty",
		"stop_sequences", "stop",
		"response_format",
		"tools", "tool_choice",
		"functions", "function_call",
		"id", "system_fingerprint",
		"finish_reason",
	}

	for _, field := range metadataFields {
		if value, exists := genInfo[field]; exists && value != nil {
			metadata[field] = value
		}
	}

	// Add stop reason if present
	if stopReason != "" {
		metadata["stop_reason"] = stopReason
	}

	// Check for model name in common locations (highest priority)
	if _, hasModel := metadata["model"]; !hasModel || metadata["model"] == "" {
		// Try alternative field names
		for _, field := range []string{"model_name", "model_id", "llm_model"} {
			if value, exists := genInfo[field]; exists {
				if modelStr, ok := value.(string); ok && modelStr != "" {
					metadata["model"] = modelStr
					break
				}
			}
		}
	}

	// Extract provider from genInfo (highest priority)
	if provider, exists := genInfo["provider"]; exists {
		if providerStr, ok := provider.(string); ok && providerStr != "" {
			metadata["provider"] = providerStr
		}
	}

	return metadata
}

// extractMetrics attempts to extract token usage metrics from generation info with multiple fallback strategies
func (h *Handler) extractMetrics(span trace.Span, genInfo map[string]any) {
	metrics := make(map[string]int64)

	// Strategy 1: Look for nested "usage" object (most common)
	if usage, ok := genInfo["usage"].(map[string]any); ok {
		extractTokensFromUsage(metrics, usage)
	}

	// Strategy 2: Look for top-level token fields
	extractTopLevelTokens(metrics, genInfo)

	// Strategy 3: Look for token_usage object (some providers)
	if tokenUsage, ok := genInfo["token_usage"].(map[string]any); ok {
		extractTokensFromUsage(metrics, tokenUsage)
	}

	// Strategy 4: Look for llm_output with token_usage (LangChain specific)
	if llmOutput, ok := genInfo["llm_output"].(map[string]any); ok {
		if usage, ok := llmOutput["token_usage"].(map[string]any); ok {
			extractTokensFromUsage(metrics, usage)
		}
	}

	// Calculate total if we have prompt and completion but not total
	if _, hasTotal := metrics["tokens"]; !hasTotal {
		if prompt, hasPrompt := metrics["prompt_tokens"]; hasPrompt {
			if completion, hasCompletion := metrics["completion_tokens"]; hasCompletion {
				metrics["tokens"] = prompt + completion
			}
		}
	}

	// Only set metrics if we found any
	if len(metrics) > 0 {
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			span.RecordError(err)
		}
	}
}

// extractTokensFromUsage extracts token counts from a usage object with various field name formats
func extractTokensFromUsage(metrics map[string]int64, usage map[string]any) {
	tokenFields := map[string][]string{
		"prompt_tokens":     {"prompt_tokens", "input_tokens", "promptTokens", "inputTokens"},
		"completion_tokens": {"completion_tokens", "output_tokens", "completionTokens", "outputTokens", "generated_tokens"},
		"tokens":            {"total_tokens", "totalTokens", "tokens"},
	}

	// Cache token fields
	cacheFields := map[string][]string{
		"prompt_cached_tokens":         {"prompt_cached_tokens", "cache_read_input_tokens", "cached_tokens"},
		"prompt_cache_creation_tokens": {"prompt_cache_creation_tokens", "cache_creation_input_tokens", "cache_write_tokens"},
	}

	// Extract standard token fields
	for targetField, sourceFields := range tokenFields {
		if _, exists := metrics[targetField]; !exists {
			for _, field := range sourceFields {
				if value, ok := usage[field]; ok {
					if ok, i := internal.ToInt64(value); ok && i > 0 {
						metrics[targetField] = i
						break
					}
				}
			}
		}
	}

	// Extract cache token fields
	for targetField, sourceFields := range cacheFields {
		for _, field := range sourceFields {
			if value, ok := usage[field]; ok {
				if ok, i := internal.ToInt64(value); ok && i > 0 {
					metrics[targetField] = i
					break
				}
			}
		}
	}
}

// extractTopLevelTokens looks for token counts at the top level of genInfo
func extractTopLevelTokens(metrics map[string]int64, genInfo map[string]any) {
	topLevelFields := []string{
		// snake_case (some providers)
		"prompt_tokens", "completion_tokens", "total_tokens",
		"input_tokens", "output_tokens",
		// PascalCase (LangChainGo OpenAI, Anthropic)
		"PromptTokens", "CompletionTokens", "TotalTokens",
		"InputTokens", "OutputTokens",
	}

	for _, field := range topLevelFields {
		if value, ok := genInfo[field]; ok {
			if ok, i := internal.ToInt64(value); ok && i > 0 {
				switch field {
				case "input_tokens", "prompt_tokens", "PromptTokens", "InputTokens":
					if _, exists := metrics["prompt_tokens"]; !exists {
						metrics["prompt_tokens"] = i
					}
				case "output_tokens", "completion_tokens", "CompletionTokens", "OutputTokens":
					if _, exists := metrics["completion_tokens"]; !exists {
						metrics["completion_tokens"] = i
					}
				case "total_tokens", "TotalTokens":
					if _, exists := metrics["tokens"]; !exists {
						metrics["tokens"] = i
					}
				}
			}
		}
	}
}

// Chain callbacks

// HandleChainStart is called at the start of a chain execution.
func (h *Handler) HandleChainStart(ctx context.Context, inputs map[string]any) {
	// Use the most recent child context as parent for proper nesting
	parentCtx := h.getParentContext(ctx)
	childCtx, span := h.tracer().Start(parentCtx, "langchain.chain")

	if err := internal.SetJSONAttr(span, "braintrust.input_json", inputs); err != nil {
		span.RecordError(err)
	}

	// Store span and child context for later retrieval
	h.pushSpan(ctx, childCtx, "chain", span)
}

// HandleChainEnd is called when a chain execution completes.
func (h *Handler) HandleChainEnd(ctx context.Context, outputs map[string]any) {
	span := h.popSpan(ctx, "chain")
	if span == nil {
		return
	}

	if err := internal.SetJSONAttr(span, "braintrust.output_json", outputs); err != nil {
		span.RecordError(err)
	}

	span.End()
}

// HandleChainError is called when a chain execution results in an error.
func (h *Handler) HandleChainError(ctx context.Context, err error) {
	span := h.popSpan(ctx, "chain")
	if span == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.End()
}

// Tool callbacks

// HandleToolStart is called at the start of a tool execution.
func (h *Handler) HandleToolStart(ctx context.Context, input string) {
	// Use the most recent child context as parent for proper nesting
	parentCtx := h.getParentContext(ctx)
	childCtx, span := h.tracer().Start(parentCtx, "langchain.tool")

	// Mark this span as a tool span
	spanAttrs := map[string]string{
		"type": "tool",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	if err := internal.SetJSONAttr(span, "braintrust.input_json", input); err != nil {
		span.RecordError(err)
	}

	// Store span and child context for later retrieval
	h.pushSpan(ctx, childCtx, "tool", span)
}

// HandleToolEnd is called when a tool execution completes.
func (h *Handler) HandleToolEnd(ctx context.Context, output string) {
	span := h.popSpan(ctx, "tool")
	if span == nil {
		return
	}

	if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
		span.RecordError(err)
	}

	span.End()
}

// HandleToolError is called when a tool execution results in an error.
func (h *Handler) HandleToolError(ctx context.Context, err error) {
	span := h.popSpan(ctx, "tool")
	if span == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.End()
}

// Agent callbacks

// HandleAgentAction is called when an agent performs an action.
func (h *Handler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	_, span := h.tracer().Start(ctx, "langchain.agent.action")

	actionData := map[string]interface{}{
		"tool":       action.Tool,
		"tool_input": action.ToolInput,
		"log":        action.Log,
	}

	if err := internal.SetJSONAttr(span, "braintrust.input_json", actionData); err != nil {
		span.RecordError(err)
	}

	// Agent actions are instantaneous events, so end immediately
	span.End()
}

// HandleAgentFinish is called when an agent finishes.
func (h *Handler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {
	_, span := h.tracer().Start(ctx, "langchain.agent.finish")

	finishData := map[string]interface{}{
		"return_values": finish.ReturnValues,
		"log":           finish.Log,
	}

	if err := internal.SetJSONAttr(span, "braintrust.output_json", finishData); err != nil {
		span.RecordError(err)
	}

	// Agent finish is an instantaneous event, so end immediately
	span.End()
}

// Retriever callbacks

// HandleRetrieverStart is called at the start of a retrieval operation.
func (h *Handler) HandleRetrieverStart(ctx context.Context, query string) {
	// Use the most recent child context as parent for proper nesting
	parentCtx := h.getParentContext(ctx)
	childCtx, span := h.tracer().Start(parentCtx, "langchain.retriever")

	if err := internal.SetJSONAttr(span, "braintrust.input_json", query); err != nil {
		span.RecordError(err)
	}

	// Store span and child context for later retrieval
	h.pushSpan(ctx, childCtx, "retriever", span)
}

// HandleRetrieverEnd is called when a retrieval operation completes.
func (h *Handler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
	span := h.popSpan(ctx, "retriever")
	if span == nil {
		return
	}

	// Convert documents to a simple format for tracing
	docs := make([]map[string]interface{}, len(documents))
	for i, doc := range documents {
		docs[i] = map[string]interface{}{
			"page_content": doc.PageContent,
			"metadata":     doc.Metadata,
		}
	}

	output := map[string]interface{}{
		"query":     query,
		"documents": docs,
		"count":     len(documents),
	}

	if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
		span.RecordError(err)
	}

	// Add document count as a metric
	metrics := map[string]int64{
		"documents_retrieved": int64(len(documents)),
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		span.RecordError(err)
	}

	span.End()
}

// HandleRetrieverError is called when a retrieval operation results in an error.
func (h *Handler) HandleRetrieverError(ctx context.Context, err error) {
	span := h.popSpan(ctx, "retriever")
	if span == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.End()
}

// Utility callbacks

// HandleText is called to handle text events.
// This creates an instantaneous event span to record the text.
func (h *Handler) HandleText(ctx context.Context, text string) {
	_, span := h.tracer().Start(ctx, "langchain.text")

	if err := internal.SetJSONAttr(span, "braintrust.input_json", text); err != nil {
		span.RecordError(err)
	}

	// Text events are instantaneous, so end immediately
	span.End()
}

// HandleStreamingFunc is called to handle streaming chunks.
// This accumulates chunks for the current LLM span so the final output contains the full streamed content.
func (h *Handler) HandleStreamingFunc(ctx context.Context, chunk []byte) {
	// Get the current span from context to find which LLM call this belongs to
	currentSpan := trace.SpanFromContext(ctx)
	if currentSpan.SpanContext().IsValid() {
		spanID := currentSpan.SpanContext().SpanID().String()

		// Accumulate the chunk
		h.mu.Lock()
		if h.streamBuffers[spanID] == nil {
			h.streamBuffers[spanID] = &strings.Builder{}
		}
		h.streamBuffers[spanID].Write(chunk)
		h.mu.Unlock()
	}
}

// mapRoleToOpenAI converts LangChainGo role names to OpenAI standard role names
func mapRoleToOpenAI(role string) string {
	switch role {
	case "human":
		return "user"
	case "ai":
		return "assistant"
	case "system":
		return "system"
	case "function":
		return "function"
	case "tool":
		return "tool"
	default:
		// Default to user for unknown roles
		return "user"
	}
}

// convertToOpenAIMessages converts LangChainGo MessageContent to OpenAI-standard message format.
// This enables the "Try prompt" button in the Braintrust UI.
func convertToOpenAIMessages(messages []llms.MessageContent) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		// Extract text content from parts
		var contentText string
		var contentParts []map[string]interface{}
		hasMultimodal := false

		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.TextContent:
				if contentText != "" {
					contentText += "\n"
				}
				contentText += p.Text
			case llms.ImageURLContent:
				hasMultimodal = true
				contentParts = append(contentParts, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": p.URL,
					},
				})
			case llms.BinaryContent:
				hasMultimodal = true
				// For binary content, we'll include metadata about it
				contentParts = append(contentParts, map[string]interface{}{
					"type":      "binary",
					"mime_type": p.MIMEType,
					"size":      len(p.Data),
				})
			default:
				// Unknown part type, convert to string representation
				contentText += fmt.Sprintf("%v", part)
			}
		}

		// Build the message in OpenAI format
		// Map LangChainGo roles to OpenAI standard roles
		role := mapRoleToOpenAI(string(msg.Role))
		message := map[string]interface{}{
			"role": role,
		}

		// If we have multimodal content, use content array format
		if hasMultimodal {
			if contentText != "" {
				contentParts = append([]map[string]interface{}{{
					"type": "text",
					"text": contentText,
				}}, contentParts...)
			}
			message["content"] = contentParts
		} else {
			// Simple text-only message
			message["content"] = contentText
		}

		result = append(result, message)
	}

	return result
}

// Ensure Handler implements callbacks.Handler
var _ interface {
	HandleText(ctx context.Context, text string)
	HandleLLMStart(ctx context.Context, prompts []string)
	HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent)
	HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse)
	HandleLLMError(ctx context.Context, err error)
	HandleChainStart(ctx context.Context, inputs map[string]any)
	HandleChainEnd(ctx context.Context, outputs map[string]any)
	HandleChainError(ctx context.Context, err error)
	HandleToolStart(ctx context.Context, input string)
	HandleToolEnd(ctx context.Context, output string)
	HandleToolError(ctx context.Context, err error)
	HandleAgentAction(ctx context.Context, action schema.AgentAction)
	HandleAgentFinish(ctx context.Context, finish schema.AgentFinish)
	HandleRetrieverStart(ctx context.Context, query string)
	HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document)
	HandleRetrieverError(ctx context.Context, err error)
	HandleStreamingFunc(ctx context.Context, chunk []byte)
} = (*Handler)(nil)
