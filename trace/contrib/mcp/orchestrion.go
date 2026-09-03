// Package tracemcp provides automatic instrumentation for the official MCP Go SDK.
//
// This file ensures dependencies are in the module graph for orchestrion.
package tracemcp

import (
	// Dependency used by orchestrion.yml template
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
