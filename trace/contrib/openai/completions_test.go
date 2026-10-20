package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// OpenAI retired all legacy completion models on 2026-09-28. Preserve real
// historical recordings to cover this endpoint without making unavailable calls.
// https://developers.openai.com/api/docs/deprecations#2025-09-26-legacy-gpt-model-snapshots
const legacyCompletionModel = "gpt-3.5-turbo-instruct"

func TestOpenAICompletions(t *testing.T) {
	t.Setenv("VCR_MODE", "replay")
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.CompletionNewParams{
		Model: legacyCompletionModel,
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String("What is 2+2? Answer with just the number."),
		},
		MaxTokens: openai.Int(5),
	}

	timer := oteltest.NewTimer()
	resp, err := client.Completions.New(context.Background(), params)
	timeRange := timer.Tick()
	require.NoError(err)
	require.NotNil(resp)

	require.NotEmpty(resp.Choices, "response should have at least one choice")

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	assert.Equal("Completion", ts.Name())

	inputRaw := ts.Input()
	inputJSON, err := json.Marshal(inputRaw)
	require.NoError(err)
	assert.Contains(string(inputJSON), "What is 2+2?")

	output := ts.Output()
	choices, ok := output.([]interface{})
	require.True(ok, "output should be an array of choices")
	require.NotEmpty(choices)
	firstChoice, ok := choices[0].(map[string]interface{})
	require.True(ok)
	assert.Equal(resp.Choices[0].Text, firstChoice["text"])

	metadata := ts.Metadata()
	assert.Equal("openai", metadata["provider"])
	assert.Equal("/v1/completions", metadata["endpoint"])
	assert.Equal(legacyCompletionModel, metadata["model"])

	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
}

func TestOpenAICompletionsStreaming(t *testing.T) {
	t.Setenv("VCR_MODE", "replay")
	client, _, exporter := setUpTest(t)
	assert := assert.New(t)
	require := require.New(t)

	params := openai.CompletionNewParams{
		Model: legacyCompletionModel,
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String("Count from 1 to 3:"),
		},
		MaxTokens: openai.Int(20),
	}

	stream := client.Completions.NewStreaming(context.Background(), params)
	var text string
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			text += chunk.Choices[0].Text
		}
	}
	require.NoError(stream.Err())
	require.NotEmpty(text)

	ts := exporter.FlushOne()
	assert.Equal("Completion", ts.Name())

	output := ts.Output()
	choices, ok := output.([]interface{})
	require.True(ok)
	require.NotEmpty(choices)
	firstChoice, ok := choices[0].(map[string]interface{})
	require.True(ok)
	assert.Equal(text, firstChoice["text"])

	metadata := ts.Metadata()
	assert.Equal(true, metadata["stream"])
}
