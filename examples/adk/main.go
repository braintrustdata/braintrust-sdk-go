package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceadk "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk"
)

// getTimeArgs defines the input arguments for the time tool
type getTimeArgs struct {
	Timezone string `json:"timezone" jsonschema:"IANA timezone identifier (e.g., 'America/New_York', 'Europe/London', 'Asia/Tokyo')."`
}

// getTimeResult defines the output of the time tool
type getTimeResult struct {
	Time     string `json:"time"`
	Timezone string `json:"timezone"`
}

// getTime is the handler function for the time tool
func getTime(ctx tool.Context, args getTimeArgs) (getTimeResult, error) {
	// Load the timezone
	loc, err := time.LoadLocation(args.Timezone)
	if err != nil {
		return getTimeResult{}, fmt.Errorf("invalid timezone '%s': %w", args.Timezone, err)
	}

	// Get current time in the timezone
	currentTime := time.Now().In(loc)

	return getTimeResult{
		Time:     currentTime.Format("2006-01-02 15:04:05 MST"),
		Timezone: args.Timezone,
	}, nil
}

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	// Initialize Braintrust tracing
	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Set up top-level span
	// Get a tracer instance from the global TracerProvider
	tracer := otel.Tracer("adk-example")

	// Create a parent span to wrap the ADK runner
	ctx, span := tracer.Start(context.Background(), "examples/adk/main.go")
	defer span.End()

	// Set up ADK
	model, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Fatalf("Failed to create model: %v", err)
	}

	// Create time tool using functiontool
	timeTool, err := functiontool.New(
		functiontool.Config{
			Name:        "get_time",
			Description: "Get the current time for a specific timezone. Provide a timezone in IANA format like 'America/New_York', 'Europe/London', or 'Asia/Tokyo'.",
		},
		getTime,
	)
	if err != nil {
		log.Fatalf("Failed to create time tool: %v", err)
	}

	cfg := llmagent.Config{
		Name:        "helper_agent",
		Model:       model,
		Description: "Helper agent.",
		Instruction: "You are a helpful assistant that helps users with various tasks. You can tell the current time in any timezone using the get_time tool.",
		Tools:       []tool.Tool{timeTool},
	}
	traceadk.AddLLMAgentCallbacks(&cfg)
	a, err := llmagent.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	sessionSvc := session.InMemoryService()
	_, err = sessionSvc.Create(ctx, &session.CreateRequest{
		AppName:   "adk-example",
		UserID:    "user",
		SessionID: "session",
	})
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	runner, err := runner.New(runner.Config{
		AppName:        "adk-example",
		Agent:          a,
		SessionService: sessionSvc,
	})
	msg := genai.NewContentFromText("What is the time in San Francisco?", genai.RoleUser)
	for ev, err := range runner.Run(ctx, "user", "session", msg, agent.RunConfig{}) {
		if err != nil {
			log.Printf("   ADK error: %v", err)
			return
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					fmt.Printf("   Response: %s\n", p.Text)
				}
			}
		}
	}

	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
