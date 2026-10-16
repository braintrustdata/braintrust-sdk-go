// Package internal provides shared utilities for OpenTelemetry middleware tracers.
package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// BufferedReader saves data read from the readCloser and triggers an action
// when fully read or closed.
type BufferedReader struct {
	src    io.ReadCloser
	buf    *bytes.Buffer
	onDone func(io.Reader, error) // called once when fully read, failed, or closed
	once   sync.Once
	closed bool
}

// NewBufferedReader calls onDone with buffered data and any read error when fully read, failed, or closed.
func NewBufferedReader(src io.ReadCloser, onDone func(io.Reader, error)) *BufferedReader {
	return &BufferedReader{
		src:    src,
		buf:    &bytes.Buffer{},
		onDone: onDone,
	}
}

func (r *BufferedReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
	}
	if err == io.EOF {
		r.trigger(nil)
	} else if err != nil {
		r.trigger(err)
	}
	return n, err
}

// Close closes the underlying reader and triggers the onDone callback.
func (r *BufferedReader) Close() error {
	r.closed = true
	r.trigger(nil)
	return r.src.Close()
}

// trigger ensures onDone is only called once
func (r *BufferedReader) trigger(err error) {
	r.once.Do(func() {
		if r.onDone != nil {
			r.onDone(r.buf, err)
		}
	})
}

// ToInt64 converts various numeric types to int64
func ToInt64(v any) (bool, int64) {
	switch v := v.(type) {
	case float64:
		return true, int64(v)
	case int64:
		return true, v
	case int:
		return true, int64(v)
	case float32:
		return true, int64(v)
	case uint64:
		return true, int64(v)
	case uint:
		return true, int64(v)
	case uint32:
		return true, int64(v)
	default:
		return false, 0
	}
}

// SetJSONAttr is a helper function to set JSON attributes on spans
func SetJSONAttr(span trace.Span, key string, value any) error {
	jsonStr, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal attribute %s: %w", key, err)
	}
	span.SetAttributes(attribute.String(key, string(jsonStr)))
	return nil
}
