// OpenAI kitchen sink - tests all major OpenAI features with minimal code. It
// uses v2 of the openai API.

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/conversations"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/responses"
	"github.com/openai/openai-go/v2/shared"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-internal-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	client := openai.NewClient(option.WithMiddleware(traceopenai.NewMiddleware()))

	// Create a root span to wrap all examples
	tracer := otel.Tracer("openai-v2-examples")
	ctx, rootSpan := tracer.Start(context.Background(), "openai-v2-examples")
	defer rootSpan.End()

	// Responses API with reasoning (for reasoning models like GPT-5)
	fmt.Println("Responses API with reasoning...")
	reasonResp, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("What is the capital of France?")},
		Model: "gpt-5", // Use GPT-5 reasoning model
		Reasoning: shared.ReasoningParam{
			Effort:  shared.ReasoningEffortLow,
			Summary: shared.ReasoningSummaryAuto,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	output := reasonResp.OutputText()
	if len(output) > 40 {
		output = output[:40] + "..."
	}
	fmt.Printf("✓ %s (reasoning params sent)\n", output)

	// Chat completion
	fmt.Println("Chat completion...")
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hello"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ %s\n", resp.Choices[0].Message.Content)

	// Multiple messages (conversation history)
	fmt.Println("Multiple messages...")
	multiResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a helpful assistant."),
			openai.UserMessage("What is the capital of France?"),
			openai.AssistantMessage("The capital of France is Paris."),
			openai.UserMessage("What is its population?"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ %s\n", multiResp.Choices[0].Message.Content)

	// Streaming chat completion
	fmt.Println("Streaming...")
	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Count 1 to 3"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	fmt.Print("✓ ")
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			fmt.Print(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Println()

	// Chat with tools
	fmt.Println("Chat with tools...")
	toolResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in San Francisco?"),
		},
		Model: openai.ChatModelGPT4oMini,
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        "get_weather",
				Description: openai.String("Get the current weather in a location"),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state, e.g. San Francisco, CA",
						},
					},
					"required": []string{"location"},
				},
			}),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if len(toolResp.Choices) > 0 && len(toolResp.Choices[0].Message.ToolCalls) > 0 {
		toolCall := toolResp.Choices[0].Message.ToolCalls[0]
		fmt.Printf("✓ Tool call: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
	} else {
		fmt.Printf("✓ Response: %s\n", toolResp.Choices[0].Message.Content)
	}

	// Streaming with tools
	fmt.Println("Streaming with tools...")
	toolStream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in Tokyo?"),
		},
		Model: openai.ChatModelGPT4oMini,
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        "get_weather",
				Description: openai.String("Get the current weather in a location"),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state or country",
						},
					},
					"required": []string{"location"},
				},
			}),
		},
	})
	var toolCallName string
	var toolCallArgs string
	for toolStream.Next() {
		chunk := toolStream.Current()
		if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			tc := chunk.Choices[0].Delta.ToolCalls[0]
			if tc.Function.Name != "" {
				toolCallName = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				toolCallArgs += tc.Function.Arguments
			}
		}
	}
	if err := toolStream.Err(); err != nil {
		log.Fatal(err)
	}
	if toolCallName != "" {
		fmt.Printf("✓ Streamed tool call: %s(%s)\n", toolCallName, toolCallArgs)
	}

	// Responses API
	fmt.Println("Responses API...")
	respAPI, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("Recommend pizza in NYC")},
		Model: openai.ChatModelGPT4,
	})
	if err != nil {
		log.Fatal(err)
	}
	output7 := respAPI.OutputText()
	if len(output7) > 50 {
		output7 = output7[:50] + "..."
	}
	fmt.Printf("✓ %s\n", output7)

	// Streaming responses
	fmt.Println("Streaming responses...")
	respStream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Model: openai.ChatModelGPT4,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("Quick coffee rec")},
	})
	var final string
	for respStream.Next() {
		data := respStream.Current()
		if data.Text != "" {
			final = data.Text
		}
	}
	if err := respStream.Err(); err != nil {
		log.Fatal(err)
	}
	if len(final) > 30 {
		final = final[:30] + "..."
	}
	fmt.Printf("✓ %s\n", final)

	// Conversations API (new in v2)
	fmt.Println("Conversations API...")
	conv, err := client.Conversations.New(ctx, conversations.ConversationNewParams{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ Conversation created: %s\n", conv.ID)

	// Models.List (untraced endpoint)
	fmt.Println("Models.List...")
	models, err := client.Models.List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ Models.List succeeded: %d models\n", len(models.Data))

	// Vision - image with text
	fmt.Println("Vision with image...")
	// 100x100 red square PNG (base64 encoded)
	redSquare := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAGQAAABkCAIAAAD/gAIDAAABFUlEQVR4nO3OUQkAIABEsetfWiv4Nx4IC7Cd7XvkByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIReeLesrH9s1agAAAABJRU5ErkJggg=="
	visionResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("What color is this image?"),
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: redSquare,
				}),
			}),
		},
		Model: openai.ChatModelGPT4o,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ %s\n", visionResp.Choices[0].Message.Content)

	fmt.Println("\n✅ All OpenAI features tested!")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
