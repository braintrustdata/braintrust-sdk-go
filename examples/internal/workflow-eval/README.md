# Workflow and batch evaluations

Workflow and batch evaluation APIs for Go. Tasks/scorers submit provider work, persist JSON
handles, and collect results after polling or an application-verified webhook.

Run the ordinary task/scorer/classifier example without credentials:

```sh
go run ./examples/internal/workflow-eval -local
```

Experiments are retained for inspection. Use `-project SDK-297-TESTING` to select
a project (default: `go-sdk-examples`); use the same project when resuming.

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
- A poll returning `WorkflowFailed` makes `Poll` return that error on every
  later call, and the run never completes, matching Python and JS. Start a new
  run to recover. This example reports failed, expired, and cancelled OpenAI
  batches as complete instead, so requests without results are recorded as failed.
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
- `MaxWait` (scorer batches only) flushes partial batches only when the workflow advances, with no
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
VCR_MODE=record go test ./examples/internal/workflow-eval -run '^TestWorkflowOpenAIBatches$' -timeout 50h -count=1
```

OpenAI gives each batch a 24-hour completion window. This test runs a task batch
and then a scorer batch, so live recording allows 50 hours for both stages. The
test follows `go test -timeout` and stops one minute early to save the VCR recording;
`-timeout 0` disables the deadline. Live polling runs once per minute; replay does
not sleep. The devbox must remain running for the live test.

Both the Braintrust and OpenAI integration tests include genuine recorded
cassettes and replay without credentials. The provider test covers task submission,
polling, result collection, scorer submission, and final scores for two cases.
`make test` includes the provider replay test. To run it separately:

```sh
VCR_MODE=replay go test ./examples/internal/workflow-eval -run '^TestWorkflowOpenAIBatches$' -count=1
```

## TypeScript behavior reference

The workflow API was compared with TypeScript commit
[`b94cd07`](https://github.com/braintrustdata/braintrust-sdk-javascript/blob/b94cd07cfeb2977d6d9bba9a7a4f3ce491c6d637/js/src/workflow-eval.ts)
(the merge of batch workflow PR #2548).

| Behavior | Go implementation and verification |
| --- | --- |
| Single-item and batch task/scorer lifecycle | `WorkflowTask`, `WorkflowScorer`, `WorkflowBatchTask`, `WorkflowBatchScorer`; single-scorer/retry and real OpenAI batch tests |
| Polling and external-ID completion | `Poll`, `ProcessSubmissionResult`; batch-resume, shared-external-ID, and provider tests |
| Full, final, and elapsed-wait scorer batching | `WorkflowBatching`; mixed-scorer and partial-batch-wait tests |
| Out-of-order results, partial failures, missing/duplicate IDs | Stable `CustomID` mapping; batch-resume, collection-checkpoint, and duplicate-result tests |
| Interrupted collection and write retry | Per-item checkpoints; collection-checkpoint and checkpoint-write-failure tests |
| Memory/Redis continuation | Versioned JSON state; real Redis operations and separate-process restart tests |
| Ordinary stages, trials, parameters, classifications | Existing Go eval types; local-trials/failures and mixed-scorer tests |
| Experiment updates without backend changes | Existing experiment insert API; real Braintrust row-identity/score-update recording |

Go-specific API choices:

- Input and result generics follow the existing Go evaluator; expected and output
  values share the result type. Ordinary and deferred scorers use separate fields.
- Submission handles use `json.RawMessage`, and batch collection uses a `yield`
  callback instead of a JavaScript iterable. Each yielded item is checkpointed.
- `MaxWait == 0` means full/final-only batching, corresponding to an omitted
  TypeScript `maxWaitMs`; use a positive duration for elapsed-wait flushing.
- Case/trial IDs are generated and persisted for the run. The TypeScript custom
  `caseId` callback is not exposed. Treat provider `CustomID` values as opaque.
- `Status` returns progress and locally aggregated scores, rather than the
  TypeScript server experiment summary. Persistence formats are not interchangeable.
- Go logs stable case roots and ordinary callback spans. It does not reproduce
  TypeScript's internal span-resumption implementation or synthesize deferred-stage
  child spans. Scheduling, webhook verification, and per-run serialization remain
  application responsibilities as described above.
