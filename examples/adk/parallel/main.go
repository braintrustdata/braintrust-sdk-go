// Package main demonstrates using Braintrust tracing with ADK parallel workflow agents.
// This example creates a coordinator agent that asks two sub-agents (joke writer and philosopher)
// to respond in parallel to a given topic.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/workflowagents/parallelagent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceadk "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk"
)

func main() {
	// Set up OpenTelemetry tracing
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	// Initialize Braintrust client
	bt, err := braintrust.New(tp,
		braintrust.WithProject("adk-parallel-example"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Create shared callbacks for tracing
	callbacks := traceadk.NewCallbacks()

	ctx := context.Background()

	flashModel, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Fatalf("Failed to create 2.5 flash model: %v", err)
	}

	// Create joke writer agent
	jokeAgent, err := llmagent.New(callbacks.LLMAgentConfig(llmagent.Config{
		Name:        "joke_writer",
		Model:       flashModel,
		Description: "An agent that writes jokes about given topics.",
		Instruction: "You are a comedian. When given a topic, write a short, funny joke about it. Keep it clean and clever.",
	}))
	if err != nil {
		log.Fatalf("Failed to create joke agent: %v", err)
	}

	// Create philosopher agent
	philosopherAgent, err := llmagent.New(callbacks.LLMAgentConfig(llmagent.Config{
		Name:        "philosopher",
		Model:       flashModel,
		Description: "An agent that writes philosophical quotes about given topics.",
		Instruction: "You are a philosopher. When given a topic, write a thoughtful, profound philosophical quote or reflection about it. Be deep and contemplative.",
	}))
	if err != nil {
		log.Fatalf("Failed to create philosopher agent: %v", err)
	}

	// Create parallel agent that runs both sub-agents concurrently
	parallelAgent, err := parallelagent.New(parallelagent.Config{
		AgentConfig: callbacks.AgentConfig(agent.Config{
			Name:        "dual_perspective_agent",
			Description: "An agent that provides both humorous and philosophical perspectives on topics by running two sub-agents in parallel.",
			SubAgents:   []agent.Agent{jokeAgent, philosopherAgent},
		}),
	})
	if err != nil {
		log.Fatalf("Failed to create parallel agent: %v", err)
	}

	// Create top-level coordinator agent that uses the parallel agent
	coordinatorAgent, err := llmagent.New(callbacks.LLMAgentConfig(llmagent.Config{
		Name:        "coordinator",
		Model:       flashModel,
		Description: "A coordinator agent that asks sub-agents to provide perspectives on topics.",
		Instruction: "You are a coordinator. When the user gives you a topic, ask your sub-agents to provide responses on the topic.",
		SubAgents:   []agent.Agent{parallelAgent},
	}))
	if err != nil {
		log.Fatalf("Failed to create coordinator agent: %v", err)
	}

	tracer := otel.Tracer("main")
	ctx, span := tracer.Start(context.Background(), "adk-example-parallel")
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

	config := &launcher.Config{
		AgentLoader: agent.NewSingleLoader(coordinatorAgent),
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Fatalf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}

	log.Printf("\nTracing link: %s", bt.Permalink(nil))
}
