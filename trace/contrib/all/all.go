// Package all imports all available Braintrust tracing integrations for use with Orchestrion.
//
// This package lives in its own module so users can opt into "all integrations"
// without making the root Braintrust SDK module be the "import everything" entrypoint.
//
// Import this package in your orchestrion.tool.go to enable automatic instrumentation
// for all supported LLM providers:
//
//	//go:build tools
//
//	package main
//
//	import (
//	    _ "github.com/DataDog/orchestrion"
//	    _ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all"
//	)
//
// This is equivalent to importing each integration individually:
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai (OpenAI official SDK)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic (Anthropic SDK)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime (AWS Bedrock Runtime)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai (Google GenAI)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai (sashabaranov/go-openai)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk (Google ADK)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit (Firebase Genkit)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino (CloudWeGo Eino)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo (LangChainGo)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/mcp (Model Context Protocol Go SDK)
package all

import (
	// OpenAI official SDK (github.com/openai/openai-go)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"

	// Anthropic SDK (github.com/anthropics/anthropic-sdk-go)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"

	// AWS Bedrock Runtime (github.com/aws/aws-sdk-go-v2/service/bedrockruntime)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime"

	// Google GenAI (google.golang.org/genai)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"

	// sashabaranov/go-openai
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai"

	// ADK (google.golang.org/adk)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk"

	// Genkit (github.com/firebase/genkit/go)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"

	// CloudWeGo Eino (github.com/cloudwego/eino)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino"

	// LangChainGo (github.com/tmc/langchaingo)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo"

	// MCP Go SDK (github.com/modelcontextprotocol/go-sdk)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/mcp"
)
