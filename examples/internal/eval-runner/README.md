# Remote Eval Runner Example

*This feature is currently in Public Preview: its use or behavior may change in future versions.*

This example runs Braintrust evals **under the `bt` CLI**, so the Braintrust playground can
trigger evaluations against code running on your own machine. Outputs stream back to the UI
and results are recorded as an experiment.

## How this works

`bt` provides an HTTP server that dispatches eval requests to the Go program. For each request it spawns a subprocess of your Go binary, passes the request in environment variables, and reads
results back over a unix socket. The process runs one eval and exits.

```
browser ──▶ bt eval --dev (localhost:8300) ──spawns──▶ this program
                                           ◀──socket── SSE frames
```

## What This Example Shows

- Registering evaluators with `evalrunner.RegisterEval` and handing over with `evalrunner.Main`
- A `food-classifier` (`string → string`) task with two scorers (`exact_match`, `valid_category`)
  and a `model` parameter the task actually reads
- A `summarizer` task driven by a **prompt parameter**: the prompt picked in the playground is
  rendered in Go and sent to OpenAI, so editing the prompt in the UI changes what the model is
  asked without touching the code
- Driving both from a Braintrust **Playground**, changing parameters to alter behavior, and
  watching results stream in

## Layout

```
main.go                     wiring: New -> RegisterEval -> Main
food_classifier_eval.go     an eval with no LLM call: task, scorers, parameters, dataset
summarizer_eval.go          an eval driven by a prompt parameter, calling OpenAI
```

The `_eval.go` suffix is how `bt` discovers Go evals in a directory, mirroring Go's own `_test.go`
convention. Splitting the eval out of `main.go` is also just good practice once you have more than
one.

## Prerequisites

1. **The `bt` CLI.** Install it with `brew install braintrustdata/tap/bt`, or see the
   [CLI docs](https://www.braintrust.dev/docs/reference/cli).
2. **Braintrust API key**: `BRAINTRUST_API_KEY` must be set. If you have it in the repo's `.env`,
   mise loads it automatically; otherwise `export` it.
3. **OpenAI API key**: `OPENAI_API_KEY`, for the `summarizer` eval. The `food-classifier` eval
   needs no LLM.
4. The Braintrust project you'll run from. The example registers under `go-sdk-examples`, but
   playground runs attach to *your* playground's project, so any project works.

## Running It

### From the Braintrust playground

Start the dev server from the `examples` module, pointing `bt` at this **directory** (Go compiles a
directory, not a file):

```bash
cd examples
bt eval --dev ./internal/eval-runner
```

`bt` infers Go from `food_classifier_eval.go` — it discovers Go evals by the `*_eval.go` suffix, the
same convention Go itself uses for `_test.go`. If your evals live in a package with no such file,
say `--language go` explicitly.

Then:

1. **Register the source**: in your project, go to **Settings → Remote evals → Create remote eval
   source**. Give it a name and set the URL to `http://localhost:8300`.
2. **Add the task**: open a **Playground**, choose **+ Task → Remote eval**, and pick
   `food-classifier`. The `model` control (from the eval's parameter schema) appears here.
   It isn't cosmetic — the task reads it: leave it at `rule-based` (lenient substring matching,
   so `"A crisp red apple"` → `fruit`) or set it to `strict` (only an exact single word matches,
   so the descriptive rows fall through to `unknown`). Try both and watch `exact_match` move.
3. **Attach a dataset**: the playground supplies the cases (the eval's own dataset is not used).
   Click **Select a dataset → Create new dataset**, then add rows manually or upload a JSON/CSV
   file. Each row needs an `input` and an `expected` field:

   ```json
   [
     {"input": "A crisp red apple",      "expected": "fruit"},
     {"input": "Fresh banana",           "expected": "fruit"},
     {"input": "Crunchy carrot sticks",  "expected": "vegetable"},
     {"input": "Romaine lettuce",        "expected": "vegetable"},
     {"input": "Grilled chicken breast", "expected": "protein"}
   ]
   ```

   > The playground has no free-form "paste inline" box — data comes from a dataset. Don't paste
   > the JSON into the `model` parameter field: that's a task parameter, not the dataset.
4. **Run.**

#### Trying the prompt parameter

Pick `summarizer` instead of `food-classifier` at step 2. Its `summary_prompt` control is a prompt
picker: leave it on the default declared in `main.go`, or choose a prompt saved in your project.
Either way the task calls `hooks.Parameters.Prompt("summary_prompt")`, renders it with the case
input, and sends it to OpenAI. Each case's dataset row needs only an `input` — the scorers here
(`non_empty`, `one_sentence`) do not compare against an expected value.

Because the built prompt is annotated onto the task span, the trace links back to the prompt that
produced it.

Edit the Go code and run again — `bt` recompiles on each request, so there is no server to restart.

### From the command line

`bt` can also run the eval without a playground. It uses the `Dataset` defined on the eval itself:

```bash
cd examples
bt eval ./internal/eval-runner
```

### Without `bt` at all

Running the binary directly prints what is registered and exits, without contacting Braintrust —
useful for checking that your evals are wired up:

```bash
cd examples
go run ./internal/eval-runner
```
```
Registered evals:
  food-classifier  (scorers: exact_match, valid_category; parameters: model)
  summarizer  (scorers: non_empty, one_sentence; parameters: summary_prompt)

Run them with: bt eval <this package directory>
```

## What to Look For

- **Terminal**: `bt` prints the requests it handles and forwards anything your code logs to
  stderr.
- **Braintrust UI**: outputs stream in row by row; the `exact_match` and `valid_category` columns
  populate; the run links to an experiment/trace you can open.

The last dataset row is a deliberate miss: the task returns `unknown` for "Grilled chicken breast",
so it scores `exact_match = 0` but `valid_category = 1` (`unknown` is a valid category) — showing
what a non-perfect run looks like.

## Note on `bt` support for Go

Go support in `bt` (`--language go`) ships alongside this SDK change. If your `bt` predates it,
you can drive the runner through the Python arm, which passes `--runner` through unvalidated:

```bash
cd examples
go build -o /tmp/foodevals ./internal/eval-runner
touch /tmp/dummy.eval.py
bt eval --dev --language python --runner /tmp/foodevals /tmp/dummy.eval.py
```

This works because the runner ignores `os.Args` entirely and takes all its input from the
environment.
