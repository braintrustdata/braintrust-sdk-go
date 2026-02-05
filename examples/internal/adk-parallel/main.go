// Package main demonstrates using Braintrust tracing with ADK parallel workflow agents.
// This example creates a coordinator agent that asks two sub-agents (joke writer and philosopher)
// to respond in parallel to a given topic.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/workflowagents/parallelagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
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

	tracer := otel.Tracer("adk-parallel-example")
	ctx, span := tracer.Start(context.Background(), "examples/internal/adk-parallel/main.go")
	defer span.End()

	flashModel, err := gemini.NewModel(ctx, "gemini-2.5-flash", &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Fatalf("Failed to create 2.5 flash model: %v", err)
	}

	// Create joke writer agent
	jokeCfg := llmagent.Config{
		Name:        "joke_writer",
		Model:       flashModel,
		Description: "An agent that writes jokes about given topics.",
		Instruction: "You are a comedian. When given a topic, write a short, funny joke about it. Keep it clean and clever.",
	}
	traceadk.AddLLMAgentCallbacks(&jokeCfg)
	jokeAgent, err := llmagent.New(jokeCfg)
	if err != nil {
		log.Fatalf("Failed to create joke agent: %v", err)
	}

	// Create philosopher agent
	philosopherCfg := llmagent.Config{
		Name:        "philosopher",
		Model:       flashModel,
		Description: "An agent that writes philosophical quotes about given topics.",
		Instruction: "You are a philosopher. When given a topic, write a thoughtful, profound philosophical quote or reflection about it. Be deep and contemplative.",
	}
	traceadk.AddLLMAgentCallbacks(&philosopherCfg)
	philosopherAgent, err := llmagent.New(philosopherCfg)
	if err != nil {
		log.Fatalf("Failed to create philosopher agent: %v", err)
	}

	// Create parallel agent that runs both sub-agents concurrently
	parallelCfg := parallelagent.Config{
		AgentConfig: agent.Config{
			Name:        "dual_perspective_agent",
			Description: "An agent that provides both humorous and philosophical perspectives on topics by running two sub-agents in parallel.",
			SubAgents:   []agent.Agent{jokeAgent, philosopherAgent},
		},
	}
	traceadk.AddAgentCallbacks(&parallelCfg.AgentConfig)
	parallelAgent, err := parallelagent.New(parallelCfg)
	if err != nil {
		log.Fatalf("Failed to create parallel agent: %v", err)
	}

	// Create top-level coordinator agent that uses the parallel agent
	coordinatorCfg := llmagent.Config{
		Name:        "coordinator",
		Model:       flashModel,
		Description: "A coordinator agent that asks sub-agents to provide perspectives on topics.",
		Instruction: "You are a coordinator. When the user gives you a topic, ask your sub-agents to provide responses on the topic.",
		SubAgents:   []agent.Agent{parallelAgent},
	}
	traceadk.AddLLMAgentCallbacks(&coordinatorCfg)
	coordinatorAgent, err := llmagent.New(coordinatorCfg)
	if err != nil {
		log.Fatalf("Failed to create coordinator agent: %v", err)
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
		Agent:          coordinatorAgent,
		SessionService: sessionSvc,
	})
	if err != nil {
		log.Fatalf("Failed to create runner: %v", err)
	}
	msg := genai.NewContentFromText("what's the secret to life", genai.RoleUser)
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
