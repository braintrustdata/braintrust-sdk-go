// Command nestedmodules prints nested modules in dependency order.
package main

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/braintrustdata/braintrust-sdk-go/internal/nestedmodules"
)

func main() {
	manifestPath := filepath.Join("scripts", "nested_modules.txt")
	modules, err := nestedmodules.ReadManifest(manifestPath)
	if err != nil {
		log.Fatal(err)
	}

	ordered, err := nestedmodules.DependencyOrder(".", modules)
	if err != nil {
		log.Fatal(err)
	}

	for _, module := range ordered {
		fmt.Println(module)
	}
}
