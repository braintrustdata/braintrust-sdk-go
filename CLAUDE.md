# CLAUDE.md

## Commands
- `make ci`: Lint, test, and build (run before committing)
- `make test`: Run tests with VCR replay
- `make lint`: Run golangci-lint
- `make fmt`: Format code

## Testing
- Follow TDD: write failing test first, then implement
- Single test: `go test -v -run=TestName ./path/to/package`
- Record single cassette: `VCR_MODE=record go test -v -run=TestName ./path/to/package`
- VCR modes:
  - `VCR_MODE=replay` (default): Use recorded responses
  - `VCR_MODE=record`: Record new cassettes (needs API keys)
  - `VCR_MODE=off`: Hit live APIs (needs API keys)

## Examples
- New features should be covered in `examples/internal/` for validation
- Run all examples: `make examples`
