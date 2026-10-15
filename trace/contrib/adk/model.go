package adk

import (
	"context"
	"iter"
	"sync/atomic"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
)

// tracedModel bridges ADK callbacks and the context used by the provider SDK.
// BeforeModel cannot replace the invocation context, so resolve its span when
// ADK calls the model, using the same session, invocation, agent, and branch.
type tracedModel struct {
	model.LLM
	callbacks *callbacksImpl
}

type modelSpan struct {
	trace.Span
	managed atomic.Bool
}

func (m *tracedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var span *modelSpan
		if invocation, ok := ctx.(agent.InvocationContext); ok {
			key := "model:" + agentSpanKey(invocation.InvocationID(), invocation.Branch(), invocation.Agent().Name())
			stored, _ := m.callbacks.retrieveSpan(invocation.Session().ID(), key)
			span, _ = stored.(*modelSpan)
		}
		if span == nil {
			m.LLM.GenerateContent(ctx, req, stream)(yield)
			return
		}

		// AfterModel can run for every streaming chunk. Keep the span open
		// until the provider iterator (and HTTP body) finishes.
		span.managed.Store(stream)
		defer func() {
			span.managed.Store(false)
			span.End()
		}()
		for response, err := range m.LLM.GenerateContent(trace.ContextWithSpan(ctx, span), req, stream) {
			// ADK skips AfterModel when the provider returns an error.
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			if !yield(response, err) {
				return
			}
		}
	}
}
