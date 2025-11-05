# Eval API Design Discussion

**Purpose:** Gather inputs for designing the eval package API for v0.1
**Date:** 2025-10-28
**Status:** Discussion Draft - No Decisions Made

---

## 1. Current State

### What Exists in `braintrust/eval/` Today

**Package Location:** `github.com/braintrustdata/braintrust-sdk-go/braintrust/eval`

**Main Entry Point:**
```go
func Run[I, R any](ctx context.Context, opts Opts[I, R]) (*Result, error)
```

**Current Opts Structure (15 fields):**
```go
type Opts[I, R any] struct {
    // Identity (one required)
    Project   string
    ProjectID string

    // Required
    Task       Task[I, R]
    Scorers    []Scorer[I, R]
    Experiment string

    // Data source (one required)
    Cases          Cases[I, R]
    Dataset        string
    DatasetID      string
    DatasetVersion string
    DatasetLimit   int

    // Optional
    Parallelism int
    Quiet       bool
    Tags        []string
    Metadata    map[string]interface{}
    Update      bool
}
```

**Current Task Signature:**
```go
type Task[I, R any] func(ctx context.Context, input I) (R, error)
```

**Other Key Types:**
```go
type Scorer[I, R any] interface {
    Name() string
    Run(ctx context.Context, input I, expected, result R, meta Metadata) (Scores, error)
}

type Cases[I, R any] interface {
    Next() (Case[I, R], error)  // Returns io.EOF when done
}

type Eval[I, R any] struct { /* unexported fields */ }
func New[I, R any](key Key, cases Cases[I, R], task Task[I, R], scorers []Scorer[I, R]) *Eval[I, R]
```

### Global State Dependencies

Current eval package depends on global state:
- Line 111: `otel.GetTracerProvider().Tracer("braintrust.eval")` - gets tracer from global
- Line 126: `braintrust.GetConfig()` - gets config from global cache
- Line 138: `auth.GetState(config.APIKey, config.OrgName)` - gets auth from global cache
- Line 497: `braintrust.Login()` - uses global config
- Line 502: `braintrust.GetConfig()` - gets global config again

### What Works Well
✅ Generic types [I, R] for type safety
✅ Iterator pattern for Cases (streaming-friendly)
✅ Scorer interface is flexible
✅ Clear separation: Run() for convenience, New() for advanced usage
✅ Context threading throughout

### What Needs Improvement
❌ Global state makes testing difficult
❌ Can't have multiple clients with different configs
❌ No hooks for runtime metadata access
❌ No trial support for non-deterministic tasks
❌ No custom error scoring
❌ No baseline comparison

---

## 2. Design Criteria (Questions for Discussion)

### (a) Backwards Incompatibility & Migration Effort

**Question:** How backwards incompatible can we be? What's acceptable migration effort?

**Considerations:**
- This is a new major version (v0.1), clean break is acceptable
- But how much code do users need to change?
- Is it mechanical (find-replace) or requires redesign?

**Examples of migration effort:**
- **Low:** Change import path only
- **Medium:** Add one parameter, update import
- **High:** Restructure code, change patterns

**For Discussion:**
- What's the target LOC (lines of code) change for typical eval usage?
- Should we provide migration helpers or codemods?
- How many existing users do we have to consider?

### (b) Feature Parity with Python/TypeScript

**Question:** Which Python/TypeScript features are must-haves for v0.1?

**Python SDK Features (from research):**
- EvalHooks - runtime access to metadata, tags, span
- Trial support - run each case N times
- Error score handlers - custom scoring on failure
- Baseline comparison - compare against previous experiment
- Progress callbacks - streaming results
- Local-only mode - don't send to Braintrust
- Timeout configuration

**TypeScript SDK Features:**
- Similar to Python
- Type-safe hooks
- Promise-based async patterns

**For Discussion:**
- Which features do 80% of users need?
- Which can wait for v0.2+?
- Should we define extension points now even if not implemented?

### (c) Required vs Optional Features

**Question:** Which features should be required fields vs optional fields?

**Current Required:**
- Task, Scorers, Experiment
- One of: Project/ProjectID
- One of: Cases/Dataset/DatasetID

**Potentially Optional:**
- Project (if client provides default)
- TrialCount (default: 1)
- ErrorScoreHandler (default behavior: score=0)
- Hooks (task can work without them)

**For Discussion:**
- Should Project be optional if using client?
- Should new features default to "off" (zero value) or require explicit opt-in?
- How do we balance required fields vs ease of use?

### (d) Fits with Client Architecture

**Question:** How should eval integrate with the new client architecture?

**Context:**
- Client provides: config, session, tracerProvider, logger
- Client is fully implemented and working
- Eval needs these resources to function

**For Discussion:**
- How does eval receive the client?
- Can eval work without a client (fallback to global state)?
- Or is client always required (clean break)?

### (e) Easy to Use

**Question:** What makes the eval API "easy to use"?

**Ease of use factors:**
- Minimal required fields for common case
- Clear error messages
- Good examples in godoc
- Type safety with generics
- IDE autocomplete friendly
- Follows Go idioms

**For Discussion:**
- What's the "happy path" code example we want users to write?
- How many lines of code for simplest eval?
- What's acceptable complexity for advanced features?

---

## 3. Key Design Questions

### Q1: Client Parameter - Where Does It Go?

**Question:** How should eval receive the client resources?

#### Option A: Client as Function Parameter

```go
func Run[I, R any](
    ctx context.Context,
    client *braintrust.Client,  // New parameter
    opts Opts[I, R],
) (*Result, error)
```

**Example:**
```go
client, _ := braintrust.NewWithOtel(braintrust.WithAPIKey("..."))
defer client.Shutdown(ctx)

result, err := eval.Run(ctx, client, eval.Opts[string, string]{
    Experiment: "test",
    Cases: cases,
    Task: task,
    Scorers: scorers,
})
```

**Pros:**
- ✅ Explicit and impossible to forget
- ✅ Compiler enforces client is provided
- ✅ Standard Go pattern (dependencies as parameters)
- ✅ Easy to test (pass mock client)
- ✅ Natural parameter order: ctx, client, opts

**Cons:**
- ❌ Adds a parameter (signature change)
- ❌ Client must be created even for simple cases
- ❌ Can't use eval without client

**Migration:**
```go
// Before
eval.Run(ctx, eval.Opts[string, string]{
    Project: "my-project",
    // ...
})

// After
client, _ := braintrust.NewWithOtel()
eval.Run(ctx, client, eval.Opts[string, string]{
    // Project optional (from client)
    // ...
})
```

---

#### Option B: Client in Opts Field

```go
type Opts[I, R any] struct {
    Client *braintrust.Client  // New field, optional or required?

    // ... existing fields
}

func Run[I, R any](ctx context.Context, opts Opts[I, R]) (*Result, error)
```

**Example:**
```go
client, _ := braintrust.NewWithOtel(braintrust.WithAPIKey("..."))

result, err := eval.Run(ctx, eval.Opts[string, string]{
    Client:     client,  // Pass in opts
    Experiment: "test",
    Cases: cases,
    Task: task,
    Scorers: scorers,
})
```

**Pros:**
- ✅ No signature change (same number of parameters)
- ✅ All config in one place (opts struct)
- ✅ Easy to add (new field)
- ✅ Can make required or optional

**Cons:**
- ❌ Less visible (buried in opts)
- ❌ Runtime check needed if required
- ❌ Could be forgotten/overlooked
- ❌ Mixes infrastructure (client) with eval config (opts)

**Migration:**
```go
// Before
eval.Run(ctx, eval.Opts[string, string]{
    Project: "my-project",
    // ...
})

// After
client, _ := braintrust.NewWithOtel()
eval.Run(ctx, eval.Opts[string, string]{
    Client: client,  // Add one field
    // ...
})
```

---

#### Option C: Client via Context

```go
// In braintrust package
func WithClient(ctx context.Context, client *Client) context.Context

// In eval package
func Run[I, R any](ctx context.Context, opts Opts[I, R]) (*Result, error) {
    // Internally: client, ok := braintrust.ClientFromContext(ctx)
}
```

**Example:**
```go
client, _ := braintrust.NewWithOtel(braintrust.WithAPIKey("..."))
ctx = braintrust.WithClient(ctx, client)

result, err := eval.Run(ctx, eval.Opts[string, string]{
    Experiment: "test",
    Cases: cases,
    Task: task,
    Scorers: scorers,
})
```

**Pros:**
- ✅ No signature change
- ✅ Go idiomatic (context for request-scoped values)
- ✅ Works with middleware patterns
- ✅ Thread client through naturally

**Cons:**
- ❌ Hidden dependency (not visible in signature)
- ❌ Hard to discover (how do users know?)
- ❌ Context values are controversial
- ❌ Could be forgotten
- ❌ Less explicit

**Migration:**
```go
// Before
eval.Run(ctx, eval.Opts[string, string]{
    Project: "my-project",
    // ...
})

// After
client, _ := braintrust.NewWithOtel()
ctx = braintrust.WithClient(ctx, client)
eval.Run(ctx, eval.Opts[string, string]{
    // ...
})
```

---

#### Option D: Evaluator Pattern

```go
// Create evaluator from client once
evaluator := eval.NewEvaluator(client)

// Use evaluator multiple times
result1, _ := evaluator.Run(ctx, eval.Opts[string, string]{...})
result2, _ := evaluator.Run(ctx, eval.Opts[int, bool]{...})
```

**Example:**
```go
client, _ := braintrust.NewWithOtel(braintrust.WithAPIKey("..."))
evaluator := eval.NewEvaluator(client)

result, err := evaluator.Run(ctx, eval.Opts[string, string]{
    Experiment: "test",
    Cases: cases,
    Task: task,
    Scorers: scorers,
})
```

**Pros:**
- ✅ Pass client once, reuse evaluator
- ✅ Object-oriented feel
- ✅ Can add methods to Evaluator
- ✅ Natural for multiple evals with same client

**Cons:**
- ❌ More API surface (Evaluator type + NewEvaluator func)
- ❌ Extra step (create evaluator first)
- ❌ Evaluator must be passed around
- ❌ Less common pattern in Go

**Migration:**
```go
// Before
eval.Run(ctx, eval.Opts[string, string]{
    Project: "my-project",
    // ...
})

// After
client, _ := braintrust.NewWithOtel()
evaluator := eval.NewEvaluator(client)
evaluator.Run(ctx, eval.Opts[string, string]{
    // ...
})
```

---

#### Comparison Matrix: Client Parameter Options

| Aspect | A: Parameter | B: Opts Field | C: Context | D: Evaluator |
|--------|-------------|--------------|-----------|--------------|
| **Explicitness** | ✅ Very explicit | ⚠️ Can be missed | ❌ Hidden | ✅ Explicit |
| **Discoverability** | ✅ In signature | ⚠️ In struct | ❌ Undiscoverable | ✅ Clear pattern |
| **Ease of Use** | ⚠️ Extra param | ✅ One struct | ✅ Thread through | ⚠️ Extra step |
| **Type Safety** | ✅ Compile-time | ⚠️ Runtime check | ⚠️ Runtime check | ✅ Compile-time |
| **Migration Effort** | Medium | Low | Low | High |
| **Go Idioms** | ✅ Standard | ✅ Common | ⚠️ Controversial | ⚠️ Less common |
| **Testing** | ✅ Easy mock | ✅ Easy mock | ⚠️ Setup context | ✅ Easy mock |

---

### Q2: EvalHooks - Required, Optional, or Future?

**Question:** Should Task functions support hooks now? If yes, how?

#### Context from v0.1.plan.md

From Section 6 (Eval Feature Parity):

> **EvalHooks (High Priority)**
> Purpose: Runtime access to evaluation metadata in task functions
>
> Suggested API:
> ```go
> type Task[I, R any] func(ctx context.Context, input I, hooks EvalHooks) (R, error)
>
> type EvalHooks interface {
>     Metadata() Metadata
>     SetMetadata(key string, val any)
>     Tags() []string
>     AddTag(tag string)
>     Expected() R
>     TrialIndex() int
>     Span() trace.Span
> }
> ```

#### Option A: Hooks Required (Change Task Signature Now)

```go
// New Task signature in v0.1
type Task[I, R any] func(ctx context.Context, input I, hooks EvalHooks) (R, error)

// EvalHooks implemented immediately
type EvalHooks interface {
    Metadata() Metadata
    SetMetadata(key string, val any)
    Tags() []string
    AddTag(tag string)
    Expected() any
    TrialIndex() int
    Span() trace.Span
}
```

**Example:**
```go
task := func(ctx context.Context, input string, hooks eval.EvalHooks) (string, error) {
    // Can access metadata, tags, span
    hooks.SetMetadata("model", "gpt-4")
    span := hooks.Span()
    span.SetAttributes(attr.String("custom", "value"))

    return processInput(input), nil
}
```

**Pros:**
- ✅ Hooks available from day 1
- ✅ Clean API (one signature)
- ✅ All tasks are uniform
- ✅ No backward compat burden later

**Cons:**
- ❌ Breaking change for all existing tasks
- ❌ Tasks must accept hooks even if unused
- ❌ Forces implementation now (can't defer)
- ❌ More complex for simple tasks

---

#### Option B: Hooks Optional (Support Both Signatures)

```go
// Two task signatures supported
type Task[I, R any] func(ctx context.Context, input I) (R, error)
type TaskWithHooks[I, R any] func(ctx context.Context, input I, hooks EvalHooks) (R, error)

// Runtime detection: eval checks signature and calls appropriately
```

**Example:**
```go
// Simple task - no hooks
simpleTask := func(ctx context.Context, input string) (string, error) {
    return "result", nil
}

// Advanced task - with hooks
advancedTask := func(ctx context.Context, input string, hooks eval.EvalHooks) (string, error) {
    hooks.SetMetadata("model", "gpt-4")
    return "result", nil
}

// Both work
eval.Run(ctx, client, eval.Opts[string, string]{Task: simpleTask, ...})
eval.Run(ctx, client, eval.Opts[string, string]{Task: advancedTask, ...})
```

**Pros:**
- ✅ Backward compatible (old tasks work)
- ✅ Optional - use hooks only if needed
- ✅ Simple tasks stay simple
- ✅ Gradual migration path

**Cons:**
- ❌ Two signatures to maintain
- ❌ Runtime reflection needed
- ❌ More complex implementation
- ❌ Could be confusing

**Implementation Note:** Use reflection to detect signature at runtime:
```go
// Pseudo-code
taskType := reflect.TypeOf(opts.Task)
if taskType.NumIn() == 3 {  // ctx, input, hooks
    // Call with hooks
} else {  // ctx, input
    // Call without hooks
}
```

---

#### Option C: Hooks via Context (No Signature Change)

```go
// Task signature stays same
type Task[I, R any] func(ctx context.Context, input I) (R, error)

// Hooks accessed from context
func HooksFromContext(ctx context.Context) EvalHooks
```

**Example:**
```go
task := func(ctx context.Context, input string) (string, error) {
    // Opt-in to hooks if needed
    if hooks, ok := eval.HooksFromContext(ctx); ok {
        hooks.SetMetadata("model", "gpt-4")
    }
    return "result", nil
}
```

**Pros:**
- ✅ No signature change
- ✅ Completely optional (check if present)
- ✅ Simple tasks don't change
- ✅ Go idiomatic (context for request values)

**Cons:**
- ❌ Hidden dependency (not in signature)
- ❌ Easy to forget hooks exist
- ❌ Context pollution
- ❌ Not discoverable

---

#### Option D: Define Interface, Don't Implement Yet (v0.2+)

```go
// v0.1: Define EvalHooks interface in godoc
// But don't change Task signature or implement

// Current Task signature stays
type Task[I, R any] func(ctx context.Context, input I) (R, error)

// Document for future:
// type EvalHooks interface { ... }  // Reserved for v0.2
```

**Pros:**
- ✅ No implementation needed now
- ✅ No breaking changes in v0.1
- ✅ Time to design properly
- ✅ Simple for v0.1 users

**Cons:**
- ❌ Feature not available in v0.1
- ❌ Breaking change when added in v0.2
- ❌ Users might need it sooner
- ❌ Delays feature parity

---

#### Comparison Matrix: EvalHooks Options

| Aspect | A: Required | B: Optional | C: Via Context | D: v0.2+ |
|--------|------------|------------|---------------|---------|
| **Breaking Change** | ❌ Yes, now | ✅ No | ✅ No | ⚠️ Yes, later |
| **Simplicity** | ✅ One signature | ⚠️ Two signatures | ✅ Same signature | ✅ No change |
| **Discoverability** | ✅ In signature | ✅ In signature | ❌ Hidden | ⚠️ Not available |
| **Flexibility** | ❌ Always required | ✅ Optional | ✅ Optional | N/A |
| **Implementation** | Complex | Very Complex | Medium | None |
| **Feature Availability** | ✅ v0.1 | ✅ v0.1 | ✅ v0.1 | ❌ v0.2 |

---

### Q3: Project/ProjectID in Opts - Required or Optional?

**Question:** If client provides a default project, should Project still be in opts?

#### Current Behavior

```go
// Opts requires one of these
type Opts[I, R any] struct {
    Project   string  // by name
    ProjectID string  // by ID
    // ...
}
```

#### Option A: Keep Required (Explicit)

```go
// Always required in Opts, client default ignored
eval.Run(ctx, client, eval.Opts[string, string]{
    Project: "my-project",  // Must provide
    // ...
})
```

**Pros:**
- ✅ Explicit which project for this eval
- ✅ Can override client default easily
- ✅ No ambiguity

**Cons:**
- ❌ Redundant if client has default
- ❌ More verbose

---

#### Option B: Optional (Use Client Default)

```go
// Optional in Opts, use client default if not provided
client, _ := braintrust.NewWithOtel(
    braintrust.WithProject("my-project"),  // Default
)

// Option 1: Use client default
eval.Run(ctx, client, eval.Opts[string, string]{
    // Project omitted - uses client default
    // ...
})

// Option 2: Override client default
eval.Run(ctx, client, eval.Opts[string, string]{
    Project: "different-project",  // Explicit override
    // ...
})
```

**Pros:**
- ✅ Less verbose for common case
- ✅ DRY - don't repeat client config
- ✅ Still allows override

**Cons:**
- ❌ Less explicit
- ❌ Could be unclear which project is used
- ❌ Need validation logic (client has default? opts has explicit?)

---

#### Option C: Remove Entirely (Always Use Client)

```go
// No Project/ProjectID fields in Opts
client, _ := braintrust.NewWithOtel(
    braintrust.WithProject("my-project"),
)

eval.Run(ctx, client, eval.Opts[string, string]{
    // Project comes only from client
    // ...
})
```

**Pros:**
- ✅ Simple - one source of truth
- ✅ Forces client config
- ✅ No ambiguity

**Cons:**
- ❌ Can't override per-eval
- ❌ Need different client for different projects
- ❌ Less flexible

---

### Q4: Reserved Fields - Include Now or Later?

**Question:** Should we add reserved fields (TrialCount, ErrorScoreHandler, etc.) to Opts now?

#### Fields in Question (from v0.1.plan.md)

```go
type Opts[I, R any] struct {
    // ... existing fields ...

    // Reserved for future (v0.2+)
    TrialCount        int             // Multiple trials per case
    ErrorScoreHandler ErrorScoreFunc  // Custom error scoring
    BaseExperiment    string          // Baseline comparison
    Timeout           time.Duration   // Eval timeout
    NoSendLogs        bool            // Local-only mode
    MaxConcurrency    int             // Fine-grained concurrency
}
```

#### Option A: Add Reserved Fields Now

Add fields to Opts with zero values = not implemented yet.

**Pros:**
- ✅ API stable - no breaking changes later
- ✅ Documents future direction
- ✅ Users can see what's coming

**Cons:**
- ❌ Clutters Opts struct
- ❌ Non-functional fields confusing
- ❌ Need clear "Reserved" documentation

---

#### Option B: Add Later in v0.2

Don't add fields until implemented.

**Pros:**
- ✅ Clean Opts in v0.1
- ✅ Only functional fields
- ✅ Less documentation burden

**Cons:**
- ❌ Breaking change in v0.2
- ❌ Users can't prepare
- ❌ API churn

---

### Q5: Package Location - Move Now or Later?

**Question:** Should we move eval to root `eval/` package now, or keep at `braintrust/eval/`?

#### Option A: Move to `eval/` Now

```
github.com/braintrustdata/braintrust-sdk-go/eval/
```

**Pros:**
- ✅ Matches v0.1.plan.md structure
- ✅ Clean break, do it all at once
- ✅ Flatter import paths

**Cons:**
- ❌ More changes in one go
- ❌ All examples break
- ❌ Need to update all imports

---

#### Option B: Keep at `braintrust/eval/` for Now

```
github.com/braintrustdata/braintrust-sdk-go/braintrust/eval/
```

**Pros:**
- ✅ Smaller change surface
- ✅ Existing imports still work
- ✅ Can move later with module rename

**Cons:**
- ❌ Two migrations (client now, location later)
- ❌ Doesn't match plan
- ❌ Keeps old structure

---

## 4. Feature Parity Analysis

### Python SDK Features

Based on v0.1.plan.md Section 6, Python SDK has:

**Implemented in Go:**
- ✅ Evaluation execution (Run)
- ✅ Cases iteration
- ✅ Scorers
- ✅ Tags & Metadata
- ✅ Dataset loading
- ✅ Experiment registration
- ✅ Parallelism

**Not Yet in Go:**
- ❌ EvalHooks (access metadata, tags, span in task)
- ❌ TrialCount (run each case N times)
- ❌ ErrorScoreHandler (custom error scoring)
- ❌ BaseExperiment (baseline comparison)
- ❌ Timeout (eval timeout)
- ❌ NoSendLogs (local-only mode)
- ❌ Progress callbacks (streaming results)

### TypeScript SDK Features

Similar to Python:
- Type-safe hooks interface
- Promise-based async
- Similar feature set

### Usage Frequency (Estimated)

**High Usage (80% of users):**
- Basic eval execution
- Cases from slice or dataset
- Standard scorers
- Tags & metadata

**Medium Usage (20-40% of users):**
- Parallelism tuning
- Custom scorers
- Multiple datasets

**Low Usage (< 20% of users):**
- EvalHooks for metadata mutation
- TrialCount for non-deterministic tasks
- Custom error scoring
- Baseline comparison

**Questions for Discussion:**
- Should all features be in v0.1?
- Which can wait for v0.2?
- How do we prioritize?

---

## 5. Migration Impact Examples

### Example 1: Simple Eval

**Before (current braintrust/eval/):**
```go
import "github.com/braintrustdata/braintrust-sdk-go/braintrust/eval"

result, err := eval.Run(context.Background(), eval.Opts[string, string]{
    Project:    "my-project",
    Experiment: "test",
    Cases: eval.NewCases([]eval.Case[string, string]{
        {Input: "hello", Expected: "world"},
    }),
    Task: func(ctx context.Context, input string) (string, error) {
        return "world", nil
    },
    Scorers: []eval.Scorer[string, string]{
        eval.NewScorer("exact", func(ctx context.Context, input, expected, result string, _ eval.Metadata) (eval.Scores, error) {
            if expected == result {
                return eval.S(1.0), nil
            }
            return eval.S(0.0), nil
        }),
    },
})
```

**After (with client parameter):**
```go
import "github.com/braintrustdata/braintrust-sdk-go/eval"  // or braintrust/eval

client, err := braintrust.NewWithOtel(
    braintrust.WithAPIKey(os.Getenv("BRAINTRUST_API_KEY")),
    braintrust.WithProject("my-project"),
)
if err != nil {
    log.Fatal(err)
}
defer client.Shutdown(context.Background())

result, err := eval.Run(context.Background(), client, eval.Opts[string, string]{
    // Project optional - from client
    Experiment: "test",
    Cases: eval.NewCases([]eval.Case[string, string]{
        {Input: "hello", Expected: "world"},
    }),
    Task: func(ctx context.Context, input string) (string, error) {
        return "world", nil
    },
    Scorers: []eval.Scorer[string, string]{
        eval.NewScorer("exact", func(ctx context.Context, input, expected, result string, _ eval.Metadata) (eval.Scores, error) {
            if expected == result {
                return eval.S(1.0), nil
            }
            return eval.S(0.0), nil
        }),
    },
})
```

**Lines Changed:** ~7 lines added (client creation), 1 line changed (eval.Run signature), 1 line removed (Project from opts)

---

### Example 2: With Dataset

**Before:**
```go
result, err := eval.Run(ctx, eval.Opts[Input, Output]{
    Project:    "my-project",
    Experiment: "dataset-test",
    Dataset:    "test-dataset",
    Task:       myTask,
    Scorers:    myScorers,
})
```

**After:**
```go
client, _ := braintrust.NewWithOtel(braintrust.WithProject("my-project"))
defer client.Shutdown(ctx)

result, err := eval.Run(ctx, client, eval.Opts[Input, Output]{
    Experiment: "dataset-test",
    Dataset:    "test-dataset",
    Task:       myTask,
    Scorers:    myScorers,
})
```

**Lines Changed:** ~3 lines added (client), 1 line changed (signature), 1 line removed (Project)

---

## 6. Open Questions for Discussion

1. **Client Integration:**
   - Which option for passing client? (parameter, opts, context, evaluator)
   - Should eval work without client (backward compat) or require it (clean break)?

2. **EvalHooks:**
   - Implement in v0.1 or defer to v0.2?
   - If v0.1: required in Task signature, optional via detection, or via context?
   - What's minimum viable hooks interface?

3. **Project Configuration:**
   - Required in Opts always?
   - Optional (use client default)?
   - Removed (always from client)?

4. **Reserved Fields:**
   - Add to Opts now as placeholders?
   - Or add in v0.2 when implementing?
   - How to document "reserved" vs "implemented"?

5. **Package Location:**
   - Move to eval/ now?
   - Or keep at braintrust/eval/ until module rename?

6. **Migration Path:**
   - Provide helper functions for migration?
   - Generate migration guide with examples?
   - Support both old and new APIs temporarily?

7. **Feature Priority:**
   - Which features are must-have for v0.1?
   - Which can wait for v0.2?
   - How to validate priority assumptions?

---

## 7. Next Steps

After discussing the above questions:

1. Make decisions on key design questions
2. Document decisions in v0.1.plan.md
3. Create detailed API specification
4. Begin implementation
5. Write tests
6. Update examples
7. Write migration guide

---

## Appendix: Related Documents

- `.plan/v0.1.plan.md` - Overall v0.1 plan
- `.plan/TODO.md` - Current implementation progress
- `braintrust/eval/eval.go` - Current implementation
- `client.go` - New client architecture (implemented)
