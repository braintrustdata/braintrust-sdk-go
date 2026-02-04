# Golden Tests

Golden tests validate the Braintrust SDK's integration with different AI providers by running test suites that mirror [internal/golden](https://github.com/braintrustdata/braintrust-sdk/tree/main/internal/golden) in the braintrust-sdk repo.

## Test Files

- `google_adk_test.go` – Tests for Google ADK (Agent Development Kit) integration with Braintrust tracing.

## Running Tests

Golden tests are integration tests and require API keys. They are skipped in short mode (`go test -short`) or when keys are unset.

```bash
# Run with replay/short (skips golden tests)
go test -short ./internal/golden/...

# Run golden ADK test (requires GOOGLE_API_KEY and BRAINTRUST_API_KEY)
go test -v -run=TestBasicCompletion ./internal/golden/...
```

## Requirements

- `BRAINTRUST_API_KEY` – to log traces to Braintrust (project: `golden-go-adk`)
- `GOOGLE_API_KEY` – for Google ADK / Gemini API calls

## Current Tests

All tests mirror the corresponding cases in braintrust-sdk `internal/golden/google_adk.py`.

| Test | Description |
|------|-------------|
| **TestBasicCompletion** | Basic completion; one user message (“What is the capital of France?”), collect final response. |
| **TestMultiTurn** | Two messages in same session (“Hi, my name is Alice.” then “What did I just tell you my name was?”); collect second response. |
| **TestSystemPrompt** | Pirate instruction; ask “Tell me about the weather.” |
| **TestStreaming** | Streaming mode; “Count from 1 to 10 slowly.”; collect all streamed text. |
| **TestImageInput** | Image + text (“What color is this image?”). Skips if `fixtures/test-image.png` missing. |
| **TestDocumentInput** | PDF + “What is in this document?”. Skips if `fixtures/test-document.pdf` missing. |
| **TestTemperatureVariations** | Three configs (temp 0, 1, 0.7 with top_p); “Say something creative.” per config. |
| **TestStopSequences** | `stop_sequences`: ["END", "\n\n"]; “Write a short story about a robot.” |
| **TestMetadata** | Labels in config (user_id, environment, feature); “Hello!” |
| **TestLongContext** | Long prompt (quick brown fox × 100); “How many times does the word 'fox' appear?” |
| **TestMixedContent** | Text + image + text parts. Skips if `fixtures/test-image.png` missing. |
| **TestPrefill** | Two messages: “Write a haiku about coding.” then “Here is a haiku:”; collect second response. |
| **TestShortMaxTokens** | `max_output_tokens=5`; “What is AI?” |
| **TestToolUse** | `get_weather` tool; “What is the weather like in Paris, France?” |
| **TestToolUseWithResult** | `calculate` tool; “What is 127 multiplied by 49?”; log tool call. |
| **TestReasoning** | ThinkingConfig; two messages (pattern for 2,6,12,20,30 then follow-up for 10th term and sum). |
