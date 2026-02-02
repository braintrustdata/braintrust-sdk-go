package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model/gemini"
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

func setupAgent(ctx context.Context, bt *braintrust.Client) agent.Agent {
	// Create Gemini model with Braintrust auto-instrumentation
	model, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{
		// HTTPClient: tracegenai.Client(), // Add tracing via custom HTTP client
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

	// Create LLMAgent with MCP tool set, time tool, and tracing callbacks
	btCallbacks := traceadk.NewCallbacks()
	a, err := llmagent.New(btCallbacks.LLMAgentConfig(llmagent.Config{
		Name:        "helper_agent",
		Model:       model,
		Description: "Helper agent.",
		Instruction: "You are a helpful assistant that helps users with various tasks. You can tell the current time in any timezone using the get_time tool.",
		Tools:       []tool.Tool{timeTool},
	}))
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	return a
}

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("adk-go-testing"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Set up top-level tracer
	tracer := otel.Tracer("main")
	ctx, span := tracer.Start(context.Background(), "adk-example")

	fmt.Println("Use Ctrl+C to cleanly exit")
	go func() {
		interruptCh := make(chan os.Signal, 1)
		signal.Notify(interruptCh, os.Interrupt)
		<-interruptCh
		span.End()
		tp.Shutdown(context.Background())
		fmt.Println("Received interrupt, exiting.")
		fmt.Println()
		log.Printf("Tracing link: %s", bt.Permalink(span))
		os.Exit(0)
	}()

	a := setupAgent(ctx, bt)

	config := &launcher.Config{
		AgentLoader: agent.NewSingleLoader(a),
	}
	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Printf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}

	log.Printf("\nTracing link: %s", bt.Permalink(span))
}
