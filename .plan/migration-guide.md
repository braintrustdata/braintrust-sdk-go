# Migration Guide: Old API → New API

## Overview

The Braintrust Go SDK has been refactored to eliminate global state and provide a cleaner, more explicit API. Three major changes:

1. **No more global state** - Replace `trace.Quickstart()` / `trace.Enable()` with explicit `braintrust.New()`
2. **Evaluator pattern** - Replace global `eval.Run()` with `evaluator.Run()`
3. **New package paths** - `braintrust/trace/traceopenai` → `trace/contrib/openai`

## 1. Initialize Braintrust Client

### Old API
```go
import "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace"

teardown, err := trace.Quickstart()
if err != nil {
    log.Fatal(err)
}
defer teardown()
```

### New API
```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"
    "github.com/braintrustdata/braintrust-sdk-go"
)

tp := trace.NewTracerProvider()
defer tp.Shutdown(context.Background())
otel.SetTracerProvider(tp)

bt, err := braintrust.New(tp,
    braintrust.WithProject("my-project"),
)
if err != nil {
    log.Fatal(err)
}
```

**Key changes:**
- Manage `TracerProvider` explicitly
- Pass `TracerProvider` to `braintrust.New()`
- Project specified via `WithProject()` option
- Returns `*braintrust.Client` instead of teardown function

## 2. Evaluations

### Old API
```go
import "github.com/braintrustdata/braintrust-sdk-go/braintrust/eval"

_, err = eval.Run(context.Background(), eval.Opts[string, string]{
    Project:    "my-project",
    Experiment: "my-experiment",
    Cases:      cases,
    Task: func(ctx context.Context, input string) (string, error) {
        return process(input), nil
    },
    Scorers: []eval.Scorer[string, string]{
        eval.NewScorer("my_scorer", func(ctx context.Context, input, expected, output string, metadata eval.Metadata) (eval.Scores, error) {
            return eval.S(score), nil
        }),
    },
})
```

### New API
```go
import (
    "github.com/braintrustdata/braintrust-sdk-go"
    "github.com/braintrustdata/braintrust-sdk-go/eval"
)

evaluator := braintrust.NewEvaluator[string, string](bt)

_, err = evaluator.Run(context.Background(), eval.Opts[string, string]{
    Experiment: "my-experiment",
    Cases:      cases,
    Task: eval.T(func(ctx context.Context, input string) (string, error) {
        return process(input), nil
    }),
    Scorers: []eval.Scorer[string, string]{
        eval.NewScorer("my_scorer", func(ctx context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
            // Access: taskResult.Input, taskResult.Expected, taskResult.Output, taskResult.Metadata
            return eval.S(score), nil
        }),
    },
})
```

**Key changes:**
- Create evaluator: `braintrust.NewEvaluator[Input, Output](bt)`
- No `Project` field (set on client)
- Wrap simple tasks with `eval.T()`
- Scorer takes `TaskResult[I, O]` instead of individual parameters

## 3. OpenAI Tracing

### Old API
```go
import (
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace"
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/traceopenai"
)

teardown, err := trace.Quickstart()
defer teardown()

client := openai.NewClient(
    option.WithMiddleware(traceopenai.Middleware),
)
```

### New API
```go
import (
    "go.opentelemetry.io/otel/sdk/trace"
    "github.com/braintrustdata/braintrust-sdk-go"
    traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

tp := trace.NewTracerProvider()
defer tp.Shutdown(context.Background())

bt, err := braintrust.New(tp, braintrust.WithProject("my-project"))

client := openai.NewClient(
    option.WithMiddleware(traceopenai.NewMiddleware()),
)
```

**Key changes:**
- Import path: `braintrust/trace/traceopenai` → `trace/contrib/openai`
- Call `traceopenai.NewMiddleware()` instead of using `traceopenai.Middleware` variable
- Optional: Pass `traceopenai.WithTracerProvider(tp)` for custom tracer

## 4. Anthropic Tracing

### Old API
```go
import (
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace"
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/traceanthropic"
)

teardown, err := trace.Quickstart()
defer teardown()

client := anthropic.NewClient(
    option.WithMiddleware(traceanthropic.Middleware),
)
```

### New API
```go
import (
    "go.opentelemetry.io/otel/sdk/trace"
    "github.com/braintrustdata/braintrust-sdk-go"
    traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

tp := trace.NewTracerProvider()
defer tp.Shutdown(context.Background())

bt, err := braintrust.New(tp, braintrust.WithProject("my-project"))

client := anthropic.NewClient(
    option.WithMiddleware(traceanthropic.NewMiddleware()),
)
```

**Key changes:**
- Import path: `braintrust/trace/traceanthropic` → `trace/contrib/anthropic`
- Call `traceanthropic.NewMiddleware()` instead of using variable

## 5. Google Gemini Tracing

### Old API
```go
import (
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace"
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/tracegenai"
)

teardown, err := trace.Quickstart()
defer teardown()

client, err := genai.NewClient(ctx, &genai.ClientConfig{
    HTTPClient: tracegenai.Client(),
})
```

### New API
```go
import (
    "go.opentelemetry.io/otel/sdk/trace"
    "github.com/braintrustdata/braintrust-sdk-go"
    tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

tp := trace.NewTracerProvider()
defer tp.Shutdown(context.Background())

bt, err := braintrust.New(tp, braintrust.WithProject("my-project"))

client, err := genai.NewClient(ctx, &genai.ClientConfig{
    HTTPClient: tracegenai.Client(),
})
```

**Key changes:**
- Import path: `braintrust/trace/tracegenai` → `trace/contrib/genai`
- `tracegenai.Client()` API unchanged

## 6. Hosted Tasks & Scorers

### Old API
```go
import (
    "github.com/braintrustdata/braintrust-sdk-go/braintrust/eval/functions"
)

task, err := functions.GetTask[string, string](ctx, functions.TaskQueryOpts{
    Slug: "my-task-slug",
})

scorer, err := functions.GetScorer[string, string](ctx, functions.ScorerQueryOpts{
    Slug: "my-scorer-slug",
})
```

### New API
```go
evaluator := braintrust.NewEvaluator[string, string](bt)

task, err := evaluator.Tasks().Get(ctx, "my-task-slug")

scorer, err := evaluator.Scorers().Get(ctx, "my-scorer-slug")
```

**Key changes:**
- Access via evaluator: `evaluator.Tasks().Get()`, `evaluator.Scorers().Get()`
- Pass slug string directly (not options struct)

## 7. Datasets

### Old API
```go
import "github.com/braintrustdata/braintrust-sdk-go/braintrust/eval"

_, err = eval.Run(ctx, eval.Opts[Input, Output]{
    DatasetID: datasetID,
    // ... rest of config
})
```

### New API
```go
evaluator := braintrust.NewEvaluator[Input, Output](bt)

// Fetch dataset cases first
cases, err := evaluator.Datasets().Get(ctx, datasetID)

// Pass to Run
_, err = evaluator.Run(ctx, eval.Opts[Input, Output]{
    Cases: cases,
    // ... rest of config
})
```

**Key changes:**
- No `DatasetID` field in `Opts`
- Fetch cases with `evaluator.Datasets().Get(datasetID)`
- Pass cases to `Run()`

## 8. Autoevals

### Old API
```go
import "github.com/braintrustdata/braintrust-sdk-go/braintrust/autoevals"

Scorers: []eval.Scorer[string, string]{
    autoevals.NewEquals[string, string](),
}
```

### New API
```go
// Inline scorer (autoevals package removed)
Scorers: []eval.Scorer[string, string]{
    eval.NewScorer("equals", func(ctx context.Context, taskResult eval.TaskResult[string, string]) (eval.Scores, error) {
        if taskResult.Output == taskResult.Expected {
            return eval.S(1.0), nil
        }
        return eval.S(0.0), nil
    }),
}
```

**Key changes:**
- `autoevals` package removed
- Replace with inline scorers using new `TaskResult` signature

## Quick Reference: Import Path Changes

| Old Import | New Import |
|------------|------------|
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/trace` | `github.com/braintrustdata/braintrust-sdk-go` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/eval` | `github.com/braintrustdata/braintrust-sdk-go/eval` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/traceopenai` | `github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/traceanthropic` | `github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/trace/tracegenai` | `github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/eval/functions` | Use `evaluator.Tasks()` / `evaluator.Scorers()` |
| `github.com/braintrustdata/braintrust-sdk-go/braintrust/autoevals` | *Removed* - use inline scorers |
