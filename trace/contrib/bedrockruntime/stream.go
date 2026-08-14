package bedrockruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// converseStreamTracer handles the ConverseStream operation. StartSpan mirrors
// converseTracer; TagOutput wraps the response's event-stream reader so the
// span is finalized when the stream drains or closes.
type converseStreamTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
}

func (t *converseStreamTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.converse-stream", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "converse-stream",
	}

	params, ok := in.(*bedrockruntime.ConverseStreamInput)
	if !ok || params == nil {
		return ctx, span
	}

	setConverseInputAttrs(t.cfg.logger, span, t.metadata, params.ModelId, params.Messages, params.System,
		params.InferenceConfig, params.ToolConfig, true)
	return ctx, span
}

func (t *converseStreamTracer) TagOutput(span trace.Span, out any, start time.Time) {
	resp, ok := out.(*bedrockruntime.ConverseStreamOutput)
	if !ok || resp == nil {
		span.End()
		return
	}
	stream := resp.GetStream()
	if stream == nil || stream.Reader == nil {
		span.End()
		return
	}

	observed := &observedConverseStream{
		log:           t.cfg.logger,
		inner:         stream.Reader,
		events:        make(chan types.ConverseStreamOutput),
		done:          make(chan struct{}),
		span:          span,
		start:         start,
		metadata:      t.metadata,
		contentBlocks: map[int32]map[string]any{},
		builders:      map[int32]*strings.Builder{},
	}
	stream.Reader = observed
	// Kick off the pump. It closes the output channel and ends the span on drain.
	go observed.pump()
}

// observedConverseStream decorates a ConverseStreamOutputReader to collect
// span attributes as events flow by. It implements the
// bedrockruntime.ConverseStreamOutputReader interface so it can be
// substituted transparently for the SDK's own reader.
type observedConverseStream struct {
	log    logger.Logger
	inner  bedrockruntime.ConverseStreamOutputReader
	events chan types.ConverseStreamOutput
	done   chan struct{}

	closeOnce sync.Once
	finalOnce sync.Once

	span     trace.Span
	start    time.Time
	metadata map[string]any

	mu            sync.Mutex
	ttftRecorded  bool
	timeToFirst   time.Duration
	contentBlocks map[int32]map[string]any
	builders      map[int32]*strings.Builder
	stopReason    types.StopReason
	usage         *types.TokenUsage
}

func (o *observedConverseStream) Events() <-chan types.ConverseStreamOutput { return o.events }

func (o *observedConverseStream) Close() error {
	o.closeOnce.Do(func() { close(o.done) })
	err := o.inner.Close()
	// Ensure finalize runs even if pump is still waiting on a send that will
	// never be received (e.g. caller stopped reading mid-stream).
	o.finalize()
	return err
}

func (o *observedConverseStream) Err() error { return o.inner.Err() }

// pump drains the inner stream, observes each event, and forwards it to our
// own channel. Exits when the inner channel closes or the user calls Close().
func (o *observedConverseStream) pump() {
	defer close(o.events)
	defer o.finalize()
	for ev := range o.inner.Events() {
		o.observe(ev)
		select {
		case o.events <- ev:
		case <-o.done:
			return
		}
	}
}

// observe inspects a single event for TTFT, content accumulation, and usage.
func (o *observedConverseStream) observe(ev types.ConverseStreamOutput) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.ttftRecorded {
		o.ttftRecorded = true
		o.timeToFirst = time.Since(o.start)
	}
	switch v := ev.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		idx := int32(0)
		if v.Value.ContentBlockIndex != nil {
			idx = *v.Value.ContentBlockIndex
		}
		// A ContentBlockStart event carries the block metadata (e.g. a toolUse
		// placeholder). Seed the accumulator so deltas find the right shape.
		block := map[string]any{}
		if t, ok := v.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
			block["type"] = "tool_use"
			if t.Value.ToolUseId != nil {
				block["id"] = *t.Value.ToolUseId
			}
			if t.Value.Name != nil {
				block["name"] = *t.Value.Name
			}
		}
		o.contentBlocks[idx] = block
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		idx := int32(0)
		if v.Value.ContentBlockIndex != nil {
			idx = *v.Value.ContentBlockIndex
		}
		if _, ok := o.contentBlocks[idx]; !ok {
			o.contentBlocks[idx] = map[string]any{}
		}
		if _, ok := o.builders[idx]; !ok {
			o.builders[idx] = &strings.Builder{}
		}
		switch d := v.Value.Delta.(type) {
		case *types.ContentBlockDeltaMemberText:
			o.builders[idx].WriteString(d.Value)
			o.contentBlocks[idx]["type"] = "text"
		case *types.ContentBlockDeltaMemberToolUse:
			if d.Value.Input != nil {
				o.builders[idx].WriteString(*d.Value.Input)
			}
			o.contentBlocks[idx]["type"] = "tool_use"
		case *types.ContentBlockDeltaMemberReasoningContent:
			switch r := d.Value.(type) {
			case *types.ReasoningContentBlockDeltaMemberText:
				o.builders[idx].WriteString(r.Value)
				o.contentBlocks[idx]["type"] = "reasoning"
			case *types.ReasoningContentBlockDeltaMemberSignature:
				o.contentBlocks[idx]["signature"] = r.Value
			}
		case *types.ContentBlockDeltaMemberCitation:
			cites, _ := o.contentBlocks[idx]["citations"].([]any)
			o.contentBlocks[idx]["citations"] = append(cites, fallbackMarshal(d.Value))
		}
	case *types.ConverseStreamOutputMemberMessageStop:
		o.stopReason = v.Value.StopReason
	case *types.ConverseStreamOutputMemberMetadata:
		if v.Value.Usage != nil {
			o.usage = v.Value.Usage
		}
	}
}

// finalize sets the accumulated span attributes and ends the span. Safe to
// call multiple times (sync.Once).
func (o *observedConverseStream) finalize() {
	o.finalOnce.Do(func() {
		o.mu.Lock()
		defer o.mu.Unlock()

		// Flush text builders into the content block "text" / "input" fields.
		for idx, b := range o.builders {
			block, ok := o.contentBlocks[idx]
			if !ok {
				continue
			}
			s := b.String()
			switch block["type"] {
			case "text":
				block["text"] = s
			case "reasoning":
				block["text"] = s
			case "tool_use":
				// Bedrock sends tool input as JSON fragments. Decode the complete
				// value so streaming output matches non-streaming output. Preserve
				// malformed model output verbatim rather than dropping it.
				var input any
				if err := json.Unmarshal([]byte(s), &input); err == nil {
					block["input"] = input
				} else {
					block["input"] = s
				}
			}
		}

		if string(o.stopReason) != "" {
			o.metadata["stop_reason"] = string(o.stopReason)
		}
		setJSONAttr(o.log, o.span, "braintrust.metadata", o.metadata)

		if len(o.contentBlocks) > 0 {
			content := make([]any, 0, len(o.contentBlocks))
			for i := int32(0); i < int32(len(o.contentBlocks)); i++ {
				if block, ok := o.contentBlocks[i]; ok {
					content = append(content, block)
				}
			}
			setJSONAttr(o.log, o.span, "braintrust.output_json", []any{
				map[string]any{"role": "assistant", "content": content},
			})
		}

		metrics := make(map[string]any)
		for k, v := range parseUsageTokens(o.usage) {
			metrics[k] = v
		}
		if o.ttftRecorded {
			metrics["time_to_first_token"] = o.timeToFirst.Seconds()
		}
		setJSONAttr(o.log, o.span, "braintrust.metrics", metrics)

		if err := o.inner.Err(); err != nil {
			o.span.RecordError(err)
			o.span.SetStatus(codes.Error, err.Error())
		}
		o.span.End()
	})
}
