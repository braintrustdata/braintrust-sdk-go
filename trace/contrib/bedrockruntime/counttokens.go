package bedrockruntime

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel/trace"
)

// countTokensTracer handles the CountTokens operation, which estimates the
// input token count for a would-be InvokeModel or Converse request without
// running inference. There is no generation, so only prompt_tokens is set;
// there is no output_json or completion metrics for this operation.
type countTokensTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
	modelID  string
}

func (t *countTokensTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.count_tokens", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "count_tokens",
	}

	params, ok := in.(*bedrockruntime.CountTokensInput)
	if !ok || params == nil {
		return ctx, span
	}
	if params.ModelId != nil {
		t.modelID = *params.ModelId
		t.metadata["model"] = t.modelID
	}

	switch input := params.Input.(type) {
	case *types.CountTokensInputMemberInvokeModel:
		if body, ok := normalizeInvokeModelInput(t.modelID, input.Value.Body, t.metadata); ok {
			setJSONAttr(t.cfg.logger, span, "braintrust.input_json", body)
		}
		setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
		setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	case *types.CountTokensInputMemberConverse:
		setConverseInputAttrs(t.cfg.logger, span, t.metadata, params.ModelId, input.Value.Messages,
			input.Value.System, nil, input.Value.ToolConfig, false)
	default:
		setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
		setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	}
	return ctx, span
}

func (t *countTokensTracer) TagOutput(span trace.Span, out any, _ time.Time) {
	defer span.End()

	resp, ok := out.(*bedrockruntime.CountTokensOutput)
	if !ok || resp == nil || resp.InputTokens == nil {
		return
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metrics", map[string]any{
		"prompt_tokens": int64(*resp.InputTokens),
	})
}
