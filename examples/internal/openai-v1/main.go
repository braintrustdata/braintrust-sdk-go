// OpenAI v1 kitchen sink - tests all major OpenAI features with v1 API

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
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
	tracer := otel.Tracer("openai-v1-examples")
	ctx, rootSpan := tracer.Start(context.Background(), "openai-v1-examples")
	defer rootSpan.End()

	// 1. Simple chat completion
	fmt.Println("1. Simple chat completion...")
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

	// 2. Multiple messages (conversation history)
	fmt.Println("2. Multiple messages...")
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

	// 3. Streaming chat completion
	fmt.Println("3. Streaming...")
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

	// 4. Chat with tools
	fmt.Println("4. Chat with tools...")
	toolResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in San Francisco?"),
		},
		Model: openai.ChatModelGPT4oMini,
		Tools: []openai.ChatCompletionToolParam{
			{
				Type: "function",
				Function: openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("Get the current weather in a location"),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city and state, e.g. San Francisco, CA",
							},
						},
						"required": []string{"location"},
					},
				},
			},
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

	// 5. Streaming with tools
	fmt.Println("5. Streaming with tools...")
	toolStream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What's the weather in Tokyo?"),
		},
		Model: openai.ChatModelGPT4oMini,
		Tools: []openai.ChatCompletionToolParam{
			{
				Type: "function",
				Function: openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("Get the current weather in a location"),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city and state or country",
							},
						},
						"required": []string{"location"},
					},
				},
			},
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

	// 6. System message with temperature
	fmt.Println("6. System message with temperature...")
	sysResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a pirate. Respond in pirate speak."),
			openai.UserMessage("Hello!"),
		},
		Model:       openai.ChatModelGPT4oMini,
		Temperature: openai.Float(0.9),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ %s\n", sysResp.Choices[0].Message.Content)

	// 7. Vision - image with text
	fmt.Println("7. Vision with image...")
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

	fmt.Println("\n✅ All OpenAI v1 features tested!")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
