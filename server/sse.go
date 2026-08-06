package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// sseWriter writes Server-Sent Events to an http.ResponseWriter.
// It is safe for concurrent use from multiple goroutines.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

// newSSEWriter creates an SSE writer and sets the required response headers.
// Returns an error if the ResponseWriter does not support flushing.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing (required for SSE)")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &sseWriter{w: w, flusher: flusher}, nil
}

// writeEvent writes a single SSE event with the given type and data.
func (s *sseWriter) writeEvent(event string, data any) error {
	// Marshal data outside the mutex to reduce contention
	var dataStr string
	switch v := data.(type) {
	case string:
		dataStr = v
	case []byte:
		dataStr = string(v)
	default:
		dataBytes, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal SSE data: %w", err)
		}
		dataStr = string(dataBytes)
	}

	// SSE data field cannot contain bare newlines; each line needs its own "data:" prefix.
	// Replace newlines with SSE multi-line format.
	dataStr = strings.ReplaceAll(dataStr, "\n", "\ndata: ")

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, dataStr); err != nil {
		return fmt.Errorf("failed to write SSE event: %w", err)
	}
	s.flusher.Flush()
	return nil
}

// writeProgress writes a progress event for a completed evaluation case.
func (s *sseWriter) writeProgress(data progressEvent) error {
	return s.writeEvent("progress", data)
}

// writeSummary writes the final summary event with aggregated scores.
func (s *sseWriter) writeSummary(data summaryEvent) error {
	return s.writeEvent("summary", data)
}

// writeDone writes the terminal done event.
func (s *sseWriter) writeDone() error {
	return s.writeEvent("done", "")
}

// writeError writes an error event.
func (s *sseWriter) writeError(err error) error {
	return s.writeEvent("error", map[string]string{"error": err.Error()})
}
