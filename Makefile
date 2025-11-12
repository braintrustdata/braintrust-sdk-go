.PHONY: help ci build clean test test-quiet test-vcr-off test-vcr-record test-vcr-verify cover cover-path lint fmt mod-verify fix godoc examples

help:
	@echo "Available commands:"
	@echo "  help             - Show this help message"
	@echo "  build            - Build all packages"
	@echo "  test             - Run all tests (VCR replay mode, fast)"
	@echo "  test-quiet       - Run all tests (quiet - no 'ok' lines)"
	@echo "  test-vcr-off     - Run all tests without VCR (requires API keys)"
	@echo "  test-vcr-record  - Record/update VCR cassettes (requires API keys)"
	@echo "  test-vcr-verify  - Verify VCR cassettes work without API keys"
	@echo "  cover            - Run tests with coverage report"
	@echo "  cover-path       - Run coverage for specific path (e.g., make cover-path PATH=./config)"
	@echo "  clean            - Clean build artifacts and coverage files"
	@echo "  fmt              - Format Go code"
	@echo "  lint             - Run golangci-lint"
	@echo "  fix              - Run golangci-lint with auto-fix"
	@echo "  godoc            - Start godoc server"
	@echo "  examples         - Run all examples"
	@echo "  ci               - Run CI pipeline (clean, lint, test, build)"
	@echo "  precommit        - Run fmt then ci"

ci: clean lint mod-verify test build

build:
	go build ./...

clean:
	go clean
	rm -rf coverage.out coverage.html dist

test:
	VCR_MODE=replay go test ./...

test-quiet:
	VCR_MODE=replay go test ./... | grep -v -E "^ok|no test files" || true

test-vcr-off:
	VCR_MODE=off go test ./...

test-vcr-record:
	VCR_MODE=record go test ./...

# Verify that VCR cassettes work without API keys
# This ensures VCR-enabled tests can run in CI/CD without credentials
test-vcr-verify:
	env -u BRAINTRUST_API_KEY VCR_MODE=replay go test ./...

cover:
	go test $$(go list ./... | grep -v /examples/) -coverpkg=./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint fmt -d
	golangci-lint run ./...

fmt:
	golangci-lint fmt

mod-verify:
	go mod tidy
	git diff --exit-code go.mod go.sum
	go mod verify

fix: fmt
	golangci-lint run --fix

godoc:
	@echo "Starting godoc server on http://localhost:6060"
	go run golang.org/x/tools/cmd/godoc@latest -http=:6060

examples:
	@echo "Running all examples (skipping temporal)..."
	@find examples -name "*.go" ! -path "*/temporal/*" -exec sh -c 'echo "Running $$(dirname "{}")..." && cd "$$(dirname "{}")" && go run .' \;
	@echo "All examples completed!"

precommit: fmt ci
