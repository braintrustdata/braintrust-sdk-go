package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSEWriter_Headers(t *testing.T) {
	w := httptest.NewRecorder()
	_, err := newSSEWriter(w)
	require.NoError(t, err)

	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", w.Header().Get("Connection"))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSSEWriter_WriteEvent(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeEvent("progress", map[string]string{"key": "value"})
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "event: progress\n")
	assert.Contains(t, body, `data: {"key":"value"}`)
	assert.True(t, strings.HasSuffix(body, "\n\n"))
}

func TestSSEWriter_WriteProgress(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeProgress(progressEvent{
		ObjectType: "task",
		Name:       "test-eval",
		Event:      "json_delta",
		Data:       "hello",
	})
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "event: progress\n")
	assert.Contains(t, body, `"object_type":"task"`)
	assert.Contains(t, body, `"name":"test-eval"`)
}

func TestSSEWriter_WriteSummary(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeSummary(summaryEvent{
		ExperimentName: "exp-1",
		Scores:         map[string]float64{"accuracy": 0.95},
	})
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "event: summary\n")
	assert.Contains(t, body, `"experiment_name":"exp-1"`)
	assert.Contains(t, body, `"accuracy":0.95`)
}

func TestSSEWriter_WriteDone(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeDone()
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "event: done\n")
	assert.Contains(t, body, "data: ")
}

func TestSSEWriter_WriteError(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeError(assert.AnError)
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "event: error\n")
	assert.Contains(t, body, "assert.AnError")
}

func TestSSEWriter_StringData(t *testing.T) {
	w := httptest.NewRecorder()
	sse, err := newSSEWriter(w)
	require.NoError(t, err)

	err = sse.writeEvent("test", "plain string")
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "data: plain string\n")
}
