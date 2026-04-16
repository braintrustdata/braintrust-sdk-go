---
name: go-integration
description: This skill is for writing integrations to the Go SDK. Claude acts as the engineer implementing LLM provider integrations. Use when adding support for OpenAI-like providers, Anthropic-like providers, or other LLM frameworks. Covers TDD workflow, comprehensive testing (streaming/non-streaming/tokens), VCR cassettes, orchestrion auto-instrumentation, and golangci-lint compliance.
---

# Writing Go SDK Integrations

**This skill is for writing integrations.** Claude acts as the Braintrust engineer implementing new integrations to the Go SDK.

## Reference Integrations

Study existing integrations as examples. Choose the pattern that matches your provider SDK's design:

| Pattern | Reference Implementation | Use When |
|---------|-------------------------|----------|
| **Middleware** | `trace/contrib/openai/` | SDK supports `option.WithMiddleware()` |
| **Middleware** | `trace/contrib/anthropic/` | SDK supports `option.WithMiddleware()` |
| **Middleware** | `trace/contrib/genkit/` | Framework exposes a `ModelMiddleware` / `ai.WithMiddleware` hook |
| **HTTP Wrapper** | `trace/contrib/genai/` | SDK accepts custom `*http.Client` |
| **HTTP Wrapper** | `trace/contrib/github.com/sashabaranov/go-openai/` | SDK accepts custom `*http.Client` |
| **Callback** | `trace/contrib/langchaingo/` | SDK has callback/handler interface |
| **Callback** | `trace/contrib/cloudwego/eino/` | Framework has a global `callbacks.Handler` + per-invocation hook |
| **Callback** | `trace/contrib/adk/` | Framework wires per-agent / per-tool callbacks on a config struct |

**Before starting**: Examine the provider library's documentation and source to identify ALL methods that call LLM APIs.

## Integration Patterns

### Pattern 1: Middleware-Based
- **Reference**: `trace/contrib/openai/traceopenai.go`, `trace/contrib/anthropic/traceanthropic.go`
- **Key components**: `NewMiddleware()` function, `middlewareConfig` struct, URL router
- **Uses**: `trace/internal.Middleware()` helper with a router function
- **Endpoint tracers**: Separate files per endpoint (e.g., `chatcompletions.go`, `responses.go`, `messages.go`)
- **Framework-level variant**: `trace/contrib/genkit/tracegenkit.go` — wraps `ai.ModelFunc` instead of an HTTP middleware, but the shape (`NewMiddleware()` + config struct + options) is the same.

### Pattern 2: HTTP Client Wrapper
- **Reference**: `trace/contrib/genai/tracegenai.go`, `trace/contrib/github.com/sashabaranov/go-openai/traceopenai.go`
- **Key components**: `WrapClient()` / `Client()` function, custom `roundTripper` implementing `http.RoundTripper`
- **Intercepts**: Request/response at transport level

### Pattern 3: Callback-Based
- **Reference**: `trace/contrib/langchaingo/tracelangchaingo.go` (chain → llm → tool span stack)
- **Reference**: `trace/contrib/cloudwego/eino/traceeino.go` (global `callbacks.Handler` + per-invocation registration, streaming `wg`)
- **Reference**: `trace/contrib/adk/traceadk.go` (per-agent / per-model / per-tool callbacks keyed by session + invocation id)
- **Key components**: Handler / Callbacks struct implementing the framework's callback interface
- **Manages**: Span stack or session-keyed span map for nested calls (chain/agent → llm → tools)

## Endpoint-Specific Tracers

Follow the `internal.MiddlewareTracer` interface pattern:
- **Reference**: `trace/internal/middleware.go`
- **Example implementation**: `trace/contrib/anthropic/messages.go`

Key methods:
- `StartSpan()` - Parse request, start span, set input/metadata attributes
- `TagSpan()` - Parse response, set output/metrics attributes

## Streaming

For streaming responses, aggregate chunks and capture final usage:
- **Reference**: `trace/contrib/anthropic/messages.go` (streaming handling)
- **Reference**: `trace/contrib/openai/chatcompletions.go` (tool call aggregation)

## Required Components

Each integration lives in its own Go module under `trace/contrib/`. **Do in this order:**

- [ ] **Go module**: `trace/contrib/yourprovider/go.mod` (see [Module Setup](#module-setup) below)
- [ ] **Register module**: Add to `scripts/nested_modules.txt` and `go.work`
- [ ] **Core tracer**: `trace/contrib/yourprovider/traceyourprovider.go`
- [ ] **Endpoint parsers**: `trace/contrib/yourprovider/messages.go` (etc.)
- [ ] **Tests**: `trace/contrib/yourprovider/traceyourprovider_test.go`
- [ ] **VCR cassettes**: `trace/contrib/yourprovider/testdata/cassettes/`
- [ ] **Orchestrion config**: `trace/contrib/yourprovider/orchestrion.yml` — **mandatory**
- [ ] **Orchestrion deps**: `trace/contrib/yourprovider/orchestrion.go` — **mandatory**, even if the YAML template's imports appear already satisfied. Every integration ships both files; use blank imports (see `trace/contrib/openai/orchestrion.go` and `trace/contrib/anthropic/orchestrion.go`) to pull the packages referenced by `orchestrion.yml` into the module graph.
- [ ] **Update all package**: Add blank import to `trace/contrib/all/all.go` + add `require` and `replace` directives in `trace/contrib/all/go.mod`
- [ ] **Run generate**: `make generate` to regenerate `trace/contrib/all/orchestrion.yml` from the per-integration YAML files
- [ ] **Tidy all modules**: `make mod-verify` to ensure all go.mod/go.sum are consistent and the manifest matches
- [ ] **Customer example**: `examples/yourprovider/main.go` (nested paths are fine, e.g. `examples/cloudwego/eino/main.go`)
- [ ] **Internal example**: `examples/internal/yourprovider/main.go` (nested paths are fine, e.g. `examples/internal/cloudwego/eino/main.go`)

## Module Setup

Every integration is its own Go module. This keeps the root SDK dependency-free of provider SDKs — users only pull in what they need.

**Reference**: `trace/contrib/anthropic/go.mod`, `trace/contrib/openai/go.mod`

### 1. Create `go.mod`

```
module github.com/braintrustdata/braintrust-sdk-go/trace/contrib/yourprovider

go 1.24.4

toolchain go1.26.1

require (
    github.com/braintrustdata/braintrust-sdk-go v0.0.0
    github.com/yourprovider/sdk-go vX.Y.Z
    // ... other deps
)

// Required: point to repo root so local development works in go.work mode.
// The release pipeline pins v0.0.0 to the real version (e.g. v0.5.0) before
// tagging via `go mod edit` + `GOWORK=off go mod tidy`; do NOT hand-edit it.
replace github.com/braintrustdata/braintrust-sdk-go => ../../..
```

- Use `v0.0.0` when first creating the module. After the next release, the merged `go.mod` on `main` will carry the real pinned version — that is expected (see `docs/PUBLISHING.md`).
- The `replace` directive depth depends on nesting: `../../..` for `trace/contrib/yourprovider/`, `../../../..` for `trace/contrib/cloudwego/eino/`, etc.

### 2. Register the module

Add the repo-relative path to **both**:

- `scripts/nested_modules.txt` — one path per line (this drives the release pipeline)
- `go.work` — add a `use` entry so the workspace resolves the module locally

### 3. Update `trace/contrib/all`

Add a blank import in `trace/contrib/all/all.go` and update `trace/contrib/all/go.mod`:

```go
// In all.go
_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/yourprovider"
```

```
// In trace/contrib/all/go.mod — add to the existing require() and replace() blocks.
// all/go.mod already lists every nested integration's require + replace; add yours
// in the same style. For nested paths, keep the full import path intact.
require github.com/braintrustdata/braintrust-sdk-go/trace/contrib/yourprovider v0.0.0
replace github.com/braintrustdata/braintrust-sdk-go/trace/contrib/yourprovider => ../yourprovider
// Nested example (see the existing cloudwego/eino entry):
// replace github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino => ../cloudwego/eino
```

### 4. Run `make mod-verify`

This tidies all modules (with `GOWORK=off` for nested ones) and runs `scripts/check_nested_modules.sh` to verify the manifest matches actual `go.mod` files.

## Test Coverage

1. Non-streaming requests (basic + attributes + metrics)
2. Streaming requests (full consumption)
3. Early stream termination (close without reading)
4. Error handling (network errors, API errors)
5. **All critical features**:
   - Tool/function calling (if supported) — verify `span_attributes.type = "tool"`, input args, output result
   - Agentic spans — if the SDK has a callback/handler system, implement span capture for tool calls and subagent invocations in addition to LLM calls
   - Images/vision (if supported)
   - System messages (if supported)
   - Multiple messages/chat history
   - Any provider-specific features (reasoning, caching, etc.)
6. Token usage edge cases (cached tokens, reasoning tokens)
7. Multiple APIs (if provider has multiple endpoints)

## Agentic Spans

Many LLM frameworks support multi-step agents with tool calling, subagent delegation, or graph-based orchestration. When the SDK has an event/callback system, capture all of these as spans:

- **Tool calls**: `span_attributes.type = "tool"`. Set `input` to the tool arguments, `output` to the result, and `metadata.name` to the tool name.
- **Subagents / nested agents**: If the framework emits a callback when one agent calls another, capture it as a child span. Use a descriptive span type (`"function"` or `"task"`) and include the subagent name.
- **Graph nodes / chains**: If the SDK wraps components (retrievers, embedders, rerankers) in a graph and fires per-node callbacks, capture them too using the appropriate span type.

**Key pattern**: Dispatch on the SDK's callback input/output type to determine what kind of span to create. Ignore types that don't map to a recognizable span type. See `trace/contrib/cloudwego/eino/traceeino.go` (uses `model.ConvCallbackInput` / `tool.ConvCallbackInput` and falls through to `return ctx` for unknown types) and `trace/contrib/adk/traceadk.go` (session + invocation-id keyed span map across `BeforeAgent`/`AfterAgent`/`BeforeModel`/`AfterModel`/`BeforeTool`/`AfterTool`).

**Internal example must cover**: at minimum one full agentic turn — model call → tool execution → model incorporating result — to verify the full span chain appears correctly in Braintrust. See `examples/internal/adk-parallel/main.go` and `examples/internal/cloudwego/eino/main.go`.

## VCR Testing

Use `internal/vcr` and `internal/oteltest` packages for HTTP recording/replay and span verification.

**References:**
- VCR package: `internal/vcr/vcr.go`
- OTel test helpers: `internal/oteltest/oteltest.go`
- Test setup pattern: `trace/contrib/openai/traceopenai_test.go` (`setUpTest` function)
- Test assertions: `trace/contrib/anthropic/traceanthropic_test.go`

**Key patterns:**
1. Create `setUpTest(t)` helper that returns client + exporter
2. Use `oteltest.Setup(t)` for tracer provider
3. Use `vcr.NewHTTPClient(t)` for VCR-wrapped HTTP client (cassette auto-named from `t.Name()`)
4. Use dummy API key in replay mode, require real key in record/off modes
5. Use `oteltest.NewTimer()` and `timer.Tick()` for timing assertions
6. Use `exporter.FlushOne()` or `exporter.Flush()` to get spans
7. Use span helper methods: `AssertNameIs()`, `AssertInTimeRange()`, `Metadata()`, `Metrics()`, `Input()`, `Output()`

**VCR Modes:**
- `VCR_MODE=replay` (default): Use recorded cassettes
- `VCR_MODE=record`: Record new cassettes (requires API keys)
- `VCR_MODE=off`: Live API calls (requires API keys)

**Cassette location:** `testdata/cassettes/<TestFunctionName>.yaml`

## Orchestrion Auto-Instrumentation

Orchestrion provides compile-time tracing injection with zero code changes. **Every integration must ship both `orchestrion.yml` and `orchestrion.go`.** There are no exceptions.

**References:**
- Middleware pattern (SDK option): `trace/contrib/openai/orchestrion.yml`, `trace/contrib/anthropic/orchestrion.yml`
- Middleware pattern (framework option append): `trace/contrib/genkit/orchestrion.yml`
- HTTP wrapper pattern: `trace/contrib/genai/orchestrion.yml`, `trace/contrib/github.com/sashabaranov/go-openai/orchestrion.yml`
- Callback pattern (append to `AppendGlobalHandlers`): `trace/contrib/cloudwego/eino/orchestrion.yml`
- Callback pattern (struct-literal wrap): `trace/contrib/adk/orchestrion.yml`
- Callback pattern (function-call arg append): `trace/contrib/langchaingo/orchestrion.yml`
- Dependency file: `trace/contrib/openai/orchestrion.go`, `trace/contrib/anthropic/orchestrion.go`
- Combined config: `trace/contrib/all/orchestrion.yml` (auto-generated — do not hand-edit)
- Generator source: `internal/genorchestrion/` (run via `make generate`)

**Required files (all mandatory):**
1. `orchestrion.yml` — Define join-points and advice. Pick the closest reference above.
2. `orchestrion.go` — Blank imports pulling every package referenced by `orchestrion.yml` templates into the module graph (e.g. `_ "github.com/openai/openai-go/option"`). Required even when those packages are already imported elsewhere in the integration — the contract is uniform across integrations.
3. Blank import in `trace/contrib/all/all.go`.
4. Corresponding `require` + `replace` in `trace/contrib/all/go.mod`.
5. Run `make generate` to update the combined `trace/contrib/all/orchestrion.yml`, then `make mod-verify` to tidy go.mod/go.sum everywhere.

## Examples

**References:**
- Customer example pattern: `examples/openai/main.go`, `examples/anthropic/main.go`, `examples/genkit/main.go`, `examples/cloudwego/eino/main.go`, `examples/adk/main.go`
- Internal example pattern: `examples/internal/autoinstrumentation/main.go` (orchestrion end-to-end), `examples/internal/kitchensink/main.go` (multi-provider coverage), `examples/internal/genkit/main.go`, `examples/internal/cloudwego/eino/main.go`, `examples/internal/adk-parallel/main.go`
- Internal examples README: `examples/internal/README.md` — shows the "kitchen sink" convention per provider

**Customer example** (`examples/yourprovider/main.go`):
- Concise, shows basic usage with manual middleware
- Creates root span, makes API call, prints permalink
- **MUST use real model SDK** — no mocks, stubs, or fake responses

**Internal example** (`examples/internal/yourprovider/main.go`):
- Comprehensive feature coverage for CI validation
- **Must cover**: non-streaming, streaming, tool calling (agentic turn), and multiple providers where available
- Skip sections gracefully when optional API keys are not set (e.g. `if key := os.Getenv("ANTHROPIC_API_KEY"); key != ""`)
- **MUST use real model SDK** — no mocks, stubs, or fake responses
- Read API keys from environment variables

> **Rule**: Examples must always use real provider SDKs with real API keys. Never use mock models, stub implementations, or hardcoded fake responses. For callback-based integrations, use the real model implementation from the provider's extension library rather than a hand-rolled callback invoker.

## TDD Workflow

**After EVERY major change**: test -> lint -> fix -> commit cycle

1. **Write one failing test**
2. **Implement minimal code** to pass
3. **Run tests for your module**: `VCR_MODE=replay go test -C trace/contrib/yourprovider ./...`
4. **Record cassettes** (when needed): `VCR_MODE=record go test -C trace/contrib/yourprovider -v -run=TestName ./...`
5. **Run all tests**: `make test` (runs root + all nested modules)
6. **Lint**: `make lint` (fix issues before committing)
7. **Run CI**: `make ci` before committing
8. **Repeat cycle** for: basic -> streaming -> errors -> tools -> tokens

> **Note**: Integration modules are separate Go modules, so use `-C <module-dir>` to run tests in the correct module context. `make test` handles this automatically for all modules.

## Defensive Coding

- Nil checks before accessing nested fields
- Type assertions with ok checks: `if v, ok := m["key"].(string); ok { ... }`
- Error handling with proper span status
- JSON serialization safety (handle marshal errors)
- Graceful handling of missing/unexpected response fields

## Token Normalization

Normalize provider-specific token fields to standard Braintrust metric names.

**References:**
- OpenAI token parsing: `trace/contrib/openai/chatcompletions.go` (`parseUsageTokens`)
- Anthropic token parsing: `trace/contrib/anthropic/messages.go` (`parseUsageTokens`)
- Token parsing tests: `trace/contrib/openai/traceopenai_test.go` (`TestParseUsageTokens`)

**Standard metric names:**
- `prompt_tokens` - Input tokens (from `input_tokens` or `prompt_tokens`)
- `completion_tokens` - Output tokens (from `output_tokens` or `completion_tokens`)
- `tokens` - Total tokens
- `prompt_cached_tokens` - Cache read tokens
- `prompt_cache_creation_tokens` - Cache write tokens
- `completion_reasoning_tokens` - Reasoning tokens
- `time_to_first_token` - Streaming latency (seconds)

## Span Attributes

Set these standard Braintrust attributes:

| Attribute | Description |
|-----------|-------------|
| `braintrust.input_json` | Request input (messages array) |
| `braintrust.output_json` | Response output (content) |
| `braintrust.metadata` | Provider, model, parameters |
| `braintrust.metrics` | Token counts, timing |
| `braintrust.span_attributes` | Span type info |

## Linting & CI

```bash
make lint         # Run golangci-lint
make fmt          # Format code
make test         # Run tests for root + all nested modules (VCR replay)
make mod-verify   # Tidy all go.mod files and verify manifest
make ci           # Full CI: lint + mod-verify + test + build

# Test a single nested module:
VCR_MODE=replay go test -C trace/contrib/yourprovider ./...

# Record cassettes for a single nested module:
VCR_MODE=record go test -C trace/contrib/yourprovider -v -run=TestName ./...
```

## Reference Files

- Integrations (one Go module each): `trace/contrib/{openai,anthropic,genai,genkit,langchaingo,adk,cloudwego/eino,github.com/sashabaranov/go-openai}/`
- Module manifests: `trace/contrib/*/go.mod`
- Tests: `trace/contrib/*/trace*_test.go` (also `context_test.go`, `nested_test.go`, `integration_test.go`, `golden_test.go` in some integrations)
- Shared middleware helper: `trace/internal/middleware.go`, `trace/internal/utils.go`
- Test helpers: `internal/oteltest/oteltest.go`, `internal/vcr/vcr.go`
- Examples: `examples/{openai,anthropic,genai,genkit,langchaingo,adk,sashabaranov-openai}/main.go`, `examples/cloudwego/eino/main.go`
- Internal examples: `examples/internal/*/main.go` (see `examples/internal/README.md`)
- Orchestrion per-integration: `trace/contrib/*/orchestrion.yml` + `trace/contrib/*/orchestrion.go`
- Orchestrion combined (generated): `trace/contrib/all/orchestrion.yml`
- Orchestrion generator: `internal/genorchestrion/` (invoked by `make generate`)
- All-integrations meta-module: `trace/contrib/all/all.go`, `trace/contrib/all/go.mod`
- Workspace: `go.work`
- Nested module registry: `scripts/nested_modules.txt`, verified by `scripts/check_nested_modules.sh`
- Manual-instrumentation guide (user-facing): `trace/contrib/README.md`
- Release docs: `docs/PUBLISHING.md` (explains the v0.0.0 → real-version pin flow)
