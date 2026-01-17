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
- Run a single example: `go run examples/internal/<name>/main.go`
- Run all examples: `make examples`

## Dev Workflow (TDD)

Follow Test-Driven Development for all changes:

1. **Write a failing test first**
2. **Implement minimal code** to make the test pass
3. **Run the cycle after every change:**
   ```bash
   make test    # Run tests (VCR replay mode)
   make lint    # Check for lint errors
   ```
4. **Fix any issues** before continuing
5. **Refactor** if needed (tests should still pass)
6. **Repeat** for each new feature or requirement

Before committing:
```bash
make ci      # Full CI: lint + test + build
```

When adding tests that need API calls:
```bash
VCR_MODE=record go test -v -run=TestName ./path/to/package
```
