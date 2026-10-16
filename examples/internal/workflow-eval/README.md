# Workflow and batch evaluations

Workflow and batch evaluation APIs for Go. Tasks/scorers submit provider work, persist JSON
handles, and collect results after polling or an application-verified webhook.

Run the ordinary task/scorer/classifier example without credentials:

```sh
go run ./examples/internal/workflow-eval -local
```

Run OpenAI batch tasks and batch scorers with `OPENAI_API_KEY` and
`BRAINTRUST_API_KEY` set:

```sh
go run ./examples/internal/workflow-eval
```

This creates provider batches and a Braintrust experiment. Provider batches can
take hours; this mode keeps polling in the same process. Add `-single` to exercise
`WorkflowTask` and `WorkflowScorer`, which submit one operation per case instead.
The example uses the provider SDK already included in the examples module.

## Restart with Redis

Start a local Redis server with a Unix socket, then supply its path. The example
uses `redis-cli` as its command adapter so it needs no extra Go dependency.

```sh
go run ./examples/internal/workflow-eval -redis-socket /path/to/redis.sock
# Copy RunID from the output; this command polls once and exits.
go run ./examples/internal/workflow-eval -redis-socket /path/to/redis.sock -run RUN_ID
```

Use the same flags/definition when resuming. State contains the dataset, outputs,
submission handles, and progress; callbacks are reconstructed by the program.
`-local -redis-socket /path/to/redis.sock` exercises storage without provider calls.

For webhook mode, add `-webhook` on every invocation. After the application has
verified the provider webhook signature, pass its completed batch ID:

```sh
go run ./examples/internal/workflow-eval -redis-socket /path/to/redis.sock -webhook
go run ./examples/internal/workflow-eval -redis-socket /path/to/redis.sock -webhook -external BATCH_ID
```

Task completion can submit scorer batches. Deliver those completion events too.
If an external ID occurs in multiple runs, also pass `-run RUN_ID`.
A production application calls `ProcessSubmissionResult` from its verified-event
worker; this CLI is not a webhook verification server.

## Guarantees and application responsibilities

- Serialize advancement of each run across all processes. The store operations
  are atomic, but the runtime does not implement a distributed execution lock.
  Different runs can advance independently; `MaxConcurrency` bounds callbacks
  within an invocation (default 10).
- `Start` returns a run ID even if a provider callback fails after the run is
  persisted. Resume that ID with `Poll` instead of starting a second run.
- Use the stable submission ID for provider idempotency/reconciliation. A crash
  after provider acceptance but before saving the handle can repeat `Submit`.
  Sending an idempotency header is not a guarantee that every provider honors it.
- `Collect` must stop if its yield callback returns an error. Collection can run
  again after interruption; already persisted results are ignored on replay.
  Duplicate IDs within one collection and unknown IDs are errors. Missing results
  at successful collection completion fail those items. Scorer failures preserve
  healthy scores, and failed tasks skip their scoring/classification stages.
- A collection callback can be invoked more than once; avoid non-idempotent side
  effects. Successful ordinary stages are also checkpointed, but can repeat after
  a crash before persistence.
- Inputs, outputs, metadata, parameters, and submission handles must be JSON
  serializable. `TaskOutput.UserData` is rejected because it can contain live
  process-local objects. Do not store clients or credentials in submission handles.
- Keep the workflow definition and store prefix unchanged for active runs.
  Records use a versioned schema; this format is not shared with TypeScript.
- Configure Redis retention to exceed the entire run and webhook retry window.
  Missing/expired state cannot be recovered by the SDK. Memory stores do not
  survive process exit.
- `MaxWait` flushes scorer batches only when the workflow advances, with no
  background timer. Zero waits for a full batch or the final remaining items.
- `Status` reads progress without advancing it. Completed includes failed cases;
  Failed counts cases with any stage failure. A completed run can have failures.
- A run's case records are loaded into memory when it advances. Collection saves
  each result separately and logging uses bounded HTTP requests.
- `NoSendLogs` disables workflow experiment logging, not tracing performed inside
  user callbacks. Ordinary callbacks receive trace context linked to stable case
  roots; `TaskHooks.EvalSpan` is a context carrier and cannot mutate a persisted root.

A production go-redis adapter can use the existing client:

```go
store, err := eval.NewWorkflowEvalRedisStore(
    func(ctx context.Context, args ...any) (any, error) {
        value, err := client.Do(ctx, args...).Result()
        if errors.Is(err, redis.Nil) {
            return nil, nil
        }
        return value, err
    },
    "my-workflows:", 7*24*time.Hour,
)
```

## Validation

Local workflow/store tests (Redis tests require `redis-server` and `redis-cli`):

```sh
go test -race ./eval -run TestWorkflow -count=1
```

Record the real Braintrust row-update and provider-batch tests:

```sh
VCR_MODE=record go test ./eval -run '^TestWorkflowExperimentIntegration$' -count=1
VCR_MODE=record go test ./examples/internal/workflow-eval -run '^TestWorkflowOpenAIBatches$' -timeout 30m -count=1
```

Provider completion can exceed the test window; rerun with a longer timeout and
adjust the test's context deadline if needed. The Braintrust integration has a
recorded cassette and runs in replay mode without credentials. The OpenAI test
skips replay until its genuine cassette exists; it must be recorded and replayed
before release. A local pass with that skip is not provider validation.
