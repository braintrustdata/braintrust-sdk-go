
# Braintrust Go Tracing & Eval SDK

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
```

## Quick Start

### Set up your API key

```bash
export BRAINTRUST_API_KEY="your-api-key"
```

### Evals

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
    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(context.Background())
    otel.SetTracerProvider(tp)

    // Initialize Braintrust
    bt, err := braintrust.New(tp,
        braintrust.WithProject("my-project"),
        braintrust.WithBlockingLogin(true),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create evaluator
    evaluator := braintrust.NewEvaluator[string, string](bt)

    // Run an evaluation
    _, err = evaluator.Run(context.Background(), eval.Opts[string, string]{
        Experiment: "greeting-experiment",
        Dataset: eval.NewDataset([]eval.Case[string, string]{
            {Input: "World", Expected: "Hello World"},
            {Input: "Alice", Expected: "Hello Alice"},
        }),
        Task: eval.T(func(ctx context.Context, input string) (string, error) {
            return "Hello " + input, nil
        }),
        Scorers: []eval.Scorer[string, string]{
            eval.NewScorer("exact_match", func(ctx context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
                if taskResult.Expected == taskResult.Output {
                    return eval.S(1.0), nil
                }
                return eval.S(0.0), nil
            }),
        },
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

### OpenAI Tracing

```go
package main

import (
    "context"
    "log"

    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"

    "github.com/braintrustdata/braintrust-sdk-go"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {
    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(context.Background())
    otel.SetTracerProvider(tp)

    // Initialize Braintrust
    _, err := braintrust.New(tp,
        braintrust.WithProject("my-project"),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create OpenAI client with tracing middleware
    client := openai.NewClient(
        option.WithMiddleware(traceopenai.NewMiddleware()),
    )

    // Your OpenAI API calls will now be automatically traced
    _ = client // Use the client for your API calls
}
```

### Anthropic Tracing

```go
package main

import (
    "context"
    "log"

    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"

    "github.com/braintrustdata/braintrust-sdk-go"
    traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

func main() {
    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(context.Background())
    otel.SetTracerProvider(tp)

    // Initialize Braintrust
    _, err := braintrust.New(tp,
        braintrust.WithProject("my-project"),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create Anthropic client with tracing middleware
    client := anthropic.NewClient(
        option.WithMiddleware(traceanthropic.NewMiddleware()),
    )

    // Your Anthropic API calls will now be automatically traced
    _ = client // Use the client for your API calls
}
```

### Google Gemini Tracing

```go
package main

import (
    "context"
    "log"
    "os"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"
    "google.golang.org/genai"

    "github.com/braintrustdata/braintrust-sdk-go"
    tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func main() {
    // Set up OpenTelemetry tracer
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(context.Background())
    otel.SetTracerProvider(tp)

    // Initialize Braintrust
    _, err := braintrust.New(tp,
        braintrust.WithProject("my-project"),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create Gemini client with tracing
    client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
        HTTPClient: tracegenai.Client(),
        APIKey:     os.Getenv("GOOGLE_API_KEY"),
        Backend:    genai.BackendGeminiAPI,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Your Gemini API calls will now be automatically traced
    _ = client // Use the client for your API calls
}
```

### LangChainGo Integration

The SDK provides comprehensive tracing support for [LangChainGo](https://github.com/tmc/langchaingo) applications. Automatically trace LLM calls, chains, tools, agents, and retrievers by passing the Braintrust callback handler to your LangChainGo components. See [`examples/langchaingo`](./examples/langchaingo/main.go) for a simple getting started example, or [`examples/internal/langchaingo`](./examples/internal/langchaingo/comprehensive.go) for a comprehensive demonstration of all features.

## Features

- **Evaluations**: Run systematic evaluations of your AI systems with custom scoring functions
- **Tracing**: Automatic instrumentation for OpenAI, Anthropic, Google Gemini, and LangChainGo
- **Datasets**: Manage and version your evaluation datasets
- **Experiments**: Track different versions and configurations of your AI systems
- **Observability**: Monitor your AI applications in production

## Examples

Check out the [`examples/`](./examples/) directory for complete working examples:

- [evals](./examples/evals/evals.go) - Create and run evaluations with custom test cases and scoring functions
- [openai](./examples/openai/main.go) - Automatically trace OpenAI API calls
- [anthropic](./examples/anthropic/main.go) - Automatically trace Anthropic API calls
- [genai](./examples/genai/main.go) - Automatically trace Google Gemini API calls
- [langchaingo](./examples/langchaingo/main.go) - Trace LangChainGo applications (chains, tools, agents, retrievers)
- [datasets](./examples/datasets/main.go) - Run evaluations using datasets stored in Braintrust

## Documentation

- [Braintrust Documentation](https://www.braintrust.dev/docs)
- [API Reference](https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go)

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup and contribution guidelines.

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](./LICENSE) file for details.
