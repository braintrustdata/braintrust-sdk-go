# Contributing to Braintrust Go SDK

## Development Setup

This project uses [mise](https://mise.jdx.dev/) to manage dependencies, hooks and configure the dev environment. If you want to do it manually,
install the tools listed in `mise.toml`.


First, [install mise](https://mise.jdx.dev/installing-mise.html) and then [activate it in your shell](https://mise.jdx.dev/getting-started.html#activate-mise). Now we
can start.

```bash
# Clone the repo.
git clone git@github.com:braintrustdata/braintrust-sdk-go.git
cd braintrust-sdk-go

# Install our dependencies.
mise trust
mise install

# Setup your env variables
cp env.example .env
```

## Build, Test and Run

All of the common dev tasks are in our `Makefile`.

```bash
make help
```


Constructor tests that depend on the configured API URL must control background
login: `auth.TestAPIKey` asynchronously supplies a valid server URL and can race
with exporter initialization. Use a local login endpoint with a terminal response
when testing URL errors, and wait for the request before shutting down the server.

### Re-recording VCR cassettes

With credentials exported in your shell, run:

```bash
./scripts/re-record-vcr-cassettes.sh
```

The helper checks that `BRAINTRUST_API_KEY`, `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, `GOOGLE_API_KEY`, `GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`,
and `AWS_SECRET_ACCESS_KEY` are present. Temporary AWS credentials also need
`AWS_SESSION_TOKEN`. It does not source credential files or validate provider
permissions in advance. Use test accounts: recording makes billable API calls
and creates/deletes test resources.

It records the root SDK, workflow-eval example, every integration in the nested
module manifest, and `btx`, then replays the same suites with provider credentials
unset. Both phases disable the Go test cache; recording also runs packages and
parallel tests serially. Any failure stops the script. A failed run can leave
already-updated cassettes in the working tree; there is no automatic rollback.

Existing cassettes are not erased: tests that explicitly force replay continue
to use their historical recordings. Review cassette diffs for sensitive response
data before committing. Run the script with `--help` for usage from any directory.

Recordable tests should own their fixtures and use IDs returned by the API.
Do not hardcode resource IDs from another test's cassette: a re-record can
replace those resources. Compatibility tests should set up fixtures with the
generated client and use the legacy client only for the compatibility behavior
under test.

## OpenAPI client generation

The public REST client and models in `openapi/*.gen.go` are generated with
`oapi-codegen`, then split into one file per API tag (shared models in
`models.gen.go`, client plumbing in `client.gen.go`). Applications and examples use the `openapi` package directly;
new REST resources must not require handwritten wrappers. See the
[HTTP API client guide](docs/api-client.md) for authentication, generated models,
and response handling.

The older `api` package and `Client.API()` are deprecated but remain for backward compatibility.
Do not expand this compatibility facade. Normal SDK paths use generated clients
directly; only explicit compatibility entry points accept the older client.
`api.API.GeneratedClient()` bridges those entry points without adding resource
wrappers. High-level hosted-function tracing and output handling stay in `eval`,
above the generated invocation operation.

Run from the repository root:

```bash
make fetch-openapi-spec       # Fetch and verify the pinned upstream snapshot
make generate-api-client     # Generate from the committed snapshot
make check-api-client-codegen # Regenerate temporarily and fail on drift
go run ./examples/internal/openapi # Build and print a request without sending it
```

To exercise the public client against a real deployment without modifying data:

```bash
# With BRAINTRUST_API_KEY set; BRAINTRUST_API_URL is optional.
go run ./examples/internal/openapi -live
```

The live mode lists one project and fetches it by ID. The default mode only
constructs the displayed request; it does not send the POST.

`openapi/config.json` pins the `braintrustdata/braintrust-openapi` commit, snapshot
SHA-256, and generator release. To update the schema, change the commit and digest,
fetch, regenerate, and review both schema and generated-code changes. To fetch
from a local checkout instead, set `BRAINTRUST_OPENAPI_ROOT`; its `openapi/spec.json`
must match the same digest. Failed fetches leave the previous snapshot intact.

The source snapshot and generated Go file are committed. Generation and drift
checks never fetch a new schema; a cold invocation can download the pinned Go
generator modules. Normal builds need neither the generator nor schema network
access. `make generate` continues to generate orchestrion configuration.

`openapi/oapi-codegen.yaml` selects 25 REST resource tags and includes function
invocation. Specialized cross-object/log ingestion, remote eval launch, and
proxy paths stay outside these bindings.
Generated clients accept authentication via request editors and custom HTTP
clients via `WithHTTPClient`. The response-aware methods return HTTP error
responses without necessarily returning a Go error; callers must check the
status code and expected decoded body as well as `err`.

The SDK configures generated clients through `internal/https.NewGenerated`.
The same internal package owns the generated-client adapter and shared transport
for authentication, logging, and `HTTPError` handling. Runtime code calls
generated operations, not the compatibility facade.
Raw operations allow direct decoding into SDK/application types without lossy
conversions through generated `any` fields. Preserve omission rules, prompt
version-over-environment precedence, supplemental filters missing from the spec,
and canonical query encoding. Login and telemetry/attachment transports without
matching generated endpoints remain separate.

`internal/genapi/normalize.go` adapts the pinned schema only in temporary input:
it extracts the score-type parameter's union/list/item schemas to avoid generated
name collisions, preserves nullable union and enum semantics, and expresses
unconstrained nullable additional properties as `true` to avoid mismatched map
helper types. Scalar UUID parameter schemas use Go strings so IDs and cursors
retain their original spelling and server-side validation; response-model UUIDs
remain unchanged. The upstream snapshot is never rewritten, and generated Go
must not be patched by hand. Schema-shape changes that invalidate the extraction
fail explicitly and require review.

CI checks regeneration drift and runs the offline example. REST and higher-level
SDK behavioral tests use generated clients and real recorded interactions.
Keep only a small compatibility suite for the older public facade and constructor
bridges; do not add a duplicate handwritten endpoint test matrix. Coverage includes
invocation, nullable fields, scalar/list unions, auth/errors, deployment URL
prefixes, arbitrary JSON precision, and prompt selection precedence.
