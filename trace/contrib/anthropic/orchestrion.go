// Package anthropic provides automatic instrumentation for the Anthropic Go SDK.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package anthropic

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/anthropics/anthropic-sdk-go/option"
)
