//go:build tools

package eino

import (
	// Ensure cloudwego/eino is in the module graph for orchestrion auto-instrumentation.
	_ "github.com/cloudwego/eino/callbacks"
)
