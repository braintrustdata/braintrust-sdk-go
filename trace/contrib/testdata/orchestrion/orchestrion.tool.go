//go:build tools

// This file controls which orchestrion integrations are enabled.
// We only enable Braintrust integrations, NOT Datadog's dd-trace-go.

package main

import (
	_ "github.com/DataDog/orchestrion"
	_ "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all"
)
