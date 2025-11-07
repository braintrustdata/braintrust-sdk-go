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

## Quick Start

Braintrust uses [OpenTelemetry](https://opentelemetry.io/) for distributed tracing. Set up a TracerProvider and initialize the client:

```go
package main

import (
    "context"
    "log"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"

    "github.com/braintrustdata/braintrust-sdk-go"
)

func main() {
    tp := trace.NewTracerProvider()
    defer tp.Shutdown(context.Background())
    otel.SetTracerProvider(tp)

    client, err := braintrust.New(tp, braintrust.WithProject("my-project"))
    if err != nil {
        log.Fatal(err)
    }
    // Client is ready to use
}
```

## Usage

### Evaluations

Run systematic evaluations with custom test cases and scoring functions:

```go
import (
    "github.com/braintrustdata/braintrust-sdk-go"
    "github.com/braintrustdata/braintrust-sdk-go/eval"
)

evaluator := braintrust.NewEvaluator[string, string](client)

_, err := evaluator.Run(ctx, eval.Opts[string, string]{
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
            if r.Expected == r.Output {
                return eval.S(1.0), nil
            }
            return eval.S(0.0), nil
        }),
    },
})
```

### Tracing LLM Calls

Automatically trace LLM calls by adding middleware to your client:

**OpenAI:**
```go
import (
    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

client := openai.NewClient(
    option.WithMiddleware(traceopenai.NewMiddleware()),
)
```

**Anthropic:**
```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
    traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

client := anthropic.NewClient(
    option.WithMiddleware(traceanthropic.NewMiddleware()),
)
```

### API Client

Manage Braintrust resources programmatically:

```go
api := client.API()

prompt, err := api.Functions().Create(ctx, functionsapi.CreateParams{
    ProjectID: "your-project-id",
    Name:      "My Prompt",
    Slug:      "my-prompt",
    FunctionData: map[string]any{"type": "prompt"},
    PromptData: map[string]any{
        "prompt": map[string]any{
            "type": "chat",
            "messages": []map[string]any{
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "{{input}}"},
            },
        },
        "options": map[string]any{"model": "gpt-4o-mini"},
    },
})
```

**Google Gemini:**
```go
import (
    "google.golang.org/genai"
    tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

client, _ := genai.NewClient(ctx, &genai.ClientConfig{
    HTTPClient: tracegenai.Client(),
    APIKey:     os.Getenv("GOOGLE_API_KEY"),
    Backend:    genai.BackendGeminiAPI,
})
```

**LangChainGo:**
The SDK provides comprehensive tracing for [LangChainGo](https://github.com/tmc/langchaingo) applications. See [`examples/langchaingo`](./examples/langchaingo/main.go) for examples.

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
