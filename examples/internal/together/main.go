// Together AI - chat completion, streaming, completion, and embeddings with tracing
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	together "github.com/togethercomputer/together-go"
	"github.com/togethercomputer/together-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracetogether "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/togethercomputer/together"
)

var tracer = otel.Tracer("together-examples")

const chatModel = "meta-llama/Llama-3.3-70B-Instruct-Turbo"
const embeddingModel = "togethercomputer/m2-bert-80M-8k-retrieval"

func userMessage(text string) []together.ChatCompletionNewParamsMessageUnion {
	return []together.ChatCompletionNewParamsMessageUnion{{
		OfChatCompletionNewsMessageChatCompletionUserMessageParam: &together.ChatCompletionNewParamsMessageChatCompletionUserMessageParam{
			Role: "user",
			Content: together.ChatCompletionNewParamsMessageChatCompletionUserMessageParamContentUnion{
				OfString: together.String(text),
			},
		},
	}}
}

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

	client := together.NewClient(
		option.WithAPIKey(os.Getenv("TOGETHER_API_KEY")),
		option.WithMiddleware(tracetogether.NewMiddleware()),
	)

	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/together/main.go")
	defer rootSpan.End()

	chatResp, err := client.Chat.Completions.New(ctx, together.ChatCompletionNewParams{
		Model:    chatModel,
		Messages: userMessage("Say hello"),
	})
	if err != nil {
		log.Fatalf("Chat completion: %v", err)
	}
	fmt.Printf("Chat completion -> %s\n", chatResp.Choices[0].Message.Content)

	stream := client.Chat.Completions.NewStreaming(ctx, together.ChatCompletionNewParams{
		Model:    chatModel,
		Messages: userMessage("Count 1 to 3"),
	})
	fmt.Print("Streaming -> ")
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			fmt.Print(chunk.Choices[0].Delta.Content)
		}
	}
	fmt.Println()
	if err := stream.Err(); err != nil {
		log.Fatalf("Streaming: %v", err)
	}

	completionResp, err := client.Completions.New(ctx, together.CompletionNewParams{
		Model:  together.CompletionNewParamsModel(chatModel),
		Prompt: "The capital of France is",
	})
	if err != nil {
		log.Fatalf("Completion: %v", err)
	}
	fmt.Printf("Completion -> %s\n", completionResp.Choices[0].Text)

	embedResp, err := client.Embeddings.New(ctx, together.EmbeddingNewParams{
		Model: together.EmbeddingNewParamsModel(embeddingModel),
		Input: together.EmbeddingNewParamsInputUnion{OfString: together.String("hello world")},
	})
	if err != nil {
		log.Fatalf("Embeddings: %v", err)
	}
	fmt.Printf("Embeddings -> %d embedding(s), %d dims\n", len(embedResp.Data), len(embedResp.Data[0].Embedding))

	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
