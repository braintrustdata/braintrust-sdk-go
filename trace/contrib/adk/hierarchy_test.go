package adk

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/workflowagents/loopagent"
	"google.golang.org/adk/agent/workflowagents/parallelagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

// TestSubAgentHierarchy records real Gemini requests for child and peer
// transfers, repeated sequential calls, and parallel branches.
func TestSubAgentHierarchy(t *testing.T) {
	for _, workflow := range []string{"delegation", "peer_transfer", "loop", "parallel"} {
		t.Run(workflow, func(t *testing.T) {
			// Loops exercise manual callback nesting in an empty branch. The
			// parallel tree mixes helper and manual callbacks to cover both
			// installation APIs without duplicating the workflow.
			manualCallbacks := func(name string) bool {
				return workflow == "loop" || (workflow == "parallel" && (name == "root" || name == "sibling_middle"))
			}
			tp, exporter := oteltest.Setup(t)
			ctx, outer := tp.Tracer("test").Start(t.Context(), "caller")
			var providerOpts []tracegenai.Option
			if workflow != "loop" {
				providerOpts = append(providerOpts, tracegenai.WithTracerProvider(tp))
			}
			llm := newTestModel(ctx, t, goldenModelID, providerOpts...)
			cb := NewCallbacks(WithTracerProvider(tp))
			var parallelModels atomic.Int32
			var parallelReady chan struct{}
			newLeaf := func(name string) agent.Agent {
				cfg := llmagent.Config{
					Name: name, Model: llm, Instruction: "Reply with one short greeting.",
					DisallowTransferToParent: true, DisallowTransferToPeers: true,
					GenerateContentConfig: &genai.GenerateContentConfig{
						MaxOutputTokens: 100,
						Temperature:     genai.Ptr(float32(0)),
						ThinkingConfig:  &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(0))},
					},
				}
				if manualCallbacks(name) {
					cfg.BeforeAgentCallbacks = []agent.BeforeAgentCallback{cb.BeforeAgent}
					cfg.AfterAgentCallbacks = []agent.AfterAgentCallback{cb.AfterAgent}
					cfg.BeforeModelCallbacks = []llmagent.BeforeModelCallback{cb.BeforeModel}
					cfg.AfterModelCallbacks = []llmagent.AfterModelCallback{cb.AfterModel}
				} else {
					AddLLMAgentCallbacks(&cfg, WithCallback(cb))
				}
				if workflow == "parallel" {
					// Keep both model spans active before either response arrives.
					// This also avoids ADK v0.4's session event read/write race
					// between request preprocessing and response persistence.
					cfg.BeforeModelCallbacks = append(cfg.BeforeModelCallbacks, func(ctx agent.CallbackContext, _ *model.LLMRequest) (*model.LLMResponse, error) {
						if parallelModels.Add(-1) == 0 {
							close(parallelReady)
						}
						select {
						case <-parallelReady:
							return nil, nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					})
				}
				leaf, err := llmagent.New(cfg)
				require.NoError(t, err)
				return leaf
			}
			newDelegator := func(name, target string, children ...agent.Agent) agent.Agent {
				cfg := llmagent.Config{
					Name: name, Model: llm, Description: "Delegates to its child agent.", SubAgents: children,
					Instruction:              fmt.Sprintf("Always transfer to %s using transfer_to_agent. Do not answer yourself.", target),
					DisallowTransferToParent: true, DisallowTransferToPeers: workflow != "peer_transfer",
					GenerateContentConfig: &genai.GenerateContentConfig{
						MaxOutputTokens: 300,
						Temperature:     genai.Ptr(float32(0)),
						ThinkingConfig:  &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(0))},
						ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
							Mode: genai.FunctionCallingConfigModeAny, AllowedFunctionNames: []string{"transfer_to_agent"},
						}},
					},
				}
				AddLLMAgentCallbacks(&cfg, WithCallback(cb))
				a, err := llmagent.New(cfg)
				require.NoError(t, err)
				return a
			}
			newWorkflow := func(name string, children ...agent.Agent) agent.Agent {
				cfg := agent.Config{Name: name, SubAgents: children}
				if manualCallbacks(name) {
					cfg.BeforeAgentCallbacks = []agent.BeforeAgentCallback{cb.BeforeAgent}
					cfg.AfterAgentCallbacks = []agent.AfterAgentCallback{cb.AfterAgent}
				} else {
					AddAgentCallbacks(&cfg, WithCallback(cb))
				}
				var a agent.Agent
				var err error
				if workflow == "parallel" {
					a, err = parallelagent.New(parallelagent.Config{AgentConfig: cfg})
				} else {
					a, err = loopagent.New(loopagent.Config{AgentConfig: cfg, MaxIterations: 2})
				}
				require.NoError(t, err)
				return a
			}
			leaf := newLeaf("leaf")
			var root agent.Agent
			if workflow == "delegation" || workflow == "peer_transfer" {
				children := []agent.Agent{leaf}
				if workflow == "peer_transfer" {
					children = nil
				}
				middle := newDelegator("middle", "leaf", children...)
				children = []agent.Agent{middle}
				if workflow == "peer_transfer" {
					// The configured tree has siblings, but the execution is
					// root -> middle -> leaf because middle transfers to its peer.
					children = append(children, leaf)
				}
				root = newDelegator("root", "middle", children...)
			} else {
				children := []agent.Agent{newWorkflow("middle", leaf)}
				if workflow == "parallel" {
					children = append(children, newWorkflow("sibling_middle", newLeaf("sibling_leaf")))
				}
				root = newWorkflow("root", children...)
			}
			svc := session.InMemoryService()
			_, err := svc.Create(ctx, &session.CreateRequest{AppName: goldenAppName, UserID: goldenUserID, SessionID: "hierarchy"})
			require.NoError(t, err)
			r, err := runner.New(runner.Config{AppName: goldenAppName, Agent: root, SessionService: svc})
			require.NoError(t, err)

			// Run twice in the same session to verify cleanup and fresh root parenting.
			for range 2 {
				if workflow == "parallel" {
					parallelModels.Store(2)
					parallelReady = make(chan struct{})
				}
				runAndCollectFinal(ctx, t, r, "hierarchy", genai.NewContentFromText("Hello", genai.RoleUser), agent.RunConfig{})
				spans := exporter.Flush()
				if workflow != "loop" {
					assertModelSpanHierarchy(t, spans)
				}
				agents := map[string][]oteltest.Span{}
				var models []oteltest.Span
				for _, span := range spans {
					if span.Name() == "call_llm" {
						models = append(models, span)
					} else {
						agents[span.Name()] = append(agents[span.Name()], span)
					}
				}
				require.Len(t, agents["agent_run [root]"], 1)
				rootSpan := agents["agent_run [root]"][0]
				assert.Equal(t, outer.SpanContext().SpanID(), rootSpan.Stub.Parent.SpanID())
				middleCount, leafCount := 1, 1
				if workflow == "loop" {
					middleCount, leafCount = 2, 4
				}
				require.Len(t, agents["agent_run [middle]"], middleCount)
				require.Len(t, agents["agent_run [leaf]"], leafCount)
				modelCount := leafCount
				if workflow == "parallel" {
					modelCount++
				}
				if workflow == "delegation" || workflow == "peer_transfer" {
					modelCount += 2
				}
				require.Len(t, models, modelCount)
				for _, span := range agents["agent_run [middle]"] {
					span.AssertChildOf(&rootSpan)
				}
				for _, span := range agents["agent_run [leaf]"] {
					assertHierarchyParent(t, span, agents["agent_run [middle]"])
				}
				if workflow == "parallel" {
					require.Len(t, agents["agent_run [sibling_middle]"], 1)
					require.Len(t, agents["agent_run [sibling_leaf]"], 1)
					siblingMiddle := agents["agent_run [sibling_middle]"][0]
					siblingMiddle.AssertChildOf(&rootSpan)
					assertHierarchyParent(t, agents["agent_run [sibling_leaf]"][0], agents["agent_run [sibling_middle]"])
				}
				for _, span := range models {
					name := span.Attr("adk.agent.name").String()
					assertHierarchyParent(t, span, agents[fmt.Sprintf("agent_run [%s]", name)])
				}
			}
			outer.End()
		})
	}
}

func assertHierarchyParent(t *testing.T, child oteltest.Span, parents []oteltest.Span) {
	t.Helper()
	for _, parent := range parents {
		if child.Stub.Parent.SpanID() == parent.Stub.SpanContext.SpanID() {
			child.AssertChildOf(&parent)
			assert.False(t, child.Stub.StartTime.Before(parent.Stub.StartTime))
			assert.False(t, child.Stub.EndTime.After(parent.Stub.EndTime))
			return
		}
	}
	t.Errorf("span %q has no matching parent", child.Name())
}
