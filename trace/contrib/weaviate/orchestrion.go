// Package weaviate provides automatic instrumentation for
// github.com/weaviate/weaviate-go-client/v5.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs
// these packages available at compile time.
package weaviate

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/weaviate/weaviate-go-client/v5/weaviate"
)
