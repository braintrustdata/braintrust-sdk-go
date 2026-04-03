[![Braintrust](./braintrust-logo.svg)](https://www.braintrust.dev/)

# Braintrust Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/braintrustdata/braintrust-sdk-go.svg)](https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go)
![Beta](https://img.shields.io/badge/status-beta-yellow)

## Overview

This library provides tools for **evaluating** and **tracing** AI applications in [Braintrust](https://www.braintrust.dev). Use it to:

- **Evaluate** your AI models with custom test cases and scoring functions
- **Trace** LLM calls and monitor AI application performance with OpenTelemetry
- **Integrate** seamlessly with OpenAI, Anthropic, Google Gemini, Genkit, ADK, CloudWeGo Eino, LangChainGo, and other LLM providers

This SDK is currently in BETA status and APIs may change.

## Installation

```bash
go get github.com/braintrustdata/braintrust-sdk-go

export BRAINTRUST_API_KEY="your-api-key"  # Get from https://www.braintrust.dev/app/settings
```

Each tracing integration is published as its own Go module. Install only the ones you need:

```bash
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai       # OpenAI (openai-go)
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic     # Anthropic
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai         # Google GenAI
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit        # Firebase Genkit
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk           # Google ADK
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino # CloudWeGo Eino
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo   # LangChainGo
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai # sashabaranov/go-openai
```

Or install all integrations at once with the meta-module:

```bash
go get github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all
```

## Instrumentation

Trace LLM calls with **automatic** or **manual** instrumentation.

### Automatic Instrumentation

Use [Orchestrion](https://github.com/DataDog/orchestrion) to automatically inject tracing at compile time—no code changes required.

**1. Install orchestrion:**
```bash
go install github.com/DataDog/orchestrion@v1.6.1
```

**2. Create `orchestrion.tool.go` in your project root:**
```go
//go:build tools

package main

import (
    _ "github.com/DataDog/orchestrion"
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all" // Dedicated meta-module for all Braintrust LLM integrations
)
```

Or import only the integrations you need:
```go
import (
    _ "github.com/DataDog/orchestrion"
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"    // OpenAI (openai-go)
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic" // Anthropic
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"     // Google GenAI
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"    // Firebase Genkit
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk"       // Google ADK
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino" // CloudWeGo Eino
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo" // LangChainGo
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai" // sashabaranov/go-openai
)
```

**3. Build with orchestrion:**
```bash
# Build with orchestrion
orchestrion go build ./...

# Or configure GOFLAGS to use orchestrion automatically
export GOFLAGS="-toolexec='orchestrion toolexec'"
go build ./...
```

**4. Initialize OpenTelemetry and Braintrust in your application:**
```go
import (
    "context"
    "log"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"
    "github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
    ctx := context.Background()

    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(ctx)
    otel.SetTracerProvider(tp)

    // Initialize Braintrust (registers the exporter)
    _, err := braintrust.New(tp, braintrust.WithProject("my-project"))
    if err != nil {
        log.Fatal(err)
    }

    // Your LLM calls are now automatically traced
}
```

That's it! Your LLM client calls are now automatically traced. No middleware or wrapper code needed in your application.

### Manual Instrumentation

If you prefer explicit control, you can add tracing middleware manually to your LLM clients. See the [Manual Instrumentation Guide](./trace/contrib/README.md) for detailed examples with OpenAI, Anthropic, Google Gemini, and other providers.

## Evaluations

Run [evals](https://www.braintrust.dev/docs/guides/evals) with custom test cases and scoring functions.

### Define and run

Define an eval once with its task and scorers, then run it against any dataset:

```go
package main

import (
    "context"
    "log"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"

    "github.com/braintrustdata/braintrust-sdk-go"
    "github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
    ctx := context.Background()

    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(ctx)
    otel.SetTracerProvider(tp)

    // Initialize Braintrust
    client, err := braintrust.New(tp)
    if err != nil {
        log.Fatal(err)
    }

    // Define a reusable eval (task + scorers)
    greetingEval := &eval.Eval[string, string]{
        Name: "greeting-experiment",
        Task: eval.T(func(ctx context.Context, input string) (string, error) {
            return "Hello " + input, nil
        }),
        Scorers: []eval.Scorer[string, string]{
            eval.NewScorer("exact_match", func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
                score := 0.0
                if r.Expected == r.Output {
                    score = 1.0
                }
                return eval.S(score), nil
            }),
        },
    }

    // Run against a dataset
    evaluator := braintrust.NewEvaluator[string, string](client)
    _, err = evaluator.RunEval(ctx, greetingEval, eval.RunOpts[string, string]{
        Dataset: eval.NewDataset([]eval.Case[string, string]{
            {Input: "World", Expected: "Hello World"},
            {Input: "Alice", Expected: "Hello Alice"},
        }),
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

### Remote Eval Server

The same eval definition can be registered with a [remote eval server](https://www.braintrust.dev/docs/evaluate/remote-evals), letting you run evals from the Braintrust playground against code on your own infrastructure:

```go
package main

import (
    "context"
    "log"
    "strings"

    "github.com/braintrustdata/braintrust-sdk-go/eval"
    "github.com/braintrustdata/braintrust-sdk-go/server"
)

func main() {
    // Define the eval once
    classify := &eval.Eval[string, string]{
        Name: "classify",
        Task: eval.T(func(ctx context.Context, input string) (string, error) {
            return strings.ToUpper(input), nil
        }),
        Scorers: []eval.Scorer[string, string]{
            eval.NewScorer("exact_match", func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
                if r.Output == r.Expected { return eval.S(1.0), nil }
                return eval.S(0.0), nil
            }),
        },
    }

    // Register with server for remote execution
    srv := server.New(
        server.WithAddress("localhost:8300"),
        server.WithNoAuth(), // Remove for production
    )
    server.RegisterEval(srv, classify, server.RegisterEvalOpts{})

    log.Fatal(srv.Start())
}
```

Then configure `http://localhost:8300` in your Braintrust project settings under **Remote evals**.

## API Client

Manage Braintrust resources programmatically:

```go
package main

import (
    "context"
    "log"

    "go.opentelemetry.io/otel/sdk/trace"

    "github.com/braintrustdata/braintrust-sdk-go"
    functionsapi "github.com/braintrustdata/braintrust-sdk-go/api/functions"
)

func main() {
    ctx := context.Background()

    // Create tracer provider
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(ctx)

    // Initialize Braintrust
    client, err := braintrust.New(tp,
        braintrust.WithProject("my-project"),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Get API client
    api := client.API()

    // Create a prompt
    prompt, err := api.Functions().Create(ctx, functionsapi.CreateParams{
        ProjectID: "your-project-id",
        Name:      "My Prompt",
        Slug:      "my-prompt",
        FunctionData: map[string]any{
            "type": "prompt",
        },
        PromptData: map[string]any{
            "prompt": map[string]any{
                "type": "chat",
                "messages": []map[string]any{
                    {
                        "role":    "system",
                        "content": "You are a helpful assistant.",
                    },
                    {
                        "role":    "user",
                        "content": "{{input}}",
                    },
                },
            },
            "options": map[string]any{
                "model": "gpt-4o-mini",
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    _ = prompt // Prompt is ready to use
}
```

## Examples

Complete working examples are available in [`examples/`](./examples/):

**Getting Started:**
- **[openai](./examples/openai/main.go)** - OpenAI tracing
- **[anthropic](./examples/anthropic/main.go)** - Anthropic tracing
- **[genai](./examples/genai/main.go)** - Google Gemini tracing
- **[genkit](./examples/genkit/main.go)** - Firebase Genkit middleware tracing

**Evaluations:**
- **[evals](./examples/evals/evals.go)** - Evaluations with custom scorers
- **[datasets](./examples/datasets/main.go)** - Run evals against downloaded datasets
- **[dataset-api](./examples/dataset-api/main.go)** - Create datasets, use prompts, run evals
- **[scorers](./examples/scorers/scorers.go)** - Custom scoring with online and code-based scorers

**Alternative Providers & Libraries:**
- **[sashabaranov-openai](./examples/sashabaranov-openai/main.go)** - sashabaranov/go-openai tracing
- **[openrouter](./examples/openrouter/main.go)** - OpenRouter tracing
- **[langchaingo](./examples/langchaingo/main.go)** - LangChainGo integration
- **[adk](./examples/adk/main.go)** - Google ADK agent tracing
- **[cloudwego/eino](./examples/cloudwego/eino/main.go)** - CloudWeGo Eino integration

**Advanced:**
- **[manual-llm-logging](./examples/manual-llm-logging/main.go)** - Manually log LLM calls
- **[attachments](./examples/attachments/main.go)** - Include images and files in traces
- **[prompts](./examples/prompts/main.go)** - Use Braintrust hosted prompts
- **[distributed-tracing](./examples/distributed-tracing/main.go)** - W3C baggage propagation across services
- **[otel](./examples/otel/main.go)** - Add Braintrust to existing OpenTelemetry setup
- **[eval-server](./examples/internal/eval-server/main.go)** - Remote eval server

## Features

- **Evaluations** - Systematic testing with custom scoring functions
- **Remote Eval Server** - Run evals from the Braintrust UI against your own code
- **Tracing** - Automatic instrumentation for major LLM providers
- **Datasets** - Manage and version evaluation datasets
- **Experiments** - Track versions and configurations
- **Observability** - Monitor AI applications in production

## Documentation

- [Braintrust Documentation](https://www.braintrust.dev/docs)
- [API Reference](https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go)

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup and contribution guidelines.

## License

Apache License 2.0. See [LICENSE](./LICENSE) for details.
