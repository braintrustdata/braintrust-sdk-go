# Publishing

This repo releases the root SDK module plus every releasable nested module listed in
[`scripts/nested_modules.txt`](../scripts/nested_modules.txt).

Tag formats:
- Root SDK: `vX.Y.Z`
- Nested modules: `<module-path>/vX.Y.Z`

Example nested tags:
- `trace/contrib/all/vX.Y.Z`
- `trace/contrib/openai/vX.Y.Z`
- `trace/contrib/adk/vX.Y.Z`

## Release process

Releases are fully automated via GitHub Actions. The process has three stages:

### 1. Prepare (manual trigger)

Go to **Actions → Prepare Release → Run workflow** and enter the version (e.g. `v1.2.3`).

The workflow:
- Creates a `release/vX.Y.Z` branch
- Pins the root module version and any nested-module interdependencies in each nested module's `go.mod` using `go mod edit` + `GOWORK=off go mod tidy`
- Opens a pull request titled `chore: release vX.Y.Z`

> **Why pin the version?** During development, `go mod tidy` runs in Go workspace mode and uses `v0.0.0` as a placeholder version for workspace-local modules. The published `go.mod` must reference a real version so downstream users can resolve the dependency from the Go module proxy.

### 2. Review and merge

Review the PR (it should only contain nested `go.mod` / `go.sum` version-pin updates) and merge it through the normal protected-branch process.

### 3. Tag and publish (automatic)

Merging the PR triggers two sequential workflows:

**Tag Release** — runs `make ci` on the merge commit as a final check, then creates and pushes:
- `vX.Y.Z` — the root module tag
- one nested tag for every module in [`scripts/nested_modules.txt`](../scripts/nested_modules.txt), for example `trace/contrib/all/vX.Y.Z`
  Nested tags are created and pushed in dependency order so nested modules are processed after any other nested modules they require. The root tag is pushed last so the publish workflow starts only after every nested module tag already exists on the remote.

**Release** — triggered by the `vX.Y.Z` tag push, runs:
- `goreleaser` to create the GitHub release with a changelog
- Go proxy indexing for the root module and every nested module

## Adding a new nested module

If a new releasable Go module is added under `trace/contrib/`:

1. Add it to [`scripts/nested_modules.txt`](../scripts/nested_modules.txt) (one repo-relative path per line).
2. Add a `replace` directive pointing to the repo root so local development works without the module being published:
   ```
   replace github.com/braintrustdata/braintrust-sdk-go => ../../..
   ```
3. The `check-nested-modules` Make target (also run as part of `mod-verify`) will fail if the manifest is out of sync with the actual `go.mod` files under `trace/contrib/`.

## Re-running a failed publish

If the **Release** workflow fails after tags are already pushed, re-run it manually:

Go to **Actions → Release → Run workflow** and enter the tag (e.g. `v1.2.3`).

This re-runs only `publish.sh` (goreleaser + proxy indexing) without re-tagging.

If tag creation fails before the root tag is pushed, fix the issue and re-run `./scripts/release.sh <version>`. The script is resumable: it reuses any local or remote release tags that already point at the intended release commit and pushes only the missing ones.
