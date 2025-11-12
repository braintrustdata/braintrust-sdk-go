// This example demonstrates comprehensive LangChainGo tracing with Braintrust.
// It shows how to trace:
// - Simple LLM calls
// - Multi-turn conversations
// - Chain executions
// - Tool usage
// - Agent actions
// - Retriever operations
// - Streaming chunks
// - System prompts
// - Temperature variations
// - Very short max tokens
// - Prefill
// - Stop sequences
// - Long context
// - Metadata passing
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracelangchaingo "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo"
)

func main() {
	fmt.Println("=== Braintrust LangChainGo Comprehensive Example ===")

	// Initialize Braintrust tracing with blocking login to ensure permalinks work immediately
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Create the Braintrust callback handler with model information
	// This enables the "Try prompt" button in the Braintrust UI
	handler := tracelangchaingo.NewHandlerWithOptions(tracelangchaingo.HandlerOptions{
		TracerProvider: tp,
		Model:          "gpt-4o-mini",
		Provider:       "openai",
	})

	// Create LangChainGo OpenAI LLM with callback
	llm, err := openai.New(openai.WithCallback(handler))
	if err != nil {
		log.Fatal(err)
	}

	// Get a tracer instance
	tracer := otel.Tracer("langchaingo-example")

	// Create a root span that will contain all examples
	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/langchaingo/comprehensive")

	// Example 1: Simple LLM call
	fmt.Println("1. Simple LLM Call")
	fmt.Println("------------------")
	simpleLLMCall(ctx, tracer, llm)

	// Example 2: Multi-turn conversation
	fmt.Println("\n2. Multi-turn Conversation")
	fmt.Println("--------------------------")
	multiTurnConversation(ctx, tracer, llm)

	// Example 3: Simulated chain execution
	fmt.Println("\n3. Simulated Chain Execution")
	fmt.Println("----------------------------")
	simulatedChain(ctx, tracer, handler, llm)

	// Example 4: Simulated tool usage
	fmt.Println("\n4. Simulated Tool Usage")
	fmt.Println("-----------------------")
	simulatedTool(ctx, tracer, handler)

	// Example 5: Simulated agent actions
	fmt.Println("\n5. Simulated Agent Actions")
	fmt.Println("--------------------------")
	simulatedAgent(ctx, tracer, handler, llm)

	// Example 6: Simulated retriever
	fmt.Println("\n6. Simulated Retriever")
	fmt.Println("----------------------")
	simulatedRetriever(ctx, tracer, handler)

	// Example 7: Streaming chunks
	fmt.Println("\n7. Streaming Chunks")
	fmt.Println("-------------------")
	streamingChunks(ctx, tracer, handler)

	// Example 8: System prompt
	fmt.Println("\n8. System Prompt")
	fmt.Println("----------------")
	systemPrompt(ctx, tracer, llm)

	// Example 9: Temperature variations
	fmt.Println("\n9. Temperature Variations")
	fmt.Println("-------------------------")
	temperatureVariations(ctx, tracer, handler)

	// Example 10: Very short max tokens
	fmt.Println("\n10. Very Short Max Tokens")
	fmt.Println("-------------------------")
	veryShortMaxTokens(ctx, tracer, llm)

	// Example 11: Prefill
	fmt.Println("\n11. Prefill")
	fmt.Println("-----------")
	prefillExample(ctx, tracer, llm)

	// Example 12: Stop sequences
	fmt.Println("\n12. Stop Sequences")
	fmt.Println("------------------")
	stopSequencesExample(ctx, tracer, llm)

	// Example 13: Long context
	fmt.Println("\n13. Long Context")
	fmt.Println("----------------")
	longContextExample(ctx, tracer, llm)

	// Example 14: Metadata passing
	fmt.Println("\n14. Metadata Passing")
	fmt.Println("--------------------")
	metadataExample(ctx, tracer, llm)

	// End the root span
	rootSpan.End()

	fmt.Println("\n=== All examples completed! ===")

	// Get a link to the root span in Braintrust
	link := bt.Permalink(rootSpan)
	if link != "" {
		fmt.Printf("\nView all traces: %s\n", link)
	}
}

// simpleLLMCall demonstrates basic LLM tracing
func simpleLLMCall(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "simple-question")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "What is the capital of France?"),
	}

	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Q: What is the capital of France?\n")
		fmt.Printf("A: %s\n", resp.Choices[0].Content)
	}
}

// multiTurnConversation demonstrates tracing a conversation with context
func multiTurnConversation(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "multi-turn-conversation")
	defer span.End()

	// First turn
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "I'm planning a trip to Paris. What should I see?"),
	}

	resp1, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Turn 1:\n")
	fmt.Printf("User: I'm planning a trip to Paris. What should I see?\n")
	if len(resp1.Choices) > 0 {
		response1 := resp1.Choices[0].Content
		// Truncate for display
		if len(response1) > 100 {
			response1 = response1[:100] + "..."
		}
		fmt.Printf("Assistant: %s\n\n", response1)

		// Second turn - add context
		messages = append(messages,
			llms.TextParts(llms.ChatMessageTypeAI, resp1.Choices[0].Content),
			llms.TextParts(llms.ChatMessageTypeHuman, "How many days should I spend there?"),
		)
	}

	resp2, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Turn 2:\n")
	fmt.Printf("User: How many days should I spend there?\n")
	if len(resp2.Choices) > 0 {
		response2 := resp2.Choices[0].Content
		if len(response2) > 100 {
			response2 = response2[:100] + "..."
		}
		fmt.Printf("Assistant: %s\n", response2)
	}
}

// simulatedChain demonstrates chain callback tracing using a real LLMChain
func simulatedChain(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "summarize-chain")
	defer span.End()

	// Create a real prompt template
	prompt := prompts.NewPromptTemplate(
		"Summarize this in 5 words: {{.text}}",
		[]string{"text"},
	)

	// Create a real LLMChain with our callback handler
	chain := chains.NewLLMChain(llm, prompt, chains.WithCallback(handler))

	// Call the chain with real input
	inputText := "LangChain is a framework for developing applications powered by language models."
	result, err := chains.Call(ctx, chain, map[string]any{
		"text": inputText,
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	// Display the result
	fmt.Printf("Input: %s\n", inputText)
	if summary, ok := result["text"].(string); ok {
		fmt.Printf("Summary: %s\n", summary)
	}
}

// simulatedTool demonstrates tool callback tracing using a real Calculator tool
func simulatedTool(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler) {
	ctx, span := tracer.Start(parentCtx, "calculator-tool")
	defer span.End()

	// Create a real Calculator tool with our callback handler
	calculator := tools.Calculator{
		CallbacksHandler: handler,
	}

	// Call the tool with a real math expression
	input := "25 * 4"
	fmt.Printf("Tool: %s\n", calculator.Name())
	fmt.Printf("Input: %s\n", input)

	result, err := calculator.Call(ctx, input)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Output: %s\n", result)
}

// simulatedAgent demonstrates agent callback tracing using a real agent executor
func simulatedAgent(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "agent-workflow")
	defer span.End()

	// Create tools for the agent (Calculator)
	agentTools := []tools.Tool{
		tools.Calculator{},
	}

	// Create a real agent executor with callback handler
	agent := agents.NewOneShotAgent(
		llm,
		agentTools,
		agents.WithCallbacksHandler(handler),
	)
	executor := agents.NewExecutor(
		agent,
		agents.WithCallbacksHandler(handler),
		agents.WithMaxIterations(3),
	)

	// Ask the agent a question that requires calculation
	question := "What is 25 multiplied by 4?"
	fmt.Printf("Question: %s\n", question)

	result, err := chains.Call(ctx, executor, map[string]any{
		"input": question,
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if output, ok := result["output"].(string); ok {
		fmt.Printf("Answer: %s\n", output)
	}
}

// inMemoryRetriever is a simple retriever implementation with pre-populated documents
type inMemoryRetriever struct {
	documents []schema.Document
}

func (r *inMemoryRetriever) GetRelevantDocuments(ctx context.Context, query string) ([]schema.Document, error) {
	// Simple filtering: return documents that contain keywords from the query
	// In a real retriever, you'd use embeddings or more sophisticated search
	return r.documents, nil
}

// simulatedRetriever demonstrates retriever callback tracing using a real retriever
func simulatedRetriever(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler) {
	ctx, span := tracer.Start(parentCtx, "document-retrieval")
	defer span.End()

	// Create a real in-memory retriever with pre-populated documents
	retriever := &inMemoryRetriever{
		documents: []schema.Document{
			{
				PageContent: "LangChain is a framework for developing applications powered by language models.",
				Metadata:    map[string]any{"source": "docs.langchain.com", "page": 1},
			},
			{
				PageContent: "It enables applications that are context-aware and can reason about data.",
				Metadata:    map[string]any{"source": "docs.langchain.com", "page": 2},
			},
		},
	}

	query := "What is LangChain?"
	fmt.Printf("Query: %s\n", query)

	// Manually wrap with callbacks (retrievers don't auto-call callbacks)
	handler.HandleRetrieverStart(ctx, query)

	documents, err := retriever.GetRelevantDocuments(ctx, query)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	handler.HandleRetrieverEnd(ctx, query, documents)

	fmt.Printf("Retrieved %d documents\n", len(documents))
}

// streamingChunks demonstrates streaming chunk tracing with real OpenAI streaming
func streamingChunks(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler) {
	ctx, span := tracer.Start(parentCtx, "streaming-llm-call")
	defer span.End()

	// Create LLM client with callback handler
	llm, err := openai.New(openai.WithCallback(handler))
	if err != nil {
		log.Printf("Error creating LLM: %v", err)
		return
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Say hello in 5 words or less"),
	}

	// Use WithStreamingFunc to enable streaming
	// The handler's HandleStreamingFunc will be called for each chunk automatically
	streamingFunc := func(ctx context.Context, chunk []byte) error {
		fmt.Print(string(chunk))
		return nil
	}

	fmt.Print("Streaming: ")
	resp, err := llm.GenerateContent(ctx, messages, llms.WithStreamingFunc(streamingFunc))
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}
	fmt.Println()

	// The response will contain the full accumulated content
	// Chunks were automatically accumulated by the Handler's HandleStreamingFunc
	if len(resp.Choices) > 0 {
		fmt.Printf("(Full response accumulated: %d chars)\n", len(resp.Choices[0].Content))
	}
}

// systemPrompt demonstrates system prompt handling
func systemPrompt(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "system-prompt")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "You are a pirate. Always respond in pirate speak."),
		llms.TextParts(llms.ChatMessageTypeHuman, "Tell me about the weather."),
	}

	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		content := resp.Choices[0].Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		fmt.Printf("Response: %s\n", content)
	}
}

// temperatureVariations demonstrates different temperature/top_p settings
func temperatureVariations(parentCtx context.Context, tracer oteltrace.Tracer, handler *tracelangchaingo.Handler) {
	ctx, span := tracer.Start(parentCtx, "temperature-variations")
	defer span.End()

	configs := []struct {
		temperature float64
		topP        float64
	}{
		{0.0, 1.0},
		{1.0, 0.9},
		{0.7, 0.95},
	}

	for _, config := range configs {
		fmt.Printf("  temp=%.1f, top_p=%.2f: ", config.temperature, config.topP)

		llm, err := openai.New(
			openai.WithCallback(handler),
			openai.WithModel("gpt-4o-mini"),
		)
		if err != nil {
			log.Printf("Error: %v", err)
			continue
		}

		messages := []llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "Say something creative about coding."),
		}

		resp, err := llm.GenerateContent(ctx, messages,
			llms.WithTemperature(config.temperature),
			llms.WithTopP(config.topP),
		)
		if err != nil {
			log.Printf("Error: %v", err)
			continue
		}

		if len(resp.Choices) > 0 {
			content := resp.Choices[0].Content
			if len(content) > 50 {
				content = content[:50] + "..."
			}
			fmt.Printf("%s\n", content)
		}
	}
}

// veryShortMaxTokens demonstrates very short token limits
func veryShortMaxTokens(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "very-short-max-tokens")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "What is AI?"),
	}

	resp, err := llm.GenerateContent(ctx, messages, llms.WithMaxTokens(5))
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Response (5 tokens): %s\n", resp.Choices[0].Content)
		fmt.Printf("Stop reason: %s\n", resp.Choices[0].StopReason)
	}
}

// prefillExample demonstrates prefilling with AI message
func prefillExample(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "prefill")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Write a haiku about coding."),
		llms.TextParts(llms.ChatMessageTypeAI, "Here is a haiku:"),
	}

	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Prefilled: Here is a haiku: %s\n", resp.Choices[0].Content)
	}
}

// stopSequencesExample demonstrates stop sequences/stop words
func stopSequencesExample(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "stop-sequences")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "Count from 1 to 10."),
	}

	// Stop at "5" to demonstrate stop sequences
	resp, err := llm.GenerateContent(ctx, messages, llms.WithStopWords([]string{"5"}))
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Response (stopped at '5'): %s\n", resp.Choices[0].Content)
		fmt.Printf("Stop reason: %s\n", resp.Choices[0].StopReason)
	}
}

// longContextExample demonstrates handling long context
func longContextExample(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "long-context")
	defer span.End()

	// Create a long context by repeating text
	longText := "This is a test sentence. "
	for i := 0; i < 100; i++ {
		longText += "The quick brown fox jumps over the lazy dog. "
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, longText+"What animal was mentioned?"),
	}

	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Input length: %d chars\n", len(longText))
		fmt.Printf("Response: %s\n", resp.Choices[0].Content)
	}
}

// metadataExample demonstrates passing custom metadata
func metadataExample(parentCtx context.Context, tracer oteltrace.Tracer, llm *openai.LLM) {
	ctx, span := tracer.Start(parentCtx, "metadata-passing")
	defer span.End()

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "What is 2+2?"),
	}

	// Pass custom metadata - this will be included in the request/trace
	metadata := map[string]any{
		"user_id":    "user-123",
		"session_id": "session-456",
		"experiment": "metadata-test",
	}

	resp, err := llm.GenerateContent(ctx, messages, llms.WithMetadata(metadata))
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Response with metadata: %s\n", resp.Choices[0].Content)
		fmt.Printf("Metadata: user_id=%s, session_id=%s, experiment=%s\n",
			metadata["user_id"], metadata["session_id"], metadata["experiment"])
	}
}
