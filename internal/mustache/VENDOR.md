# Vendored: github.com/cbroglie/mustache

This directory is a vendored copy of [cbroglie/mustache](https://github.com/cbroglie/mustache),
a Mustache template renderer. It is vendored rather than added to `go.mod` so
the SDK's root module keeps its dependency-light footprint — the same choice the
Ruby SDK made (`sdk-ruby/lib/braintrust/vendor/mustache/`).

Braintrust prompts are Mustache templates, so rendering one locally requires a
Mustache implementation that matches what the playground (which uses
`mustache.js`) produces.

| | |
|---|---|
| Upstream | https://github.com/cbroglie/mustache |
| Revision | `6d0f72f0cd2740129a8017e77f87ebc2e8cd8a54` (2026-01-13) |
| License | MIT — see `LICENSE`, retained verbatim |

## What was copied

`mustache.go`, `partials.go`, `error.go`, `mustache_test.go` and `LICENSE`. The
upstream `cmd/` CLI (which is why upstream's `go.mod` requires cobra and
yaml.v2), the `spec/` git submodule, and the filesystem `tests/` fixtures were
not copied. Upstream's own test suite came along so a re-sync has a regression
check that does not depend on our code.

## Rendering is value-substitution only

Braintrust renders templates that arrive from the API, so this copy is trimmed
to do one thing: substitute the supplied values. Anything in the upstream engine
that reaches beyond that — reading partials from disk, dispatching to methods on
context values, or invoking context funcs as lambdas — has been removed, since
none of it is needed for prompt rendering and all of it widens what a template
can do. `capabilities_test.go` locks this in (including a check that no file here
imports `os`, `net`, `os/exec`). Treat those tests as load-bearing: if one fails
after a re-sync, re-apply the removal rather than relaxing the test.

## Local modifications

Each is marked in-place with a comment referring to this file.

1. **Package doc** on `mustache.go` pointing here.
2. **`ValueFunc`** — a new `Template.value` field, `Template.Value(fn)` setter,
   and `Template.renderValue` helper. `renderElement` routes both raw
   (`{{{var}}}`) and escaped (`{{var}}`) interpolation through it. Upstream
   always uses `fmt.Sprint`, which renders a map as `map[a:1]`; Braintrust
   prompts need JSON, matching the JavaScript SDK. Two positional
   `Template{...}` literals gained a trailing `nil` for the new field.
3. **Partials are rejected at parse time** (`parsePartial` returns
   `ErrPartialNotSupported`). Upstream resolved `{{>name}}` by reading a file
   from disk; prompt rendering never needs partials, so `ParseStringRaw` no
   longer builds a filesystem `FileProvider`, and `FileProvider`, `ParseFile*`
   and `RenderFile*` are removed. Rejecting rather than silently rendering
   nothing means a prompt that uses a partial is reported.
4. **Method dispatch removed** from `lookup`: a name resolves against fields
   and map keys only, never against methods on the value.
5. **Lambdas removed** from `renderSection`: a func in the context is not a
   truthy section value, and rendered output is never re-parsed as a template.
6. **No writes to stdout.** Upstream printed recovered panics with
   `fmt.Printf`; under `bt eval` stdout carries the eval manifest, so writing
   there corrupts the protocol.
7. Upstream tests covering the removed capabilities (lambdas, method receivers,
   filesystem partials) are deleted, and `TestFRender`/`TestPartial` were
   rewritten against `ParseString` and a rejection assertion.

Everything else is upstream. `braintrust_test.go` and `capabilities_test.go`
cover the modifications.

## Re-syncing

```bash
git clone https://github.com/cbroglie/mustache /tmp/mustache
diff -u /tmp/mustache/mustache.go internal/mustache/mustache.go
```

Reapply the modifications above, then run `go test ./internal/mustache/...`.
