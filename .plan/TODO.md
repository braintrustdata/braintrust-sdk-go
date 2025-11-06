# Braintrust Go SDK v0.1 - Final Sprint

**Status: 90% → 100%**
**Target: 1-2 days to release**
**Last Updated: 2025-11-04**

See `.plan/DONE.md` for completed work history.

---

## Critical Path (Ordered by Priority)

### A. Move integrations to trace/contrib (Priority 1) ✅ COMPLETE
- [x] Move braintrust/trace/traceanthropic → trace/contrib/anthropic
- [x] Move braintrust/trace/traceopenai → trace/contrib/openai
- [x] Move braintrust/trace/tracegenai → trace/contrib/genai
- [x] Move braintrust/trace/tracelangchaingo → trace/contrib/langchaingo
- [x] Update all imports in moved packages
- [x] Update imports in examples and tests
- [x] Delete all old braintrust/trace/trace* packages
- [x] Add NewMiddleware() API with optional TracerProvider (openai, anthropic)
- [x] Add functional options Client()/WrapClient() API (genai)
- [x] Add TracerProvider to HandlerOptions (langchaingo)
- [x] Add Client.Permalink() method (returns string, logs warnings with client logger)
- [x] Fix bodyclose linter warnings (added nolint with explanations)
- [x] All tests pass: `make ci` ✅
- [x] Update README.md snippets to use new trace/contrib packages ✅

### D. Rewrite all examples with braintrust.New() (Priority 2) ✅ COMPLETE
- [x] examples/anthropic (uses braintrust.New() + Client.Permalink())
- [x] examples/openai (uses braintrust.New() + Client.Permalink())
- [x] examples/genai (updated to braintrust.New())
- [x] examples/langchaingo (updated to braintrust.New() + TracerProvider option)
- [x] examples/evals (updated to braintrust.New())
- [x] examples/datasets (updated to braintrust.New())
- [x] examples/prompts (updated to braintrust.New())
- [x] examples/manual-llm-logging (updated to braintrust.New() + bt.Permalink())
- [x] examples/otel (updated from Enable to New)
- [x] examples/attachments (updated from Enable to New)
- [x] examples/scorers (updated to braintrust.New())
- [x] examples/temporal/cmd/worker (updated to braintrust.New())
- [x] examples/temporal/cmd/client (updated to braintrust.New())
- [x] examples/internal/openai-v1 (updated to braintrust.New())
- [x] examples/internal/openai-v2 (updated to braintrust.New())
- [x] examples/internal/anthropic (updated to braintrust.New())
- [x] examples/openrouter (updated to braintrust.New())
- [x] examples/internal/rewrite (fixed Permalink usage)
- [x] Verify all examples build: `go build ./examples/...` ✅
- [ ] Verify all examples run: `make examples`

### B. Solidify API design (Priority 3) ✅ COMPLETE
- [x] Refactor api/ to use subpackages (api/projects/, api/datasets/, api/experiments/, api/functions/)
  - [x] Projects refactor: api/projects/types.go + projects.go with Create(), Get(), List()
  - [x] Datasets refactor: api/datasets/types.go + datasets.go with Create(), Insert(), Delete(), Fetch(), Query()
  - [x] Experiments refactor: api/experiments/types.go + experiments.go with Create(), Register(), Get(), List(), Delete()
  - [x] Functions refactor: api/functions/types.go + functions.go with Create(), Query(), Invoke(), Delete()
- [x] Enhanced Dataset Event struct with all fields (system, tracing, merge controls)
- [x] Added comprehensive integration tests for all API packages
  - [x] Projects: 86.8% coverage
  - [x] Datasets: 84.5% coverage (+ EventFields test)
  - [x] Experiments: 85.2% coverage
  - [x] Functions: 78.6% coverage
- [x] Decide: TaskAPI.Query() - REMOVED (unused, not needed)
- [x] Decide: Experiments API - Added full CRUD (Create, Register, Get, List, Delete)
- [ ] Address 2 remaining skipped tests:
  - [ ] task_api_test.go:132 (TestTaskAPI_Get_ReturnsCallableTask)
  - [ ] scorer_api_test.go:288 (TestScorerAPI_Get)
- [ ] Review public API surface (client.go, eval/, api/)
- [ ] Add godoc comments to all exported functions/types
- [ ] Lock in API for v0.1 (no breaking changes after this)

### C. Test coverage - target >85% (Priority 4)
- [ ] Client tests (~10 tests for client.go + options.go)
  - [ ] TestClient_NewEvaluator (0% coverage - lines 214-216)
  - [ ] TestClient_API (0% coverage - lines 233-248)
  - [ ] TestNew_URLOptions (WithAPIURL, WithAppURL - 0% coverage)
  - [ ] TestNew_ProjectAndOrgOptions (WithOrgName, WithProjectID - 0% coverage)
  - [ ] TestNew_SpanFilteringOptions (WithFilterAISpans, WithSpanFilterFuncs - 0% coverage)
  - [ ] TestNew_SessionCreationError (lines 82-85)
  - [ ] TestNew_SetupTracingError (lines 92-94)
  - [ ] TestNew_BlockingLoginError (lines 102-104)
  - [ ] TestSetupTracing_AddProcessorError (lines 127-129)
  - [ ] TestConvertSpanFilters_EdgeCases (lines 136-137)
- [x] Permalink tests (trace/trace_test.go) ✅
  - [x] TestPermalink_ReadWriteSpan (live span)
  - [x] TestPermalink_EndedSpan (span after End())
  - [x] TestPermalink_NoopSpan (noop tracer)
- [ ] Run `make cover` and verify >85% coverage
- [ ] Open coverage.html and review critical gaps
- [ ] Fix any critical gaps revealed by coverage

### E. Deprecate old API (Priority 5)
- [ ] Add deprecation godoc to braintrust.GetConfig()
- [ ] Add deprecation godoc to braintrust.Login()
- [ ] Add deprecation godoc to old trace functions (if any)
- [ ] Add deprecation godoc to braintrust/api/* functions
- [ ] Add "Deprecated: Use braintrust.New() instead" to all
- [ ] Audit GetConfig() usage (14 files use it)
- [ ] Note: Keep code working, remove in v0.2

### F. Documentation (Priority 6)
- [ ] Update README.md
  - [ ] Replace examples with braintrust.New() pattern
  - [ ] Show eval examples with new API
  - [ ] Show dataset loading examples
  - [ ] Add "Migration from Old API" section
- [ ] Update root doc.go
  - [ ] Document new Client architecture
  - [ ] Show quick start with braintrust.New()
  - [ ] Explain functional options pattern
- [ ] Create MIGRATION.md
  - [ ] Old pattern → New pattern side-by-side
  - [ ] List of breaking changes
  - [ ] Migration checklist
  - [ ] Timeline for deprecation (v0.1 → v0.2)
- [ ] Verify all public APIs have godoc comments

---

## Success Criteria

- [x] All tests pass: `make ci` ✅
- [ ] Coverage >85%: `make cover`
- [x] All examples build: `go build ./examples/...` ✅
- [ ] All examples run without error: `make examples`
- [ ] Documentation complete and accurate
- [ ] Old API deprecated with clear migration path
- [ ] No TODOs or FIXMEs in critical code paths
- [ ] Ready to tag v0.1.0

---

## Deferred to v0.2

**API Operations:**
- Full CRUD for Projects (Get, List, Update, Delete)
- Full CRUD for Experiments (Get, List, Update, Delete, FetchResults)
- Full CRUD for Datasets (Get, Register, List, Update)
- Prompts client (full implementation)

**Infrastructure:**
- HTTP retry logic in internal/https
- Enhanced error handling (APIError type, rate limiting)
- Remove deprecated braintrust/ global APIs entirely

**Nice-to-have:**
- TaskAPI.Query() if not needed for v0.1
- Additional hosted scorer/task features
