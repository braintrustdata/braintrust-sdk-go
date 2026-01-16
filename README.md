# Braintrust Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/braintrustdata/braintrust-sdk-go.svg)](https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go)
![Beta](https://img.shields.io/badge/status-beta-yellow)

## Overview

This library provides tools for **evaluating** and **tracing** AI applications in [Braintrust](https://www.braintrust.dev). Use it to:

- **Evaluate** your AI models with custom test cases and scoring functions
- **Trace** LLM calls and monitor AI application performance with OpenTelemetry
- **Integrate** seamlessly with OpenAI, Anthropic, Google Gemini, LangChainGo, and other LLM providers

This SDK is currently in BETA status and APIs may change.

## Installation

```bash
go get github.com/braintrustdata/braintrust-sdk-go

export BRAINTRUST_API_KEY="your-api-key"  # Get from https://www.braintrust.dev/app/settings
```

## Instrumentation

Trace LLM calls with **automatic** or **manual** instrumentation.

### Automatic Instrumentation

Use [Orchestrion](https://github.com/DataDog/orchestrion) to automatically inject tracing at compile time—no code changes required.

**1. Install orchestrion:**
```bash
go install github.com/DataDog/orchestrion@latest
```

**2. Create `orchestrion.tool.go` in your project root:**
```go
//go:build tools

package main

import (
    _ "github.com/DataDog/orchestrion"
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all" // All LLM providers
)
```

Or import only the integrations you need:
```go
import (
    _ "github.com/DataDog/orchestrion"
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"    // OpenAI (openai-go)
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic" // Anthropic
    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"     // Google GenAI
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

That's it! Your LLM client calls are now automatically traced. No middleware or wrapper code needed in your application.

### Manual Instrumentation

If you prefer explicit control, you can add tracing middleware manually to your LLM clients. See the [Manual Instrumentation Guide](./trace/contrib/README.md) for detailed examples with OpenAI, Anthropic, Google Gemini, and other providers.

## Evaluations

Run systematic evaluations with custom test cases and scoring functions:

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

    // Create an evaluator with your task's input and output types
    evaluator := braintrust.NewEvaluator[string, string](client)

    // Run an evaluation
    _, err = evaluator.Run(ctx, eval.Opts[string, string]{
        Experiment: "greeting-experiment",
        Dataset: eval.NewDataset([]eval.Case[string, string]{
            {Input: "World", Expected: "Hello World"},
            {Input: "Alice", Expected: "Hello Alice"},
        }),
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
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

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

- **[evals](./examples/evals/evals.go)** - Evaluations with custom scorers
- **[openai](./examples/openai/main.go)** - OpenAI tracing
- **[anthropic](./examples/anthropic/main.go)** - Anthropic tracing
- **[genai](./examples/genai/main.go)** - Google Gemini tracing
- **[langchaingo](./examples/langchaingo/main.go)** - LangChainGo integration
- **[datasets](./examples/datasets/main.go)** - Using Braintrust datasets

## Features

- **Evaluations** - Systematic testing with custom scoring functions
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
