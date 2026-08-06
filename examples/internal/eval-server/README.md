# Remote Eval Server Example

This example runs a **remote eval server**: it exposes locally-registered evaluators over
HTTP so the Braintrust UI can trigger evaluations against code running on your machine.
Results (outputs + scores) stream back to the UI and are recorded as an experiment.

## What This Example Shows

- Starting a remote eval server with `server.New(...)` and `srv.Start()`
- Registering an evaluator (`food-classifier`, a `string → string` task) with two scorers
  (`exact_match`, `valid_category`) and a UI-visible `model` parameter
- Driving that evaluator from a Braintrust **Playground** and viewing streamed results

The task is intentionally rule-based (simple string matching), so the example needs **no
LLM API key** — the focus is the remote-eval wiring, not the model.

## Prerequisites

1. **Braintrust API key**: `BRAINTRUST_API_KEY` must be set. If you have it in the repo's
   `.env`, mise loads it automatically; otherwise `export` it.
2. The Braintrust project you'll run from. The example registers under `go-sdk-examples`,
   but Playground runs attach to *your* playground's project, so any project works. (To run
   it standalone against a specific project, change `ProjectName` in `main.go`.)

## Running the Example

From the `examples` module:

```bash
cd examples
go run ./internal/eval-server
```

The server listens on `http://localhost:8300` and prints its endpoints:

```
Eval server starting on localhost:8300
Registered evaluators: food-classifier
Health check: http://localhost:8300/
List evals:   http://localhost:8300/list
```

Sanity-check it directly:

```bash
curl -s localhost:8300/        # {"status":"ok"}
curl -s localhost:8300/list    # food-classifier, its scorers, and the model parameter
```

Keep the process running while you use the remote eval. Stop it with `Ctrl-C`.

## Running From the Braintrust UI

The browser calls `localhost:8300` directly (the server sends CORS headers), so **no tunnel
is needed** as long as your browser is on the same machine as the server.

1. **Register the source**: in your project, go to **Settings → Remote evals →
   Create remote eval source**. Give it a name and set the URL to `http://localhost:8300`.
2. **Add the task**: open a **Playground**, choose **+ Task → Remote eval**, and pick
   `food-classifier`. The `model` control (from the eval's parameter schema) appears here.
3. **Attach a dataset**: the playground supplies the cases (the eval's own dataset is not
   used). Click **Select a dataset → Create new dataset**, then add rows manually or upload
   a JSON/CSV file. Each row needs an `input` and an `expected` field:

   ```json
   [
     {"input": "A crisp red apple",      "expected": "fruit"},
     {"input": "Fresh banana",           "expected": "fruit"},
     {"input": "Crunchy carrot sticks",  "expected": "vegetable"},
     {"input": "Romaine lettuce",        "expected": "vegetable"},
     {"input": "Grilled chicken breast", "expected": "protein"}
   ]
   ```

   > The playground has no free-form "paste inline" box — data comes from a dataset, and with
   > none attached it runs a single empty case. Don't paste the JSON into the `model`
   > parameter field: that's a task parameter, not the dataset.
4. **Run**.

## What to Look For

- **Go console**: after `eval server listening`, you'll see the server handle the incoming
  `/eval` request while the playground run is in flight.
- **Braintrust UI**: outputs stream in row by row; the `exact_match` and `valid_category`
  columns populate; the run links to an experiment/trace you can open.

The last row is a deliberate miss: the task returns `unknown` for "Grilled chicken breast",
so it scores `exact_match = 0` but `valid_category = 1` (`unknown` is a valid category) —
showing what a non-perfect run looks like.
</content>
