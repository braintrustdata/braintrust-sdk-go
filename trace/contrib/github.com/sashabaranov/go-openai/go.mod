module github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai

go 1.24.4

toolchain go1.24.11

require (
	github.com/braintrustdata/braintrust-sdk-go v0.4.0
	github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai v0.4.0
	github.com/sashabaranov/go-openai v1.41.2
	github.com/stretchr/testify v1.11.1
	go.opentelemetry.io/otel/trace v1.38.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/openai/openai-go v1.12.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.38.0 // indirect
	go.opentelemetry.io/otel/metric v1.38.0 // indirect
	go.opentelemetry.io/otel/sdk v1.38.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	gopkg.in/dnaeon/go-vcr.v3 v3.2.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/braintrustdata/braintrust-sdk-go => ../../../../..
	github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai => ../../../openai
)
