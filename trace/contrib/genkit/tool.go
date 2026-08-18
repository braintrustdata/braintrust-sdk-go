package genkit

import (
	"context"

	"github.com/firebase/genkit/go/ai"
	firebasegenkit "github.com/firebase/genkit/go/genkit"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// DefineTool defines a Genkit tool whose execution is traced as a Braintrust
// tool span. It is a drop-in replacement for genkit.DefineTool.
func DefineTool[In, Out any](
	g *firebasegenkit.Genkit,
	name, description string,
	fn ai.ToolFunc[In, Out],
	opts ...ai.ToolOption,
) *ai.ToolDef[In, Out] {
	return firebasegenkit.DefineTool(g, name, description, wrapToolFunc(name, fn, true), opts...)
}

// DefineToolWithInputSchema defines a traced Genkit tool with a custom input schema.
func DefineToolWithInputSchema[Out any](
	g *firebasegenkit.Genkit,
	name, description string,
	inputSchema map[string]any,
	fn ai.ToolFunc[any, Out],
) *ai.ToolDef[any, Out] {
	return firebasegenkit.DefineTool(
		g,
		name,
		description,
		wrapToolFunc(name, fn, true),
		ai.WithInputSchema(inputSchema),
	)
}

// DefineMultipartTool defines a traced Genkit multipart tool.
func DefineMultipartTool[In any](
	g *firebasegenkit.Genkit,
	name, description string,
	fn ai.MultipartToolFunc[In],
	opts ...ai.ToolOption,
) *ai.ToolDef[In, *ai.MultipartToolResponse] {
	return firebasegenkit.DefineMultipartTool(
		g,
		name,
		description,
		wrapMultipartToolFunc(name, fn, true),
		opts...,
	)
}

// WrapToolFunc wraps a Genkit tool handler with Braintrust tool tracing.
func WrapToolFunc[In, Out any](name string, fn ai.ToolFunc[In, Out]) ai.ToolFunc[In, Out] {
	return wrapToolFunc(name, fn, false)
}

func wrapToolFunc[In, Out any](
	name string,
	fn ai.ToolFunc[In, Out],
	reuseGenkitActionSpan bool,
) ai.ToolFunc[In, Out] {
	return func(toolCtx *ai.ToolContext, input In) (Out, error) {
		ctx, span, owned := startToolSpan(toolContext(toolCtx), name, input, reuseGenkitActionSpan)
		if owned {
			defer span.End()
		}

		output, err := fn(withToolContext(ctx, toolCtx), input)
		if err != nil {
			finishToolSpan(span, nil, err)
		} else {
			finishToolSpan(span, output, nil)
		}
		return output, err
	}
}

// WrapMultipartToolFunc wraps a Genkit multipart tool handler with Braintrust tool tracing.
func WrapMultipartToolFunc[In any](name string, fn ai.MultipartToolFunc[In]) ai.MultipartToolFunc[In] {
	return wrapMultipartToolFunc(name, fn, false)
}

func wrapMultipartToolFunc[In any](
	name string,
	fn ai.MultipartToolFunc[In],
	reuseGenkitActionSpan bool,
) ai.MultipartToolFunc[In] {
	return func(toolCtx *ai.ToolContext, input In) (*ai.MultipartToolResponse, error) {
		ctx, span, owned := startToolSpan(toolContext(toolCtx), name, input, reuseGenkitActionSpan)
		if owned {
			defer span.End()
		}

		output, err := fn(withToolContext(ctx, toolCtx), input)
		if output != nil {
			finishToolSpan(span, output.Output, err)
		} else {
			finishToolSpan(span, nil, err)
		}
		return output, err
	}
}

func toolContext(toolCtx *ai.ToolContext) context.Context {
	if toolCtx == nil || toolCtx.Context == nil {
		return context.Background()
	}
	return toolCtx.Context
}

func withToolContext(ctx context.Context, toolCtx *ai.ToolContext) *ai.ToolContext {
	if toolCtx == nil {
		return &ai.ToolContext{Context: ctx}
	}
	cloned := *toolCtx
	cloned.Context = ctx
	return &cloned
}

func startToolSpan(
	ctx context.Context,
	name string,
	input any,
	reuseGenkitActionSpan bool,
) (context.Context, trace.Span, bool) {
	span := trace.SpanFromContext(ctx)
	owned := !reuseGenkitActionSpan || !span.SpanContext().IsValid()
	if owned {
		tracerProvider := otel.GetTracerProvider()
		if span.SpanContext().IsValid() {
			tracerProvider = span.TracerProvider()
		}
		ctx, span = tracerProvider.Tracer("braintrust").Start(ctx, name)
	}
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "tool"})
	_ = internal.SetJSONAttr(span, "braintrust.input_json", normalizeJSON(input))
	return ctx, span, owned
}

func finishToolSpan(span trace.Span, output any, err error) {
	if output != nil {
		_ = internal.SetJSONAttr(span, "braintrust.output_json", normalizeJSON(output))
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
