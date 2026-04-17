// Package bedrockruntime provides automatic instrumentation for AWS Bedrock Runtime.
//
// This file ensures dependencies referenced by orchestrion.yml templates are
// in the module graph at compile time.
package bedrockruntime

import (
	// Dependencies used by orchestrion.yml template.
	_ "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)
