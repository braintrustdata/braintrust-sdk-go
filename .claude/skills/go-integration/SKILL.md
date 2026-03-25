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
| **HTTP Wrapper** | `trace/contrib/genai/` | SDK accepts custom `*http.Client` |
| **HTTP Wrapper** | `trace/contrib/github.com/sashabaranov/go-openai/` | SDK accepts custom `*http.Client` |
| **Callback** | `trace/contrib/langchaingo/` | SDK has callback/handler interface |

**Before starting**: Examine the provider library's documentation and source to identify ALL methods that call LLM APIs.

## Integration Patterns

### Pattern 1: Middleware-Based
- **Reference**: `trace/contrib/openai/traceopenai.go`
- **Key components**: `NewMiddleware()` function, `middlewareConfig` struct, URL router
- **Uses**: `trace/internal.Middleware()` helper with a router function
- **Endpoint tracers**: Separate files per endpoint (e.g., `chatcompletions.go`, `responses.go`)

### Pattern 2: HTTP Client Wrapper
- **Reference**: `trace/contrib/genai/tracegenai.go`
- **Key components**: `WrapClient()` function, custom `roundTripper` implementing `http.RoundTripper`
- **Intercepts**: Request/response at transport level

### Pattern 3: Callback-Based
- **Reference**: `trace/contrib/langchaingo/tracelangchaingo.go`
- **Key components**: Handler struct implementing SDK's callback interface
- **Manages**: Span stack for nested calls (chain → llm → tools)

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

**Do in this order:**

- [ ] **Core tracer**: `trace/contrib/yourprovider/traceyourprovider.go`
- [ ] **Endpoint parsers**: `trace/contrib/yourprovider/messages.go` (etc.)
- [ ] **Tests**: `trace/contrib/yourprovider/traceyourprovider_test.go`
- [ ] **VCR cassettes**: `trace/contrib/yourprovider/testdata/cassettes/`
- [ ] **Orchestrion config**: `trace/contrib/yourprovider/orchestrion.yml`
- [ ] **Orchestrion deps**: `trace/contrib/yourprovider/orchestrion.go`
- [ ] **Update all package**: Add import to `trace/contrib/all/all.go`
- [ ] **Run generate**: `make generate` to update combined orchestrion.yml
- [ ] **Customer example**: `examples/yourprovider/main.go`
- [ ] **Internal example**: `examples/internal/yourprovider/main.go`

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

**Key pattern**: Dispatch on the SDK's callback input/output type to determine what kind of span to create. Ignore types that don't map to a recognizable span type. Check with `tool.ConvCallbackInput`, `model.ConvCallbackInput`, etc. and fall through to `return ctx` for unknown types.

**Internal example must cover**: at minimum one full agentic turn — model call → tool execution → model incorporating result — to verify the full span chain appears correctly in Braintrust.

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

Orchestrion provides compile-time tracing injection with zero code changes.

**References:**
- Middleware pattern: `trace/contrib/openai/orchestrion.yml`
- HTTP wrapper pattern: `trace/contrib/genai/orchestrion.yml`
- Dependency file: `trace/contrib/openai/orchestrion.go`
- Combined config: `trace/contrib/all/orchestrion.yml` (auto-generated)

**Required files:**
1. `orchestrion.yml` - Define join-points and advice (follow OpenAI pattern for middleware, GenAI pattern for HTTP wrapper)
2. `orchestrion.go` - Blank imports ensuring dependencies are in module graph
3. Add import to `trace/contrib/all/all.go`
4. Run `make generate` to update combined orchestrion.yml

## Examples

**References:**
- Customer example pattern: `examples/openai/main.go`
- Internal example pattern: `examples/internal/autoinstrumentation/main.go`

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
3. **Run tests**: `make test` (uses VCR replay mode)
4. **Record cassettes** (when needed): `VCR_MODE=record go test -v -run=TestName ./path`
5. **Lint**: `make lint` (fix issues before committing)
6. **Run CI**: `make ci` before committing
7. **Repeat cycle** for: basic -> streaming -> errors -> tools -> tokens

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
make lint     # Run golangci-lint
make fmt      # Format code
make test     # Run tests (VCR replay)
make ci       # Full CI: lint + test + build
```

## Reference Files

- Integrations: `trace/contrib/{openai,anthropic,genai,langchaingo}/`
- Tests: `trace/contrib/*/trace*_test.go`
- Test helpers: `internal/oteltest/oteltest.go`, `internal/vcr/vcr.go`
- Examples: `examples/{openai,anthropic,genai}/main.go`
- Internal examples: `examples/internal/*/main.go`
- Orchestrion: `trace/contrib/*/orchestrion.yml`
