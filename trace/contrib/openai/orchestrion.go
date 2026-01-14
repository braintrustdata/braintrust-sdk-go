// Package openai provides automatic instrumentation for the OpenAI Go SDK.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package openai

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/openai/openai-go/option"
)
