.PHONY: help ci build clean test test-quiet test-vcr-off test-vcr-record test-vcr-verify cover cover-path lint fmt mod-verify fix godoc examples release generate check-nested-modules

# Releasable nested modules, read from the manifest at make-time.
NESTED_MODULE_DIRS := $(shell ./scripts/list_nested_modules.sh)

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
	@echo "  check-nested-modules - Verify releasable nested module manifest"
	@echo "  godoc            - Start godoc server"
	@echo "  examples         - Run all examples"
	@echo "  generate         - Generate combined orchestrion.yml"
	@echo "  ci               - Run CI pipeline (clean, lint, test, build)"
	@echo "  precommit        - Run fmt then ci"
	@echo "  release          - Publish release with goreleaser"

ci: clean lint mod-verify test build

build:
	go build ./...
	for dir in $(NESTED_MODULE_DIRS); do go build -C $$dir ./...; done

clean:
	go clean
	rm -rf coverage.out coverage.html dist

test:
	VCR_MODE=replay go test ./...
	for dir in $(NESTED_MODULE_DIRS); do VCR_MODE=replay go test -C $$dir ./...; done

test-quiet:
	VCR_MODE=replay go test ./... | grep -v -E "^ok|no test files|^\\?" || true
	for dir in $(NESTED_MODULE_DIRS); do VCR_MODE=replay go test -C $$dir ./... | grep -v -E "^ok|no test files|^\\?" || true; done

test-vcr-off:
	VCR_MODE=off go test ./...
	for dir in $(NESTED_MODULE_DIRS); do VCR_MODE=off go test -C $$dir ./...; done

test-vcr-record:
	VCR_MODE=record go test ./...
	for dir in $(NESTED_MODULE_DIRS); do VCR_MODE=record go test -C $$dir ./...; done

# Verify that VCR cassettes work without API keys
# This ensures VCR-enabled tests can run in CI/CD without credentials
test-vcr-verify:
	env -u BRAINTRUST_API_KEY VCR_MODE=replay go test ./...
	for dir in $(NESTED_MODULE_DIRS); do env -u BRAINTRUST_API_KEY VCR_MODE=replay go test -C $$dir ./...; done

cover:
	go test $$(go list ./... | grep -v /examples/) -coverpkg=./... -coverprofile=coverage.out
	for dir in $(NESTED_MODULE_DIRS); do go test -C $$dir -coverpkg=./... -coverprofile=coverage.out ./...; done
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint fmt -d
	golangci-lint run ./...

fmt:
	golangci-lint fmt

mod-verify:
	go mod tidy
	# Use GOWORK=off so tidy uses the replace directive instead of the workspace.
	# This preserves an explicit version pin in the nested go.mod (e.g. set by
	# release.sh before tagging) rather than resetting it to v0.0.0.
	for dir in $(NESTED_MODULE_DIRS); do GOWORK=off go mod tidy -C $$dir; done
	git diff --exit-code go.mod go.sum \
		$(foreach dir,$(NESTED_MODULE_DIRS),$(dir)/go.mod $(dir)/go.sum)
	./scripts/check_nested_modules.sh
	go mod verify
	for dir in $(NESTED_MODULE_DIRS); do (cd $$dir && go mod verify); done

check-nested-modules:
	./scripts/check_nested_modules.sh

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

release: ci
	./scripts/publish.sh

generate:
	go run ./internal/genorchestrion/cmd
