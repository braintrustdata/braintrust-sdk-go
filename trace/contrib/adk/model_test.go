package adk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

// TestModelSpanHierarchy covers streaming completion, early termination, and
// provider errors. Normal requests and shared models are covered by the tool
// integration and sub-agent hierarchy tests.
func TestModelSpanHierarchy(t *testing.T) {
	for _, name := range []string{"streaming", "error"} {
		t.Run(name, func(t *testing.T) {
			ctx, tp, exporter := setupGoldenTest(t)
			modelID := goldenModelID
			if name == "error" {
				modelID = "nonexistent-model-for-tracing-test"
			}
			recoverError := false
			fallback := &model.LLMResponse{Content: genai.NewContentFromText("Recovered response", genai.RoleModel)}
			r := newGoldenRunner(ctx, t, tp, "model-hierarchy", llmagent.Config{
				Name:        "greeter",
				Model:       newTestModel(ctx, t, modelID, tracegenai.WithTracerProvider(tp)),
				Instruction: "Reply with one short greeting.",
				GenerateContentConfig: &genai.GenerateContentConfig{
					MaxOutputTokens: 100,
					ThinkingConfig:  &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(0))},
				},
				OnModelErrorCallbacks: []llmagent.OnModelErrorCallback{
					func(agent.CallbackContext, *model.LLMRequest, error) (*model.LLMResponse, error) {
						// The provider returns a real recorded 404. This is the
						// application's optional recovery policy.
						if recoverError {
							return fallback, nil
						}
						return nil, nil
					},
				},
			})
			msg := genai.NewContentFromText("Hello", genai.RoleUser)
			if name == "error" {
				for _, recovered := range []bool{false, true} {
					recoverError = recovered
					var modelErr error
					var output *genai.Content
					for ev, err := range r.Run(ctx, goldenUserID, "model-hierarchy", msg, agent.RunConfig{}) {
						if err != nil {
							modelErr = err
						}
						if ev != nil && ev.IsFinalResponse() {
							output = ev.Content
						}
					}
					if recovered {
						require.NoError(t, modelErr)
						assert.Equal(t, fallback.Content, output)
					} else {
						require.ErrorContains(t, modelErr, "404")
					}
					spans := exporter.Flush()
					assertModelSpanHierarchy(t, spans)
					for _, span := range spans {
						if span.Name() == "generate_content" || (span.Name() == "call_llm" && !recovered) {
							assert.Equal(t, codes.Error, span.Status().Code)
						} else if span.Name() == "call_llm" {
							assert.Equal(t, codes.Unset, span.Status().Code)
							assert.Empty(t, span.Events(), "recovered calls must not record an unhandled exception")
						}
					}
				}
				return
			}

			runCfg := agent.RunConfig{StreamingMode: agent.StreamingModeSSE}
			responses := runAndCollectFinal(ctx, t, r, "model-hierarchy", msg, runCfg)
			assertFinalResponse(t, responses)
			assertModelSpanHierarchy(t, exporter.Flush())

			// Reuse the same session, then stop before exhausting the iterator.
			// The provider and model spans must still end with the right parent.
			var received bool
			for ev, err := range r.Run(ctx, goldenUserID, "model-hierarchy", msg, runCfg) {
				require.NoError(t, err)
				if ev.Content != nil {
					received = true
					break
				}
			}
			require.True(t, received)
			assertModelSpanHierarchy(t, exporter.Flush())
		})
	}
}

func assertModelSpanHierarchy(t *testing.T, spans []oteltest.Span) {
	t.Helper()
	var models, providers []oteltest.Span
	for _, span := range spans {
		switch span.Name() {
		case "call_llm":
			models = append(models, span)
		case "generate_content":
			providers = append(providers, span)
		}
	}
	require.NotEmpty(t, models)
	require.Len(t, providers, len(models))
	for _, parent := range models {
		var children []oteltest.Span
		for _, child := range providers {
			if child.Stub.Parent.SpanID() == parent.Stub.SpanContext.SpanID() {
				children = append(children, child)
			}
		}
		if !assert.Len(t, children, 1, "each call_llm must parent its own generate_content span") {
			continue
		}
		assertHierarchyParent(t, children[0], []oteltest.Span{parent})
	}
}
