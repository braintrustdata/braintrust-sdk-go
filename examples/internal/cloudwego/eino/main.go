// Internal golden-test example for CloudWeGo Eino tracing.
//
// Covers: OpenAI + Anthropic, streaming + non-streaming, and tool calling
// with compose.ToolsNode to verify agentic (tool) span capture.
//
// To run:
//
//	export BRAINTRUST_API_KEY="your-api-key"
//	export OPENAI_API_KEY="your-openai-key"
//	export ANTHROPIC_API_KEY="your-anthropic-key"
//	go run .
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	einoembed "github.com/cloudwego/eino-ext/components/embedding/openai"
	einoclaude "github.com/cloudwego/eino-ext/components/model/claude"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	traceeino "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino"
)

func main() {
	fmt.Println("=== Braintrust CloudWeGo Eino Tracing Example ===")

	// Step 1: Initialize Braintrust tracing
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Step 2: Register the Braintrust Eino handler globally
	handler := traceeino.NewHandlerWithOptions(traceeino.HandlerOptions{
		TracerProvider: tp,
	})
	callbacks.AppendGlobalHandlers(handler)

	tracer := otel.Tracer("eino-example")
	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/cloudwego/eino/main.go")
	defer rootSpan.End()

	// --- OpenAI ---
	openaiModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		Model:  "gpt-4o-mini",
		APIKey: os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	// Example 1: OpenAI non-streaming
	fmt.Println("\n1. OpenAI non-streaming")
	fmt.Println("-----------------------")
	resp, err := openaiModel.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is 2+2? Answer with just the number."},
	})
	if err != nil {
		log.Fatalf("Generate failed: %v", err)
	}
	fmt.Printf("Response: %s\n", resp.Content)
	handler.Wait()

	// Example 2: OpenAI streaming
	fmt.Println("\n2. OpenAI streaming")
	fmt.Println("-------------------")
	reader, err := openaiModel.Stream(ctx, []*schema.Message{
		{Role: schema.User, Content: "Count from 1 to 3, one number per word."},
	})
	if err != nil {
		log.Fatalf("Stream failed: %v", err)
	}
	fmt.Print("Streaming output: ")
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("Stream Recv error: %v", err)
		}
		fmt.Print(chunk.Content)
	}
	reader.Close()
	fmt.Println()
	handler.Wait()

	// --- Anthropic ---
	if anthropicKey := os.Getenv("ANTHROPIC_API_KEY"); anthropicKey != "" {
		claudeModel, err := einoclaude.NewChatModel(ctx, &einoclaude.Config{
			Model:     "claude-haiku-4-5-20251001",
			APIKey:    anthropicKey,
			MaxTokens: 1024,
		})
		if err != nil {
			log.Fatal(err)
		}

		// Example 3: Anthropic non-streaming
		fmt.Println("\n3. Anthropic non-streaming")
		fmt.Println("--------------------------")
		resp, err = claudeModel.Generate(ctx, []*schema.Message{
			{Role: schema.User, Content: "What is the capital of France? Answer with just the city name."},
		})
		if err != nil {
			log.Fatalf("Anthropic Generate failed: %v", err)
		}
		fmt.Printf("Response: %s\n", resp.Content)
		handler.Wait()

		// Example 4: Anthropic streaming
		fmt.Println("\n4. Anthropic streaming")
		fmt.Println("----------------------")
		claudeReader, err := claudeModel.Stream(ctx, []*schema.Message{
			{Role: schema.User, Content: "List the first 3 planets in our solar system, one per line."},
		})
		if err != nil {
			log.Fatalf("Anthropic Stream failed: %v", err)
		}
		fmt.Print("Streaming output:\n")
		for {
			chunk, err := claudeReader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Fatalf("Anthropic Stream Recv error: %v", err)
			}
			fmt.Print(chunk.Content)
		}
		claudeReader.Close()
		fmt.Println()
		handler.Wait()
	} else {
		fmt.Println("\n3-4. Anthropic skipped (ANTHROPIC_API_KEY not set)")
	}

	// --- Tool calling (agentic span) ---
	fmt.Println("\n5. Tool calling (agentic span)")
	fmt.Println("------------------------------")

	// Define a simple multiply tool
	type multiplyInput struct {
		A float64 `json:"a" jsonschema_description:"First number"`
		B float64 `json:"b" jsonschema_description:"Second number"`
	}
	multiplyTool, err := utils.InferTool("multiply", "Multiply two numbers together",
		func(_ context.Context, input multiplyInput) (string, error) {
			result := input.A * input.B
			out, _ := json.Marshal(map[string]float64{"result": result})
			return string(out), nil
		})
	if err != nil {
		log.Fatal(err)
	}

	// Bind the tool using its full schema (includes parameter definitions)
	toolInfo, err := multiplyTool.Info(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := openaiModel.BindTools([]*schema.ToolInfo{toolInfo}); err != nil {
		log.Fatal(err)
	}

	// Create ToolsNode
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []tool.BaseTool{multiplyTool},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Run the full model → tool → model turn inside an Eino graph. The graph
	// becomes a parent task span, with each model call and tool execution as a child.
	agentTurn := compose.InvokableLambda(func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
		firstResp, err := openaiModel.Generate(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("initial model call: %w", err)
		}
		if len(firstResp.ToolCalls) == 0 {
			return firstResp, nil
		}

		toolResults, err := toolsNode.Invoke(ctx, firstResp)
		if err != nil {
			return nil, fmt.Errorf("tool execution: %w", err)
		}
		followUp := append(append([]*schema.Message{}, messages...), firstResp)
		followUp = append(followUp, toolResults...)
		return openaiModel.Generate(ctx, followUp)
	})

	agentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
	if err := agentGraph.AddLambdaNode("agent_turn", agentTurn); err != nil {
		log.Fatal(err)
	}
	if err := agentGraph.AddEdge(compose.START, "agent_turn"); err != nil {
		log.Fatal(err)
	}
	if err := agentGraph.AddEdge("agent_turn", compose.END); err != nil {
		log.Fatal(err)
	}
	agent, err := agentGraph.Compile(ctx)
	if err != nil {
		log.Fatal(err)
	}
	finalResp, err := agent.Invoke(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is 6 multiplied by 7?"},
	})
	if err != nil {
		log.Fatalf("Agent turn failed: %v", err)
	}
	fmt.Printf("Tool call result: %s\n", finalResp.Content)
	handler.Wait()

	// Example 6: OpenAI embeddings
	fmt.Println("\n6. OpenAI embeddings")
	fmt.Println("--------------------")
	embedder, err := einoembed.NewEmbedder(ctx, &einoembed.EmbeddingConfig{
		APIKey: os.Getenv("OPENAI_API_KEY"),
		Model:  "text-embedding-3-small",
	})
	if err != nil {
		log.Fatalf("embedder init failed: %v", err)
	}
	vectors, err := embedder.EmbedStrings(ctx, []string{
		"The quick brown fox jumps over the lazy dog",
		"braintrust tracing",
	})
	if err != nil {
		log.Fatalf("EmbedStrings failed: %v", err)
	}
	fmt.Printf("Embedded %d texts, %d dims each\n", len(vectors), len(vectors[0]))
	handler.Wait()

	fmt.Printf("\nView traces: %s\n", bt.Permalink(rootSpan))
}
