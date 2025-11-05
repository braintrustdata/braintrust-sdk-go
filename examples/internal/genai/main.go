// Google Gemini kitchen sink - tests all the Gemini features with minimal code
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"google.golang.org/genai"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

var tracer = otel.Tracer("genai-examples")

// GeminiBot demonstrates using Google Gemini API with tracing
type GeminiBot struct {
	client *genai.Client
}

func newGeminiBot(client *genai.Client) *GeminiBot {
	return &GeminiBot{
		client: client,
	}
}

// basicText demonstrates basic text generation
func (g *GeminiBot) basicText(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "basic-text")
	defer span.End()

	fmt.Println("\n=== Example 1: Basic Text Generation ===")

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("What is the capital of France?"),
		nil,
	)
	if err != nil {
		return fmt.Errorf("basic text error: %v", err)
	}

	fmt.Printf("  %s\n", resp.Text())
	return nil
}

// withSystemInstruction demonstrates system instructions and parameters
func (g *GeminiBot) withSystemInstruction(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "with-system-instruction")
	defer span.End()

	fmt.Println("\n=== Example 2: System Instructions & Parameters ===")

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Explain quantum computing."),
		&genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("You are a helpful science educator. Be concise and clear.", ""),
			Temperature:       genai.Ptr[float32](0.7),
			TopP:              genai.Ptr[float32](0.9),
			TopK:              genai.Ptr[float32](40),
			MaxOutputTokens:   150,
		},
	)
	if err != nil {
		return fmt.Errorf("system instruction error: %v", err)
	}

	fmt.Printf("  %s\n", resp.Text())
	return nil
}

// multiTurnConversation demonstrates multi-turn conversation
func (g *GeminiBot) multiTurnConversation(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "multi-turn")
	defer span.End()

	fmt.Println("\n=== Example 3: Multi-Turn Conversation ===")

	// Simulate a conversation history
	history := append(
		genai.Text("What is 5 + 3?"),
		&genai.Content{
			Role:  "model",
			Parts: []*genai.Part{{Text: "5 + 3 equals 8."}},
		},
	)
	history = append(history, genai.Text("And what is that times 2?")...)

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		history,
		&genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.5),
		},
	)
	if err != nil {
		return fmt.Errorf("multi-turn error: %v", err)
	}

	fmt.Printf("  %s\n", resp.Text())
	return nil
}

// streaming demonstrates streaming responses
func (g *GeminiBot) streaming(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "streaming")
	defer span.End()

	fmt.Println("\n=== Example 4: Streaming ===")

	iter := g.client.Models.GenerateContentStream(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Count from 1 to 5 slowly, with one number per line."),
		&genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.9),
		},
	)

	fmt.Print("  ")
	for resp, err := range iter {
		if err != nil {
			fmt.Printf("\nError: %v\n", err)
			break
		}
		fmt.Print(resp.Text())
	}
	fmt.Println()

	return nil
}

// functionCalling demonstrates tool/function calling
func (g *GeminiBot) functionCalling(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "function-calling")
	defer span.End()

	fmt.Println("\n=== Example 5: Function Calling ===")

	// Define a weather function
	getWeatherFunc := &genai.FunctionDeclaration{
		Name:        "get_weather",
		Description: "Get the current weather for a location",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"location": {
					Type:        genai.TypeString,
					Description: "The city and state, e.g. San Francisco, CA",
				},
				"unit": {
					Type: genai.TypeString,
					Enum: []string{"celsius", "fahrenheit"},
				},
			},
			Required: []string{"location"},
		},
	}

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("What's the weather like in Tokyo?"),
		&genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{getWeatherFunc},
				},
			},
			ToolConfig: &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode: genai.FunctionCallingConfigModeAuto,
				},
			},
			Temperature: genai.Ptr[float32](0.0), // Lower temperature for deterministic tool calling
		},
	)
	if err != nil {
		return fmt.Errorf("function calling error: %v", err)
	}

	// Check if model wants to call a function
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.FunctionCall != nil {
			fmt.Printf("  Function Call: %s\n", part.FunctionCall.Name)
			fmt.Printf("  Arguments: %v\n", part.FunctionCall.Args)
		} else if part.Text != "" {
			fmt.Printf("  Text: %s\n", part.Text)
		}
	}

	return nil
}

// safetySettings demonstrates content safety controls
func (g *GeminiBot) safetySettings(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "safety-settings")
	defer span.End()

	fmt.Println("\n=== Example 6: Safety Settings ===")

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Tell me about internet safety."),
		&genai.GenerateContentConfig{
			SafetySettings: []*genai.SafetySetting{
				{
					Category:  genai.HarmCategoryHarassment,
					Threshold: genai.HarmBlockThresholdBlockMediumAndAbove,
				},
				{
					Category:  genai.HarmCategoryHateSpeech,
					Threshold: genai.HarmBlockThresholdBlockMediumAndAbove,
				},
				{
					Category:  genai.HarmCategoryDangerousContent,
					Threshold: genai.HarmBlockThresholdBlockMediumAndAbove,
				},
				{
					Category:  genai.HarmCategorySexuallyExplicit,
					Threshold: genai.HarmBlockThresholdBlockMediumAndAbove,
				},
			},
		},
	)
	if err != nil {
		return fmt.Errorf("safety settings error: %v", err)
	}

	fmt.Printf("  %s\n", resp.Text())

	// Show safety ratings
	if len(resp.Candidates) > 0 && len(resp.Candidates[0].SafetyRatings) > 0 {
		fmt.Println("  Safety Ratings:")
		for _, rating := range resp.Candidates[0].SafetyRatings {
			fmt.Printf("    - %s: %s\n", rating.Category, rating.Probability)
		}
	}

	return nil
}

// jsonMode demonstrates structured output with JSON schema
func (g *GeminiBot) jsonMode(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "json-mode")
	defer span.End()

	fmt.Println("\n=== Example 7: JSON Mode (Structured Output) ===")

	// Define a JSON schema for the response
	schema := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"name": {
				Type:        genai.TypeString,
				Description: "Name of the person",
			},
			"age": {
				Type:        genai.TypeInteger,
				Description: "Age in years",
			},
			"occupation": {
				Type:        genai.TypeString,
				Description: "Current occupation",
			},
		},
		Required: []string{"name", "age", "occupation"},
	}

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Create a profile for Albert Einstein"),
		&genai.GenerateContentConfig{
			ResponseMIMEType: "application/json",
			ResponseSchema:   schema,
		},
	)
	if err != nil {
		return fmt.Errorf("json mode error: %v", err)
	}

	fmt.Printf("  JSON Response: %s\n", resp.Text())
	return nil
}

// multimodal demonstrates working with images (conceptual example)
func (g *GeminiBot) multimodal(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "multimodal")
	defer span.End()

	fmt.Println("\n=== Example 8: Multimodal (Images) ===")
	fmt.Println("  (Conceptual - would require actual image bytes)")

	// In production, you'd load an actual image file like this:
	// imageBytes, _ := os.ReadFile("image.jpg")
	// content := &genai.Content{
	// 	Parts: []*genai.Part{
	// 		{Text: "What's in this image?"},
	// 		{InlineData: &genai.Blob{
	// 			MIMEType: "image/jpeg",
	// 			Data:     imageBytes,
	// 		}},
	// 	},
	// }

	resp, err := g.client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-exp",
		genai.Text("Describe a beautiful sunset over mountains."),
		nil,
	)
	if err != nil {
		return fmt.Errorf("multimodal error: %v", err)
	}

	fmt.Printf("  %s\n", resp.Text())
	return nil
}

func main() {
	fmt.Println("Braintrust Google Gemini Tracing Examples")
	fmt.Println("==========================================")

	// Initialize braintrust tracing with a specific project
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-internal-examples"),
		braintrust.WithBlockingLogin(true), // Ensure org name is available for permalinks
	)
	if err != nil {
		log.Fatal(err)
	}

	// Create a Gemini client with tracing
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: tracegenai.Client(tracegenai.WithTracerProvider(tp)),
		APIKey:     os.Getenv("GOOGLE_API_KEY"),
		Backend:    genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Set the experiment as parent for tracing
	ctx, rootSpan := tracer.Start(ctx, "genai-examples")
	defer rootSpan.End()

	// ======================
	// GEMINI API EXAMPLES
	// ======================
	fmt.Println("\nGoogle Gemini API Examples")
	fmt.Println("===========================")
	fmt.Println("Demonstrating: text generation, system instructions, multi-turn,")
	fmt.Println("streaming, function calling, safety settings, JSON mode, and multimodal")

	bot := newGeminiBot(client)

	if err := bot.basicText(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.withSystemInstruction(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.multiTurnConversation(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.streaming(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.functionCalling(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.safetySettings(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.jsonMode(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	if err := bot.multimodal(ctx); err != nil {
		log.Printf("Error: %v", err)
	}

	fmt.Println("\n=== Tracing Complete ===")
	fmt.Println("All examples completed successfully!")

	// Print the root span link
	link := bt.Permalink(rootSpan)
	if link != "" {
		fmt.Printf("View trace: %s\n", link)
	} else {
		fmt.Println("Check your Braintrust dashboard to view the traces.")
	}
}
