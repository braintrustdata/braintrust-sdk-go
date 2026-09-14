// Package cloudflare provides automatic instrumentation for
// github.com/cloudflare/cloudflare-go/v7.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package cloudflare

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/cloudflare/cloudflare-go/v7/option"
)
