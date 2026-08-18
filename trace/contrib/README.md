# Manual Instrumentation

This guide shows how to manually add tracing middleware to your LLM clients. For zero-code instrumentation, see [Automatic Instrumentation](../../README.md#automatic-instrumentation) in the main README.

## Prerequisites

Set up OpenTelemetry and initialize Braintrust:

```go
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

    _, err := braintrust.New(tp, braintrust.WithProject("my-project"))
    if err != nil {
        log.Fatal(err)
    }
    // Now add tracing middleware to your LLM clients below
}
```

## OpenAI (openai-go)

```go
import (
    "context"

    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func main() {
    ctx := context.Background()
    client := openai.NewClient(
        option.WithMiddleware(traceopenai.NewMiddleware()),
    )

    _, _ = client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
        Messages: []openai.ChatCompletionMessageParamUnion{
            openai.UserMessage("Hello!"),
        },
        Model: openai.ChatModelGPT4oMini,
    })
}
```

## Anthropic

```go
import (
    "context"

    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
    traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

func main() {
    ctx := context.Background()
    client := anthropic.NewClient(
        option.WithMiddleware(traceanthropic.NewMiddleware()),
    )

    _, _ = client.Messages.New(ctx, anthropic.MessageNewParams{
        Model: anthropic.ModelClaudeHaiku4_5,
        Messages: []anthropic.MessageParam{
            anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
        },
        MaxTokens: 1024,
    })
}
```

## Google Gemini

```go
import (
    "context"
    "os"

    "google.golang.org/genai"
    tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func main() {
    ctx := context.Background()
    client, _ := genai.NewClient(ctx, &genai.ClientConfig{
        HTTPClient: tracegenai.Client(),
        APIKey:     os.Getenv("GOOGLE_API_KEY"),
        Backend:    genai.BackendGeminiAPI,
    })

    _, _ = client.Models.GenerateContent(ctx,
        "gemini-1.5-flash",
        genai.Text("Hello!"),
        nil,
)
}
```

## Genkit

```go
import (
    "context"
    "os"

    tracegenkit "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"
    "github.com/firebase/genkit/go/ai"
    "github.com/firebase/genkit/go/genkit"
    "github.com/firebase/genkit/go/plugins/googlegenai"
)

func main() {
    ctx := context.Background()
    g := genkit.Init(ctx,
        genkit.WithPlugins(&googlegenai.GoogleAI{
            APIKey: os.Getenv("GOOGLE_API_KEY"),
        }),
        genkit.WithDefaultModel("googleai/gemini-2.5-flash"),
    )

    _, _ = genkit.Generate(ctx, g,
        ai.WithPrompt("Hello!"),
        ai.WithMiddleware(tracegenkit.NewMiddleware(
            tracegenkit.WithProvider("google"),
            tracegenkit.WithModel("gemini-2.5-flash"),
        )),
    )
}
```

Use `trace/contrib/genkit` as the top-level tracing layer for Genkit requests. Avoid combining it with lower-level provider integrations such as `trace/contrib/openai`, `trace/contrib/anthropic`, or `trace/contrib/genai` on the same request path, or you may emit nested LLM spans. Tool-enabled generation emits an agent `task` with child `llm` and `tool` spans. Genkit does not always expose the selected model to model middleware, so pass `WithProvider` and `WithModel` when they cannot be inferred from the provider response.

For manual instrumentation, define tools with `tracegenkit.DefineTool`, `tracegenkit.DefineToolWithInputSchema`, or `tracegenkit.DefineMultipartTool` so each handler is traced for its full execution, including failures.

With orchestrion, middleware is injected automatically into all `genkit.Generate`, `genkit.GenerateText`, and `genkit.GenerateStream` calls, and Genkit tool definitions are replaced with their traced equivalents.

## sashabaranov/go-openai

```go
import (
    "context"
    "os"

    "github.com/sashabaranov/go-openai"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai"
)

func main() {
    ctx := context.Background()
    config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
    config.HTTPClient = traceopenai.Client()
    client := openai.NewClientWithConfig(config)

    _, _ = client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
        Model: openai.GPT4oMini,
        Messages: []openai.ChatCompletionMessage{
            {Role: openai.ChatMessageRoleUser, Content: "Hello!"},
        },
    })
}
```

## LangChainGo

```go
import (
    "context"

    "github.com/tmc/langchaingo/llms"
    "github.com/tmc/langchaingo/llms/openai"
    tracelangchaingo "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo"
)

func main() {
    ctx := context.Background()
    handler := tracelangchaingo.NewHandler()
    llm, _ := openai.New(openai.WithCallback(handler))

    _, _ = llm.GenerateContent(ctx, []llms.MessageContent{
        llms.TextParts(llms.ChatMessageTypeHuman, "Hello!"),
    })
}
```

For richer traces, use `NewHandlerWithOptions` with `TracerProvider`, `Model`, and `Provider` options.
See [`examples/langchaingo`](../../examples/langchaingo/main.go) for complete examples.
