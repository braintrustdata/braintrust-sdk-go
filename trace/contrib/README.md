# Manual Instrumentation

This guide shows how to manually add tracing middleware to your LLM clients. For zero-code instrumentation, see [Automatic Instrumentation](../../README.md#automatic-instrumentation) in the main README.

## Prerequisites

Set up OpenTelemetry and initialize Braintrust:

```go
import (
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
    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

client := openai.NewClient(
    option.WithMiddleware(traceopenai.NewMiddleware()),
)

_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
    Messages: []openai.ChatCompletionMessageParamUnion{
        openai.UserMessage("Hello!"),
    },
    Model: openai.ChatModelGPT4oMini,
})
```

## Anthropic

```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
    traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

client := anthropic.NewClient(
    option.WithMiddleware(traceanthropic.NewMiddleware()),
)

_, err := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model: anthropic.ModelClaude3_7SonnetLatest,
    Messages: []anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
    },
    MaxTokens: 1024,
})
```

## Google Gemini

```go
import (
    "google.golang.org/genai"
    tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

client, err := genai.NewClient(ctx, &genai.ClientConfig{
    HTTPClient: tracegenai.Client(),
    APIKey:     os.Getenv("GOOGLE_API_KEY"),
    Backend:    genai.BackendGeminiAPI,
})

_, err = client.Models.GenerateContent(ctx,
    "gemini-1.5-flash",
    genai.Text("Hello!"),
    nil,
)
```

## sashabaranov/go-openai

```go
import (
    "github.com/sashabaranov/go-openai"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai"
)

config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
config.HTTPClient = traceopenai.Client()
client := openai.NewClientWithConfig(config)

_, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
    Model: openai.GPT4oMini,
    Messages: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Hello!"},
    },
})
```

## LangChainGo

See [`examples/langchaingo`](../../examples/langchaingo/main.go) for LangChainGo integration with callback-based tracing.
