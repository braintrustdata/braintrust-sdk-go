// Package all imports all available Braintrust tracing integrations for use with Orchestrion.
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
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai (Google GenAI)
//   - github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai (sashabaranov/go-openai)
package all

import (
	// OpenAI official SDK (github.com/openai/openai-go)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"

	// Anthropic SDK (github.com/anthropics/anthropic-sdk-go)
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"

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
)
