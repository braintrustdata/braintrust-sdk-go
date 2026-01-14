// Command genorchestrion generates the combined orchestrion.yml file.
//
// Usage:
//
//	go run ./internal/genorchestrion/cmd
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/braintrustdata/braintrust-sdk-go/internal/genorchestrion"
)

func main() {
	// Find repo root by looking for go.mod
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error getting working directory: %v\n", err)
		os.Exit(1)
	}

	contribDir := filepath.Join(wd, "trace", "contrib")
	outputPath := filepath.Join(contribDir, "all", "orchestrion.yml")

	if err := genorchestrion.GenerateFile(contribDir, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "error generating orchestrion.yml: %v\n", err)
		os.Exit(1)
	}
}
