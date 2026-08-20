.PHONY: run build help

help:
	@echo "Auto-Instrumentation Example (using Orchestrion)"
	@echo ""
	@echo "Prerequisites:"
	@echo "  go install github.com/DataDog/orchestrion@v1.12.1"
	@echo ""
	@echo "Environment variables:"
	@echo "  BRAINTRUST_API_KEY  - Braintrust API key"
	@echo "  OPENAI_API_KEY      - OpenAI API key"
	@echo "  ANTHROPIC_API_KEY   - Anthropic API key"
	@echo ""
	@echo "Commands:"
	@echo "  make run    - Run with orchestrion auto-instrumentation"
	@echo "  make build  - Build with orchestrion auto-instrumentation"

run:
	GOWORK=off orchestrion go run .

build:
	GOWORK=off orchestrion go build -o autoinstrumentation-example .
