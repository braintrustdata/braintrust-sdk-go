# HTTP API client

Use the public [`openapi`](https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go/openapi) package for direct access to the Braintrust REST API. Its client, resource methods, and models are generated from the pinned OpenAPI specification; new resources do not need handwritten wrappers.

For evaluations, tracing, and prompt rendering, prefer the SDK's higher-level APIs. For REST access, import `github.com/braintrustdata/braintrust-sdk-go/openapi`.

## Basic usage

This program lists existing projects without modifying data:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "net/http"
    "os"
    "time"

    "github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func main() {
    key := os.Getenv("BRAINTRUST_API_KEY")
    if key == "" {
        log.Fatal("BRAINTRUST_API_KEY is required")
    }
    baseURL := os.Getenv("BRAINTRUST_API_URL")
    if baseURL == "" {
        baseURL = "https://api.braintrust.dev"
    }
    client, err := openapi.NewClientWithResponses(baseURL,
        openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
            req.Header.Set("Authorization", "Bearer "+key)
            return nil
        }),
    )
    if err != nil {
        log.Fatal(err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    limit := 5
    response, err := client.GetProjectWithResponse(ctx, &openapi.GetProjectParams{Limit: &limit})
    if err != nil {
        log.Fatal(err)
    }
    if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
        log.Fatalf("list projects: HTTP %d: %s", response.StatusCode(), response.Body)
    }
    for _, project := range response.JSON200.Objects {
        fmt.Printf("%s\t%s\n", project.Id, project.Name)
    }
}
```

The generated constructor does not load environment variables, add authentication, or create resources. The snippet configures the base URL and bearer header explicitly. No LLM-provider key is needed. Use `openapi.WithHTTPClient` to supply a custom `*http.Client`, for example with a timeout or transport.

## Responses and generated models

- Prefer `NewClientWithResponses` and methods ending in `WithResponse` for decoded responses. **An HTTP error is not necessarily a Go error.** Check `StatusCode()` and the expected decoded field (`JSON200` for the calls above), as well as `err`. `Body` contains the response bytes, including error details.
- The response-aware methods read and close the HTTP response body. If you instead use the raw methods from `NewClient`, close each returned response body yourself.
- Models use generated field names such as `Id` and `ProjectId`. UUID-valued model fields use `github.com/google/uuid.UUID`; path/query ID parameters accept strings.
- Optional fields use pointers or `nullable.Nullable[T]`, depending on the schema. For nullable fields, the zero value omits the field, `nullable.NewNullableWithValue(value)` supplies a value, and `nullable.NewNullNullable[T]()` explicitly sends JSON null. These states matter for PATCH requests.
- Union fields have generated `From...` and `As...` helpers. The automation lifecycle below constructs a logs configuration and webhook action using these helpers, without handwritten automation models.
- List/fetch operations return one page. Pass the generated pagination parameters or fetch-body cursor to request subsequent pages; the client does not auto-paginate.

The generated surface includes the 25 REST resource groups selected in [`openapi/oapi-codegen.yaml`](../openapi/oapi-codegen.yaml), including projects, experiments, datasets, prompts, functions, project automations, and function invocation. For a non-streaming invocation, use `PostFunctionIdInvokeWithResponse`; use the raw operation and consume the response body yourself for streaming. Specialized log ingestion, proxy, and remote-eval launch endpoints are outside this generation scope; use the corresponding higher-level SDK APIs.

## Runnable example

From the repository root:

```bash
export BRAINTRUST_API_KEY="<your-api-key>"
go run ./examples/api-client
# Select an existing project explicitly:
go run ./examples/api-client -project-id "<project-id>"
```

The [example](../examples/api-client/main.go) uses the generated client directly and is **read-only by default**. It gets the selected project (or lists and selects the first accessible project), then lists up to five experiments, prompts, and datasets in it. An account with no accessible projects exits without creating one. The default path needs only read access and prints resource IDs and names, not prompt bodies or dataset rows.

Two full CRUD demonstrations are defined but their calls are commented out in `run`:

- **`DatasetLifecycle`** creates a uniquely named temporary dataset, inserts and fetches a row, updates the same row with a merge, fetches it again, then deletes the row and dataset. It requires write/delete permissions and prints its sample row contents. It demonstrates row updates; generated dataset metadata PATCH methods are also available separately.
- **`AutomationLifecycle`** creates a uniquely named logs automation, reads it, updates its description and factuality threshold from `0.5` to `0.25`, reads it again, and deletes it. It requires permission to manage project automations. Both configurations are **paused**, so the sample webhook (`https://example.com/braintrust-hook`) is not sent.

Uncomment either call only in a project where creating and deleting test resources is acceptable. Each helper defers cleanup using an uncanceled context with a separate timeout, and reports cleanup failures rather than hiding them. If cleanup fails or the process is forcibly stopped, delete the printed resource ID manually.

## Compatibility with the older client

The handwritten `api` package and `braintrust.Client.API()` are deprecated but remain available for backward compatibility. Their Go doc comments include standard `Deprecated:` paragraphs pointing to generated-client replacements; no existing signatures or behavior have been removed. They will not gain new resource wrappers. SDK evaluation, hosted-function execution, dataset loading, prompt loading, and remote-eval setup use generated operations directly.

The old `eval.NewEvaluator` and `prompt.Load` signatures are also deprecated compatibility entry points for callers holding an `*api.API`. They forward to the generated-client implementations through `API.GeneratedClient()`. New low-level callers can use `eval.NewEvaluatorWithClient` and `prompt.LoadWithClient`; most applications should continue to use `braintrust.NewEvaluator` and `Client.LoadPrompt`, which are not deprecated.

The SDK's `internal/https` package contains both the shared HTTP transport and the adapter that configures generated clients with existing authentication, logging, HTTP-error handling, and query encoding. The public `openapi` package remains separate and generated. The SDK uses raw generated operations when it needs to decode directly into application types without losing JSON integer precision. Login, telemetry export, and attachment upload retain their dedicated transports where there is no corresponding generated operation.

## OpenAPI source and generation

The specification revision and generator version are pinned in [`openapi/config.json`](../openapi/config.json). Generated source is committed as one file per API tag in [`openapi/`](../openapi/) (`*.gen.go`). See [Contributing: OpenAPI client generation](../CONTRIBUTING.md#openapi-client-generation) for fetching a new revision, regenerating the client, and checking for drift. Normal application builds do not run generation or fetch the specification.
