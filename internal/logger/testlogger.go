// Package logger provides internal logging utilities.
package logger

import (
	"testing"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// FailTestLogger is a logger that fails tests when the application emits warnings or errors.
// Use this in tests to assert that application code doesn't produce unexpected warnings/errors.
type FailTestLogger struct {
	t *testing.T
}

// NewFailTestLogger creates a new test logger that fails on errors or warnings.
func NewFailTestLogger(t *testing.T) logger.Logger {
	t.Helper()
	return &FailTestLogger{t: t}
}

// Debug is a no-op. Debug logs are expected and don't indicate problems.
func (l *FailTestLogger) Debug(msg string, args ...any) {
	l.t.Helper()
	l.t.Logf("[DEBUG] %s %v", msg, args)
	// Intentionally silent
}

// Info is a no-op. Info logs are expected and don't indicate problems.
func (l *FailTestLogger) Info(msg string, args ...any) {
	l.t.Helper()
	l.t.Logf("[INFO] %s %v", msg, args)
	// Intentionally silent
}

// Warn fails the test. Application code should not emit warnings during tests.
func (l *FailTestLogger) Warn(msg string, args ...any) {
	l.t.Helper()
	l.t.Fatalf("[WARN] %s %v", msg, args)
}

// Error fails the test. Application code should not emit errors during tests.
func (l *FailTestLogger) Error(msg string, args ...any) {
	l.t.Helper()
	l.t.Fatalf("[ERROR] %s %v", msg, args)
}
