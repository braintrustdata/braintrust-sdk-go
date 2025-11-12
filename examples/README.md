# Braintrust Go SDK Examples

This directory contains examples demonstrating how to use the Braintrust Go SDK. Examples are organized by complexity and use case.

## Getting Started (5 minutes)

Start here to learn basic tracing with popular AI providers:

- **[openai/](openai/)** - Trace OpenAI chat completions
- **[anthropic/](anthropic/)** - Trace Anthropic Claude messages
- **[genai/](genai/)** - Trace Google Gemini requests

## Evaluations (15 minutes)

Learn how to evaluate and improve your AI applications:

- **[evals/](evals/)** - Simple eval with inline dataset and scorers
- **[datasets/](datasets/)** - Run evals against downloaded datasets
- **[dataset-api/](dataset-api/)** - Complete workflow: create datasets, use prompts, run evals
- **[scorers/](scorers/)** - Custom scoring with online and code-based scorers

## Alternative Providers & Libraries

Examples for other AI providers and client libraries:

- **[sashabaranov-openai/](sashabaranov-openai/)** - OpenAI tracing with sashabaranov/go-openai library
- **[openrouter/](openrouter/)** - Trace OpenRouter requests
- **[langchaingo/](langchaingo/)** - Trace LangChainGo multi-turn conversations

## Advanced Features (30 minutes)

More specialized use cases and integrations:

- **[manual-llm-logging/](manual-llm-logging/)** - Manually log LLM calls (for custom AI proxies)
- **[attachments/](attachments/)** - Include images and files in traces
- **[prompts/](prompts/)** - Use Braintrust hosted prompts in evaluations
- **[distributed-tracing/](distributed-tracing/)** - W3C baggage propagation across services
- **[otel/](otel/)** - Add Braintrust to existing OpenTelemetry setup

## Internal Examples

The **[internal/](internal/)** directory contains comprehensive examples that test all SDK features. These are primarily used for SDK development and validation, not for learning. See [internal/README.md](internal/README.md) for details.

## Running Examples

Each example is a standalone Go program. To run an example:

```bash
cd examples/openai
go run main.go
```

Make sure you have the required API keys set as environment variables (e.g., `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `BRAINTRUST_API_KEY`).
