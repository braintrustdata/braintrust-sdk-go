// Package traceopenai provides automatic instrumentation for github.com/sashabaranov/go-openai.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package traceopenai

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/sashabaranov/go-openai"
)
