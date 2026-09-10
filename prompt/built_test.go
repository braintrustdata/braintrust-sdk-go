package prompt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestBuilt_AnnotateSpan(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	p := FromData("greeter", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "hi {{name}}"},
		Options: &Options{Model: "m"},
	})
	p.ID = "prompt-1"
	p.ProjectID = "project-1"
	p.Version = "xact-1"

	built, err := p.Build(map[string]any{"name": "Ada"})
	require.NoError(t, err)

	_, span := tp.Tracer("test").Start(context.Background(), "task")
	built.AnnotateSpan(span)
	span.End()

	taskSpan := exporter.FlushOne()
	taskSpan.AssertMetadata(map[string]any{
		"prompt": map[string]any{
			"id":         "prompt-1",
			"project_id": "project-1",
			"version":    "xact-1",
			"variables":  map[string]any{"name": "Ada"},
		},
	})
}

func TestBuilt_AnnotateSpanWithoutMetadata(t *testing.T) {
	tp, exporter := oteltest.Setup(t)

	// A prompt with no Braintrust identity has nothing to link back to, so the
	// span is left alone rather than annotated with empty fields.
	built, err := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "hi"},
		Options: &Options{Model: "m"},
	}).Build(nil)
	require.NoError(t, err)
	require.Nil(t, built.Metadata)

	_, span := tp.Tracer("test").Start(context.Background(), "task")
	built.AnnotateSpan(span)
	span.End()

	taskSpan := exporter.FlushOne()
	assert.False(t, taskSpan.HasAttr("braintrust.metadata"))
}

func TestBuilt_AnnotateNilSpan(t *testing.T) {
	built, err := FromData("p", Data{
		Prompt:  &Block{Type: BlockCompletion, Content: "hi"},
		Options: &Options{Model: "m"},
		Origin:  &Origin{PromptID: "p-1"},
	}).Build(nil)
	require.NoError(t, err)

	assert.NotPanics(t, func() { built.AnnotateSpan(nil) })
}
