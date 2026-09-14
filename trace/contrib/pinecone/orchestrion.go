// Package pinecone provides automatic instrumentation for
// github.com/pinecone-io/go-pinecone/v6/pinecone.
//
// This file ensures dependencies are in the module graph for orchestrion.
// When using orchestrion, the code generated from orchestrion.yml needs these
// packages available at compile time.
package pinecone

import (
	// Dependencies used by orchestrion.yml template
	_ "github.com/pinecone-io/go-pinecone/v6/pinecone"
)
