// Package genkit provides automatic instrumentation for Firebase Genkit.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package genkit

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/firebase/genkit/go/ai"
)
