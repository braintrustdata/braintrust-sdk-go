//go:build tools

package main

import (
	_ "github.com/DataDog/orchestrion"

	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all"
)
