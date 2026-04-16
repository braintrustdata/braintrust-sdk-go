// OpenAI v1 kitchen sink - tests all major OpenAI features with v1 API

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

var tracer = otel.Tracer("openai-v1-examples")

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	client := openai.NewClient(option.WithMiddleware(traceopenai.NewMiddleware()))

	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/openai-v1/main.go")
	defer rootSpan.End()

	for _, example := range []struct {
		name string
		fn   func(context.Context, openai.Client) error
	}{
		{"chat-reasoning", chatReasoning},
		{"chat-completion", chatCompletion},
		{"chat-multi-turn", chatMultiTurn},
		{"chat-streaming", chatStreaming},
		{"chat-tools", chatTools},
		{"chat-streaming-tools", chatStreamingTools},
		{"chat-system-temperature", chatSystemTemperature},
		{"chat-vision", chatVision},
		{"embeddings", embeddingsExample},
		{"embeddings-batch", embeddingsBatchExample},
	} {
		fmt.Printf("%s...\n", example.name)
		exampleCtx, span := tracer.Start(ctx, example.name)
		if err := example.fn(exampleCtx, client); err != nil {
			span.End()
			log.Fatal(err)
		}
		span.End()
	}

	fmt.Println("\nAll OpenAI v1 features tested!")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}

func chatReasoning(ctx context.Context, client openai.Client) error {
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
		Model:               "o4-mini",
		MaxCompletionTokens: openai.Int(200),
		ReasoningEffort:     shared.ReasoningEffortLow,
	})
	if err != nil {
		return err
	}
	output := resp.Choices[0].Message.Content
	if len(output) > 40 {
		output = output[:40] + "..."
	}
	fmt.Printf("  %s\n", output)
	return nil
}

func chatCompletion(ctx context.Context, client openai.Client) error {
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say hello"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		return err
	}
	fmt.Printf("  %s\n", resp.Choices[0].Message.Content)
	return nil
}

func chatMultiTurn(ctx context.Context, client openai.Client) error {
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a helpful assistant."),
			openai.UserMessage("What is the capital of France?"),
			openai.AssistantMessage("The capital of France is Paris."),
			openai.UserMessage("What is its population?"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	if err != nil {
		return err
	}
	fmt.Printf("  %s\n", resp.Choices[0].Message.Content)
	return nil
}

func chatStreaming(ctx context.Context, client openai.Client) error {
	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Count 1 to 3"),
		},
		Model: openai.ChatModelGPT4oMini,
	})
	fmt.Print("  ")
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			fmt.Print(chunk.Choices[0].Delta.Content)
		}
	}
	fmt.Println()
	return stream.Err()
}

func chatTools(ctx context.Context, client openai.Client) error {
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
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
		return err
	}
	if len(resp.Choices) > 0 && len(resp.Choices[0].Message.ToolCalls) > 0 {
		tc := resp.Choices[0].Message.ToolCalls[0]
		fmt.Printf("  Tool call: %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
	} else {
		fmt.Printf("  %s\n", resp.Choices[0].Message.Content)
	}
	return nil
}

func chatStreamingTools(ctx context.Context, client openai.Client) error {
	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
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
	var name, args string
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			tc := chunk.Choices[0].Delta.ToolCalls[0]
			if tc.Function.Name != "" {
				name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				args += tc.Function.Arguments
			}
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	if name != "" {
		fmt.Printf("  Streamed tool call: %s(%s)\n", name, args)
	}
	return nil
}

func chatSystemTemperature(ctx context.Context, client openai.Client) error {
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a pirate. Respond in pirate speak."),
			openai.UserMessage("Hello!"),
		},
		Model:       openai.ChatModelGPT4oMini,
		Temperature: openai.Float(0.9),
	})
	if err != nil {
		return err
	}
	fmt.Printf("  %s\n", resp.Choices[0].Message.Content)
	return nil
}

func chatVision(ctx context.Context, client openai.Client) error {
	// 100x100 red square PNG (base64 encoded)
	redSquare := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAGQAAABkCAIAAAD/gAIDAAABFUlEQVR4nO3OUQkAIABEsetfWiv4Nx4IC7Cd7XvkByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIX4Q4gchfhDiByF+EOIHIReeLesrH9s1agAAAABJRU5ErkJggg=="
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
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
		return err
	}
	fmt.Printf("  %s\n", resp.Choices[0].Message.Content)
	return nil
}

func embeddingsExample(ctx context.Context, client openai.Client) error {
	resp, err := client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("The quick brown fox jumps over the lazy dog"),
		},
		Model: openai.EmbeddingModelTextEmbedding3Small,
	})
	if err != nil {
		return err
	}
	fmt.Printf("  single embedding: %d dims, %d prompt tokens\n", len(resp.Data[0].Embedding), resp.Usage.PromptTokens)
	return nil
}

func embeddingsBatchExample(ctx context.Context, client openai.Client) error {
	resp, err := client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: []string{"hello world", "goodbye world", "braintrust tracing"},
		},
		Model: openai.EmbeddingModelTextEmbedding3Small,
	})
	if err != nil {
		return err
	}
	fmt.Printf("  batch: %d embeddings, %d dims each\n", len(resp.Data), len(resp.Data[0].Embedding))
	return nil
}
