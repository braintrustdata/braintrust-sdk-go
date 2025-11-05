# Braintrust Go SDK v0.1 - Completed Work

**Completed: ~90% (as of 2025-11-04)**

This document tracks all completed work for the v0.1 rewrite. The rewrite introduces a client-based architecture (`braintrust.New()`) to replace global state and improve testability.

---

## What's Been Completed ✅

### Client Architecture (`/client.go`, `/options.go`) ✅
- `braintrust.New()` creates configured client
- Functional options pattern (WithAPIKey, WithProject, WithLogger, WithBlockingLogin, WithExporter, WithFilterAISpans, WithSpanFilterFuncs)
- Client lifecycle management (Shutdown)
- Per-client configuration (no global state!)

### Configuration Management (`/config/config.go`) ✅
- Immutable Config struct
- Environment variable loading
- No global config cache (main fix!)
- Test isolation support

### Authentication/Session (`/internal/auth/`) ✅
- Session-based async login with retry
- Exponential backoff on 5xx/network errors
- Fast failure on 4xx errors
- Non-blocking Info() and blocking Login()
- 10 comprehensive tests, all passing

### Logger Interface (`/logger/logger.go`, `/internal/tests/logger.go`) ✅
- Clean logger interface (Debug, Info, Warn, Error)
- Default logger with BRAINTRUST_DEBUG support
- Test utilities (NoopLogger, FailTestLogger)

### Test Infrastructure ✅
- Test loggers for different scenarios
- README snippet compilation tests
- Shared test helpers (tests.NewSession, oteltest.Setup)

### Trace Integration (`/client.go`, `/braintrust/trace/trace.go`) ✅
- Client creates and manages TracerProvider
- Trace setup integrated via client.setupTracing()
- Braintrust exporter configured with session auth
- Spans reach Braintrust successfully
- Test verified end-to-end

**Completed Tasks:**
- [x] Refactor `braintrust/trace/trace.go` to work with Client
- [x] Remove dependency on `braintrust.GetConfig()`
- [x] Remove dependency on global auth cache
- [x] Accept Client's TracerProvider instead of creating own
- [x] Create Braintrust OTLP exporter
- [x] Use session.Info() for org/API URL
- [x] Handle async login completion
- [x] Configure proper auth headers
- [x] Implement span processor with filtering
- [x] Apply FilterAISpans config
- [x] Apply SpanFilterFuncs config
- [x] Set parent span attributes (experiment ID, project ID)
- [x] Update Client.setupTracing()
- [x] Test end-to-end (async login, blocking login, filtering)

**Files Modified:**
- `/client.go` - completed setupTracing()
- `/braintrust/trace/trace.go` - refactored to accept Client

### Eval Integration (`/eval/`) ✅
Brand new eval package with dependency injection, no global state.

**Features:**
- Cases iterator interface (NewCases helper)
- Generic types (Opts[I, R], Case[I, R], Task[I, R], Scorer[I, R])
- Parallel execution support (configurable Parallelism)
- Result summary printing (configurable Quiet flag)
- TaskHooks for extensibility (Expected, Metadata, Tags, TaskSpan, EvalSpan)
- TaskOutput[R] wrapper for future expansion
- TaskResult[I, R] for clean scorer interface
- T() adapter for simple tasks
- NewScorer() and S() helpers
- Two client methods: RunEval() and NewEvaluator()

**Test Coverage:**
- 41+ comprehensive tests using TDD
- Parallel execution edge cases
- Score metadata formatting
- Dataset integration
- Experiment features (tags, metadata, update)
- 100% feature parity with old eval API

**Completed Tasks:**
- [x] Written comprehensive unit tests (7 initial tests)
- [x] Added 13 tests for 100% parity with old API
- [x] Created core types (Opts, Case, Cases, Task, Scorer, Score, Result)
- [x] Implemented eval execution engine
- [x] Implemented experiment registration
- [x] Created reusable Evaluator type
- [x] Wired up Client methods (RunEval, NewEvaluator)
- [x] Created working example
- [x] Refactored task API for extensibility (TaskHooks)
- [x] Harmonized scorer API (TaskResult)
- [x] All types extensible via struct fields

**Files Created:**
- `/eval/task.go` - Task types
- `/eval/scorers.go` - Scorer types
- `/eval/cases.go` - Cases iterator
- `/eval/eval.go` - Execution engine
- `/eval/experiment.go` - Experiment API
- `/eval/evaluator.go` - Reusable evaluator
- `/eval/eval_test.go` - 20+ unit tests
- `/internal/logger/testlogger.go` - Shared test logger
- `/internal/tests/session.go` - Test session helper
- `/api/experiments.go` - Experiment registration
- `/api/projects.go` - Project registration
- `/examples/internal/rewrite/main.go` - Working example

**Files Modified:**
- `/client.go` - added RunEval[I, R]() and NewEvaluator[I, R]()
- `/internal/oteltest/oteltest.go` - moved from braintrust/internal
- `/internal/auth/session.go` - added NewTestSession()

### Dataset API Integration (`/eval/dataset_api.go`, `/internal/https/`) ✅
Complete three-layer architecture for dataset loading.

**Layer 1: HTTP Foundation**
- Centralized HTTP client (`/internal/https/client.go`)
- GET, POST, DELETE methods with context support
- Automatic Bearer token auth injection
- Non-2xx status code error handling
- Debug logging (method, URL, duration, status, body)
- 30-second default timeout

**Layer 2: CRUD Operations**
- New api.Client with namespaced sub-clients
- ProjectsClient with Register()
- DatasetsClient with Create(), Insert(), Delete()
- Clean separation of concerns

**Layer 3: Typed Eval Integration**
- DatasetAPI[I, R] with typed dataset loading
- Get(ctx, id) - load dataset by ID
- Query(ctx, opts) - load dataset by name/version
- Auto-pagination with cursor support
- Limit support (configurable max records)
- Automatic type conversion from JSON to Go types
- Lazy loading (fetches batches as needed)

**Completed Tasks:**
- [x] Implement centralized HTTP client
- [x] Implement Projects client (Register only)
- [x] Implement Datasets client (Create, Insert, Delete)
- [x] Implement eval.DatasetAPI[I, R]
- [x] Auto-pagination with cursor tracking
- [x] Limit support
- [x] Returns Cases[I, R] iterator
- [x] Write 5 dataset API tests
- [x] Create full end-to-end example

**Files Created:**
- `/internal/https/client.go` - 149 lines
- `/api/client.go` - 60 lines
- `/api/projects.go` - ~60 lines
- `/api/datasets.go` - 81 lines
- `/eval/dataset_api.go` - 234 lines
- `/eval/dataset_api_test.go` - 282 lines
- `/eval/task_api.go` - TaskAPI[I, R] with Get()
- `/eval/scorer_api.go` - ScorerAPI[I, R] with Get()
- `/eval/task_api_test.go` - task API tests
- `/eval/scorer_api_test.go` - scorer API tests
- `/examples/dataset-api/main.go` - 184 lines

**Files Modified:**
- `/eval/evaluator.go` - added Datasets(), Tasks(), Scorers() methods
- `/client.go` - updated to support new API patterns

### API Client Foundation (`/api/`) ✅
Partial completion - core operations working, additional CRUD deferred to v0.2.

**Completed:**
- Base API client structure with namespaced sub-clients
- Centralized HTTP client with auth and logging
- ProjectsClient.Register(ctx, name)
- DatasetsClient.Create(ctx, req)
- DatasetsClient.Insert(ctx, id, events)
- DatasetsClient.Delete(ctx, id)
- FunctionsClient.Query(), Create(), Invoke(), Delete()
- ExperimentsClient.Register(ctx, name, projectID, opts)

**Deferred to v0.2:**
- Full CRUD for Projects (Get, List, Update, Delete)
- Full CRUD for Experiments (Get, List, Update, Delete, FetchResults)
- Full CRUD for Datasets (Get, Register, List, Update)
- Prompts client
- Retry logic in HTTP client
- Enhanced error handling (APIError type, rate limiting)

---

## Key Architectural Decisions

### Design Choices Made ✅
- API client location: `/api/` (not internal, public API)
- Three-layer architecture for datasets: HTTP → CRUD → Typed Eval
- eval.DatasetAPI uses HTTP directly (not through api.Client) for better type safety
- Keep old API during transition, deprecate in v0.1, remove in v0.2
- TaskHooks provides extensibility without breaking changes
- All types use structs for future field additions
- Functional options pattern for configuration
- Session-based auth with async login by default

### Why These Choices?
1. **Three layers for datasets:**
   - `internal/https` - Shared HTTP infrastructure
   - `api` - Generic CRUD operations
   - `eval` - Typed evaluation workflow
   - Different abstractions for different use cases

2. **eval.DatasetAPI not using api.Client:**
   - Different abstractions: CRUD vs streaming iteration
   - Type safety: Cases[I, R] vs interface{}
   - Performance: Lazy loading vs batch fetching
   - Both share same HTTP foundation for consistency

3. **Functional options:**
   - Extensible without breaking changes
   - Clear, self-documenting API
   - Optional parameters without nil checks

4. **Async login by default:**
   - Non-blocking New() for better UX
   - Blocking option available when needed
   - Session info available when ready

---

## Test Coverage

### Overall Statistics
- 41+ comprehensive tests for eval package
- 100% feature parity with old eval API
- All tests passing with `make ci`
- Coverage: ~65-85% (varies by package)

### Test Categories Covered
1. **Unit Tests:**
   - Client creation and configuration
   - Auth session lifecycle
   - Eval execution (sequential and parallel)
   - Task and scorer functionality
   - Dataset loading and pagination
   - Error handling

2. **Integration Tests:**
   - Dataset loading by ID and name
   - Dataset tags and metadata preservation
   - Experiment tags and metadata
   - Update flag behavior
   - End-to-end eval workflows

3. **Edge Cases:**
   - Parallel execution with errors
   - Score metadata formatting (single/multiple scorers)
   - Iterator errors during execution
   - All tasks failing
   - Scorer errors with partial success

---

## Critical Path Completed

```
✅ Trace Integration (DONE)
    ↓
✅ Eval Integration (DONE)
    ↓
✅ Eval API Improvements (DONE)
    ↓
✅ Dataset API Integration (DONE)
    ↓
✅ Test Parity with Old API (DONE)
    ↓
⏳ NEXT: Client Tests, Documentation, Examples
```

---

## Trace Integrations Migration (`/trace/contrib/`) ✅

All trace integrations moved to trace/contrib/ with optional TracerProvider support.

**Completed Tasks:**
- [x] Move braintrust/trace/traceanthropic → trace/contrib/anthropic
- [x] Move braintrust/trace/traceopenai → trace/contrib/openai
- [x] Move braintrust/trace/tracegenai → trace/contrib/genai
- [x] Move braintrust/trace/tracelangchaingo → trace/contrib/langchaingo
- [x] Update all imports in moved packages (use trace/internal instead of braintrust/trace/internal)
- [x] Update imports in examples and tests
- [x] Delete all old braintrust/trace/trace* packages
- [x] Add NewMiddleware() API with optional TracerProvider (openai, anthropic)
- [x] Add functional options Client()/WrapClient() API (genai)
- [x] Add TracerProvider to HandlerOptions (langchaingo)
- [x] Add Client.Permalink() method (returns string, logs warnings internally)
- [x] Fix bodyclose linter warnings (added nolint with explanations)
- [x] All tests pass: `make ci` ✅

**Implementation Details:**
1. **OpenAI/Anthropic**: Used middleware pattern with `NewMiddleware(opts ...Option)` and `WithTracerProvider()` option
2. **Genai**: Used HTTP client wrapping with `Client(opts ...Option)` and `WrapClient(client, opts ...Option)`
3. **LangChainGo**: Added TracerProvider field to existing HandlerOptions struct
4. **Permalink**: Moved from standalone function to Client method, logs warnings instead of returning errors

**Files Modified:**
- `/trace/contrib/openai/traceopenai.go` - Added config struct, NewMiddleware() with options
- `/trace/contrib/anthropic/traceanthropic.go` - Added config struct, NewMiddleware() with options
- `/trace/contrib/genai/tracegenai.go` - Added config struct, Client()/WrapClient() with options
- `/trace/contrib/langchaingo/tracelangchaingo.go` - Added TracerProvider to HandlerOptions, tracer() became Handler method
- `/client.go` - Added Permalink(span) method
- All test files updated to use `internal/oteltest` instead of `braintrust/internal/oteltest`

---

## Examples Migration to braintrust.New() ✅

All 18 examples updated to use the new `braintrust.New()` API pattern.

**Completed Tasks:**
- [x] examples/anthropic - Uses braintrust.New() + Client.Permalink()
- [x] examples/openai - Uses braintrust.New() + Client.Permalink()
- [x] examples/genai - Updated to braintrust.New()
- [x] examples/langchaingo - Updated to braintrust.New() + TracerProvider option
- [x] examples/evals - Updated to braintrust.New()
- [x] examples/datasets - Updated to braintrust.New()
- [x] examples/prompts - Updated to braintrust.New()
- [x] examples/manual-llm-logging - Updated to braintrust.New() + bt.Permalink()
- [x] examples/otel - Updated from Enable() to New()
- [x] examples/attachments - Updated from Enable() to New()
- [x] examples/scorers - Updated to braintrust.New()
- [x] examples/temporal/cmd/worker - Updated to braintrust.New()
- [x] examples/temporal/cmd/client - Updated to braintrust.New()
- [x] examples/internal/openai-v1 - Updated to braintrust.New()
- [x] examples/internal/openai-v2 - Updated to braintrust.New()
- [x] examples/internal/anthropic - Updated to braintrust.New()
- [x] examples/openrouter - Updated to braintrust.New()
- [x] examples/internal/rewrite - Fixed Permalink usage
- [x] Verify all examples build: `go build ./examples/...` ✅
- [x] All tests pass: `make ci` ✅

**Common Pattern Applied:**
```go
// OLD API
teardown, err := trace.Quickstart(opts...)
defer teardown()
// or
err := trace.Enable(tp, opts...)

// NEW API
tp := trace.NewTracerProvider()
defer tp.Shutdown(context.Background()) //nolint:errcheck
otel.SetTracerProvider(tp)

bt, err := braintrust.New(tp,
    braintrust.WithProject("go-sdk-examples"),
    braintrust.WithBlockingLogin(true),
)

// Top-level spans use file paths
_, span := tracer.Start(ctx, "examples/path/to/file.go")

// Permalink simplified (no error handling needed)
fmt.Printf("View trace: %s\n", bt.Permalink(span))
```

**Key Changes:**
1. All examples log to "go-sdk-examples" project
2. Top-level spans named with file paths (e.g., "examples/genai/main.go")
3. Replaced `trace.Permalink(span)` with `bt.Permalink(span)` (no error handling)
4. Replaced `trace.Quickstart()` with `braintrust.New()`
5. Replaced `trace.Enable()` with `braintrust.New()`
6. Updated imports from braintrust/trace to go.opentelemetry.io/otel/sdk/trace

**Files Modified:**
- 18 example files across examples/ directory
- All examples now consistent with new API patterns

---

## Latest Achievement

**Permalink Test Coverage Complete!** Added comprehensive tests for the `Permalink` function with all span types (trace/trace_test.go):
- TestPermalink_ReadWriteSpan - Tests live, recording spans
- TestPermalink_EndedSpan - Tests spans before and after End() call
- TestPermalink_NoopSpan - Tests noop tracer fallback behavior
- All 10 trace tests passing ✅
- Updated test helper to use `auth.NewTestSession()` with proper org/app URL
- Full CI suite passing: `make ci` ✅

**Previous Achievement:** All Examples Migrated! Successfully updated all 18 examples to use the new `braintrust.New()` API pattern. All examples:
- Use consistent project name ("go-sdk-examples")
- Use file paths for top-level span names
- Use simplified Client.Permalink() method
- Build successfully: `go build ./examples/...` ✅
- All tests still passing: `make ci` ✅

**Earlier Achievement:** 100% Test Parity! The new eval API has complete test coverage matching the old API. Added 13 new tests covering:
- Parallel execution edge cases (4 tests)
- Score metadata formatting (3 tests)
- Dataset integration (3 tests)
- Experiment features (3 tests: tags, metadata, update)

All 41+ tests passing. The new API supports 100% of old API functionality plus enhancements (TaskHooks, cleaner signatures, better type safety).

---

## Permalink Test Coverage (`/trace/trace_test.go`) ✅

Added comprehensive test coverage for the `Permalink` function with all span types.

**Completed Tasks:**
- [x] TestPermalink_ReadWriteSpan - Tests Permalink with live, recording spans
- [x] TestPermalink_EndedSpan - Tests Permalink before and after span.End()
- [x] TestPermalink_NoopSpan - Tests fallback URL for noop tracers
- [x] Updated test helper `newTestSession()` to use `auth.NewTestSession()`
- [x] Added noop tracer import for testing non-recording spans
- [x] All 10 trace tests passing ✅
- [x] Full CI suite passing: `make ci` ✅

**Test Coverage Details:**
1. **ReadWriteSpan (Live Span):** Verifies Permalink works with active, recording spans
2. **EndedSpan:** Demonstrates that Permalink works before ending, and returns noop fallback after ending (since `IsRecording()` returns false)
3. **NoopSpan:** Verifies fallback URL behavior with true noop spans from `noop.NewTracerProvider()`

**Implementation Details:**
- Updated `newTestSession()` helper to properly initialize auth info with OrgName and AppURL
- Used `auth.NewTestSession()` factory for test sessions with complete auth info
- Added `go.opentelemetry.io/otel/trace/noop` import for noop span testing
- All tests verify URL structure, project names, trace IDs, and span IDs

**Files Modified:**
- `/trace/trace_test.go` - Added 3 new Permalink tests (lines 483-579)

---

## What's Remaining for v0.1 (10%)

See `.plan/TODO.md` for the concise finish line checklist.

1. ~~Move integrations to trace/contrib~~ ✅ DONE
2. ~~Rewrite all examples with braintrust.New()~~ ✅ DONE
3. Solidify API design
4. Client tests (~10 tests for client.go + options.go)
5. Deprecate old API with clear comments
6. Documentation (README, MIGRATION.md, godoc)

**Estimated Time:** 1-2 days

---

## Future Work (v0.2+)

- Complete Experiments API client (full CRUD)
- Complete Prompts API client
- Full CRUD for Projects and Datasets
- HTTP retry logic in internal/https
- Enhanced error handling (APIError type, rate limiting)
- Remove deprecated braintrust/ global APIs
- TaskAPI.Query() if needed
