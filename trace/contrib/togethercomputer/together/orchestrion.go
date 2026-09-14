// Package together provides automatic instrumentation for
// github.com/togethercomputer/together-go.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package together

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/togethercomputer/together-go/option"
)
