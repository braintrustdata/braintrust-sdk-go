// Package genai provides automatic instrumentation for google.golang.org/genai.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package genai

import (
	// Dependencies used by orchestrion.yml template
	_ "google.golang.org/genai"
)
