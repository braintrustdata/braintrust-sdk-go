// AWS Bedrock Runtime kitchen sink - exercises the full Converse feature surface.
//
// Requires an AWS environment with Bedrock access and a Claude inference profile
// available in the configured region (see haikuModelID below).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracebedrockruntime "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime"
)

// Inference-profile ID for the Claude Haiku 4.5 on-demand cross-region profile.
const haikuModelID = "us.anthropic.claude-haiku-4-5-20251001-v1:0"

var tracer = otel.Tracer("bedrockruntime-examples")

// BedrockBot demonstrates the Bedrock Runtime API with tracing.
type BedrockBot struct {
	client *bedrockruntime.Client
}

func newBedrockBot(client *bedrockruntime.Client) *BedrockBot {
	return &BedrockBot{client: client}
}

// messages demonstrates non-streaming Converse with a system prompt and
// inference parameters.
func (b *BedrockBot) messages(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "messages")
	defer span.End()

	fmt.Println("\n=== Example 1: Messages ===")

	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(haikuModelID),
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: "You are a helpful assistant."},
		},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What is the capital of France?"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(1024),
			Temperature: aws.Float32(0.7),
		},
	})
	if err != nil {
		return fmt.Errorf("messages: %w", err)
	}
	fmt.Printf("  %s\n", firstText(out.Output))
	return nil
}

// tools demonstrates non-streaming Converse with tool use and extra inference
// parameters (stop sequences + model-specific top_k via additionalModelRequestFields).
func (b *BedrockBot) tools(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "tools")
	defer span.End()

	fmt.Println("\n=== Example 2: Tools ===")

	schema := document.NewLazyDocument(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{
				"type":        "string",
				"description": "The city and state",
			},
		},
		"required": []string{"location"},
	})

	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(haikuModelID),
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: "You are a helpful weather assistant."},
		},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What's the weather in SF?"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:     aws.Int32(1024),
			Temperature:   aws.Float32(0.7),
			StopSequences: []string{"END"},
		},
		// top_k isn't in the base InferenceConfig so pass it via additionalModelRequestFields.
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{"top_k": 50}),
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{&types.ToolMemberToolSpec{Value: types.ToolSpecification{
				Name:        aws.String("get_weather"),
				Description: aws.String("Get weather for a city"),
				InputSchema: &types.ToolInputSchemaMemberJson{Value: schema},
			}}},
		},
	})
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}

	if m, ok := out.Output.(*types.ConverseOutputMemberMessage); ok {
		for _, block := range m.Value.Content {
			switch v := block.(type) {
			case *types.ContentBlockMemberText:
				fmt.Printf("  Text: %s\n", v.Value)
			case *types.ContentBlockMemberToolUse:
				fmt.Printf("  Tool: %s\n", aws.ToString(v.Value.Name))
				var input any
				if v.Value.Input != nil {
					_ = v.Value.Input.UnmarshalSmithyDocument(&input)
				}
				fmt.Printf("  Input: %v\n", input)
			}
		}
	}
	fmt.Printf("  stop_reason=%s\n", out.StopReason)
	return nil
}

// streaming demonstrates ConverseStream with tool-use triggering.
func (b *BedrockBot) streaming(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "streaming")
	defer span.End()

	fmt.Println("\n=== Example 3: Streaming ===")

	schema := document.NewLazyDocument(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{"type": "string", "description": "The city and country"},
		},
		"required": []string{"location"},
	})

	out, err := b.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(haikuModelID),
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: "You are a helpful assistant."},
		},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What's the weather in Tokyo and tell me a joke."}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(1024),
			Temperature: aws.Float32(0.8),
		},
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{&types.ToolMemberToolSpec{Value: types.ToolSpecification{
				Name:        aws.String("get_weather"),
				Description: aws.String("Get weather for a city"),
				InputSchema: &types.ToolInputSchemaMemberJson{Value: schema},
			}}},
		},
	})
	if err != nil {
		return fmt.Errorf("streaming: %w", err)
	}

	stream := out.GetStream()
	defer stream.Close() //nolint:errcheck

	fmt.Print("  ")
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case *types.ConverseStreamOutputMemberContentBlockStart:
			if tu, ok := e.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
				fmt.Printf("\n  [Tool: %s] ", aws.ToString(tu.Value.Name))
			}
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if d, ok := e.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				fmt.Print(d.Value)
			}
		}
	}
	fmt.Println()
	return stream.Err()
}

// streamingCitations demonstrates Converse with a document that has citations enabled.
func (b *BedrockBot) streamingCitations(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "streaming-citations")
	defer span.End()

	fmt.Println("\n=== Example 3b: Streaming Citations ===")

	out, err := b.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(haikuModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberDocument{Value: types.DocumentBlock{
					Name:   aws.String("france-facts"),
					Format: types.DocumentFormatTxt,
					Source: &types.DocumentSourceMemberText{
						Value: "France's capital is Paris. Paris lies on the Seine River. The Louvre Museum is also in Paris.",
					},
					Citations: &types.CitationsConfig{Enabled: aws.Bool(true)},
				}},
				&types.ContentBlockMemberText{
					Value: "Use only the provided document. In one sentence, what is the capital of France? Include citations in the answer.",
				},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(256)},
	})
	if err != nil {
		return fmt.Errorf("streaming-citations: %w", err)
	}

	stream := out.GetStream()
	defer stream.Close() //nolint:errcheck

	var response strings.Builder
	citationCount := 0
	for ev := range stream.Events() {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			switch delta := d.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				response.WriteString(delta.Value)
			case *types.ContentBlockDeltaMemberCitation:
				citationCount++
				fmt.Printf("  Citation %d: %T\n", citationCount, delta.Value)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("streaming-citations: %w", err)
	}
	fmt.Printf("  Response: %s\n", response.String())
	fmt.Printf("  Total citations: %d\n", citationCount)
	return nil
}

// extendedThinking demonstrates Claude's extended-thinking on Bedrock Converse.
func (b *BedrockBot) extendedThinking(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "extended-thinking")
	defer span.End()

	fmt.Println("\n=== Example 4: Extended Thinking ===")

	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(haikuModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What is the capital of France and why is it historically significant?"},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(16000),
			Temperature: aws.Float32(1.0),
		},
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": 2000,
			},
		}),
	})
	if err != nil {
		return fmt.Errorf("extended-thinking: %w", err)
	}

	if m, ok := out.Output.(*types.ConverseOutputMemberMessage); ok {
		for _, block := range m.Value.Content {
			switch v := block.(type) {
			case *types.ContentBlockMemberReasoningContent:
				if r, ok := v.Value.(*types.ReasoningContentBlockMemberReasoningText); ok {
					text := aws.ToString(r.Value.Text)
					if len(text) > 100 {
						text = text[:100] + "..."
					}
					fmt.Printf("  Thinking: %s\n", text)
				}
			case *types.ContentBlockMemberText:
				fmt.Printf("  Response: %s\n", v.Value)
			}
		}
	}
	return nil
}

// streamingExtendedThinking demonstrates streaming with extended thinking enabled.
func (b *BedrockBot) streamingExtendedThinking(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "streaming-extended-thinking")
	defer span.End()

	fmt.Println("\n=== Example 4b: Streaming Extended Thinking ===")

	out, err := b.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(haikuModelID),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What is 27 * 453?"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(16000),
			Temperature: aws.Float32(1.0),
		},
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": 2000,
			},
		}),
	})
	if err != nil {
		return fmt.Errorf("streaming-extended-thinking: %w", err)
	}

	stream := out.GetStream()
	defer stream.Close() //nolint:errcheck

	var thinkingText, responseText strings.Builder
	for ev := range stream.Events() {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			switch delta := d.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				responseText.WriteString(delta.Value)
			case *types.ContentBlockDeltaMemberReasoningContent:
				if r, ok := delta.Value.(*types.ReasoningContentBlockDeltaMemberText); ok {
					thinkingText.WriteString(r.Value)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("streaming-extended-thinking: %w", err)
	}
	thinkStr := thinkingText.String()
	if len(thinkStr) > 100 {
		thinkStr = thinkStr[:100] + "..."
	}
	fmt.Printf("  Thinking: %s\n", thinkStr)
	fmt.Printf("  Response: %s\n", responseText.String())
	return nil
}

// vision demonstrates an image content block via Converse.
func (b *BedrockBot) vision(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "vision")
	defer span.End()

	fmt.Println("\n=== Example 5: Vision ===")

	imgBytes := newRedPNG(256, 256)

	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(haikuModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What color is this image?"},
				&types.ContentBlockMemberImage{Value: types.ImageBlock{
					Format: types.ImageFormatPng,
					Source: &types.ImageSourceMemberBytes{Value: imgBytes},
				}},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(1024)},
	})
	if err != nil {
		return fmt.Errorf("vision: %w", err)
	}
	fmt.Printf("  %s\n", firstText(out.Output))
	return nil
}

// invokeModelClaude demonstrates the low-level InvokeModel API with Claude's
// Messages-style body. Token metrics are normalized via the Claude branch.
func (b *BedrockBot) invokeModelClaude(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "invoke-model-claude")
	defer span.End()

	fmt.Println("\n=== Example 6: InvokeModel (Claude) ===")

	body, err := json.Marshal(map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        100,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "Say hi."}},
		}},
	})
	if err != nil {
		return fmt.Errorf("invoke-model marshal: %w", err)
	}
	out, err := b.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(haikuModelID),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("invoke-model: %w", err)
	}
	fmt.Printf("  %s\n", truncate(string(out.Body), 200))
	return nil
}

// invokeModelClaudeStream demonstrates stream accumulation and TTFT capture
// for the low-level Claude InvokeModelWithResponseStream API.
func (b *BedrockBot) invokeModelClaudeStream(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "invoke-model-claude-stream")
	defer span.End()

	fmt.Println("\n=== Example 7: InvokeModelWithResponseStream (Claude) ===")

	body, err := json.Marshal(map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        100,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "Count to three."}},
		}},
	})
	if err != nil {
		return fmt.Errorf("invoke-model-stream marshal: %w", err)
	}
	out, err := b.client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     aws.String(haikuModelID),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("invoke-model-stream: %w", err)
	}
	defer out.GetStream().Close() //nolint:errcheck
	for event := range out.GetStream().Events() {
		if chunk, ok := event.(*types.ResponseStreamMemberChunk); ok {
			fmt.Printf("  %s\n", truncate(string(chunk.Value.Bytes), 200))
		}
	}
	if err := out.GetStream().Err(); err != nil {
		return fmt.Errorf("invoke-model-stream events: %w", err)
	}
	return nil
}

// firstText extracts the first text block from a Converse output.
func firstText(out types.ConverseOutput) string {
	m, ok := out.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	for _, block := range m.Value.Content {
		if t, ok := block.(*types.ContentBlockMemberText); ok {
			return t.Value
		}
	}
	return ""
}

// newRedPNG builds a w×h solid-red PNG in memory so the vision example
// doesn't depend on any external asset.
func newRedPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	red := color.RGBA{R: 220, G: 20, B: 60, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, red)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func main() {
	fmt.Println("Braintrust Bedrock Runtime Tracing Examples")
	fmt.Println("==========================================")

	if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
		log.Println("skipping bedrockruntime internal example: AWS_REGION not set")
		return
	}

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

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("skipping bedrockruntime internal example: %v", err)
		return
	}

	client := bedrockruntime.NewFromConfig(cfg, tracebedrockruntime.NewMiddleware())
	bot := newBedrockBot(client)

	ctx, rootSpan := tracer.Start(ctx, "examples/internal/bedrockruntime/main.go")
	defer rootSpan.End()

	fmt.Println("\nBedrock Converse Examples")
	fmt.Println("=========================")
	fmt.Println("Demonstrating: system prompts, tools, streaming, citations, extended thinking, vision, and InvokeModel")

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"messages", bot.messages},
		{"tools", bot.tools},
		{"streaming", bot.streaming},
		{"streaming-citations", bot.streamingCitations},
		{"extended-thinking", bot.extendedThinking},
		{"streaming-extended-thinking", bot.streamingExtendedThinking},
		{"vision", bot.vision},
		{"invoke-model-claude", bot.invokeModelClaude},
		{"invoke-model-claude-stream", bot.invokeModelClaudeStream},
	}
	for _, s := range steps {
		if err := s.fn(ctx); err != nil {
			log.Printf("  [%s] %v", s.name, err)
		}
	}

	fmt.Println("\n=== Tracing Complete ===")
	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
