# Internal Examples

This directory contains comprehensive examples that test all features of the Braintrust Go SDK. These examples are primarily used for:

- **SDK development and validation** - Ensuring all features work correctly
- **Regression testing** - Catching breaking changes during development
- **Feature coverage** - Demonstrating every SDK capability

## Purpose

Unlike the customer-facing examples in the parent directory, these examples are **not optimized for learning**. They are "kitchen sink" examples that exercise many features at once to verify SDK functionality.

## Examples

### Provider Kitchen Sinks

Comprehensive examples testing all features for each AI provider:

- **[anthropic/](anthropic/)** - All Anthropic features (messages, tools, streaming, thinking, vision)
- **[openai-v1/](openai-v1/)** - All OpenAI v1 features (chat, streaming, tools, vision)
- **[openai-v2/](openai-v2/)** - All OpenAI v2 features (Responses API, reasoning, conversations)
- **[genai/](genai/)** - All Gemini features (text, system instruction, multi-turn, streaming, functions, safety, JSON mode, multimodal)
- **[langchaingo/](langchaingo/)** - All LangChainGo features (simple, multi-turn, chains, tools, agents, retriever, streaming, system prompt, temperature, max tokens, prefill, stop sequences, long context, metadata)

### Specialized Testing

- **[langchaingo-anthropic/](langchaingo-anthropic/)** - LangChainGo with Anthropic provider (uses forked langchaingo)
- **[functions/](functions/)** - Functions API usage (loading tasks/scorers with FunctionOpts)
- **[rewrite/](rewrite/)** - Manual tracing and evaluator API testing
- **[email-evals/](email-evals/)** - Realistic eval example with complex scoring
- **[eval-updates/](eval-updates/)** - Testing Update option for appending to experiments
- **[temporal/](temporal/)** - Temporal workflow distributed tracing (worker + client)

## For Learning

If you're learning the Braintrust SDK, **start with the examples in the parent directory** (`../`). Those examples are concise, focused, and designed for customer education.
