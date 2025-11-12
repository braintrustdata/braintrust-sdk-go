# Temporal + Braintrust Distributed Tracing Example

This example demonstrates distributed tracing between Braintrust evals and Temporal workflows.

## What This Example Shows

- A minimal Temporal workflow (`LoggerWorkflow`) that processes numbers
- An activity (`ProcessNumberActivity`) that logs its input
- Distributed tracing from eval execution through Temporal workflows and activities
- OpenTelemetry context propagation between processes

## Prerequisites

1. **Mise**: Install from https://mise.jdx.dev/
2. **Braintrust API Key**: Set `BRAINTRUST_API_KEY` environment variable

## Setup

```bash
# Install dependencies (temporal CLI, overmind, etc.)
mise install
```

## Running the Example

### Option 1: Using Mise (Recommended)

```bash
# Terminal 1: Start Temporal server + worker
mise run server

# Terminal 2: Run eval (once server is ready)
mise run workflow
```

### Option 2: Manual

```bash
# Terminal 1: Start Temporal server
temporal server start-dev

# Terminal 2: Start the worker
go run cmd/worker/main.go

# Terminal 3: Run eval
go run cmd/client/main.go
```

## How It Works

1. **Eval Client** (`go run cmd/client/main.go`):
   - Initializes Braintrust with `braintrust.New()`
   - Creates Temporal client with OpenTelemetry interceptor
   - Executes workflows with different numeric inputs
   - Each test case creates a root span with experiment context

2. **Worker** (`go run cmd/worker/main.go`):
   - Initializes Braintrust with `braintrust.New()`
   - Creates Temporal client with OpenTelemetry interceptor
   - Registers workflow and activities
   - Processes workflow tasks from the queue
   - Activity executions create child spans under the workflow

3. **Distributed Tracing**:
   - Temporal's OpenTelemetry interceptor automatically propagates trace context
   - All spans in a test case share the same trace ID: Eval Test Case → Workflow → Activity
   - Works seamlessly across remote workers and process boundaries
   - View complete trace trees in the Braintrust UI

## Project Structure

```
examples/temporal/
├── cmd/
│   ├── worker/main.go   # Worker executable
│   └── client/main.go   # Client/eval executable
├── shared.go            # Workflow, activity, and shared constants
├── shared_test.go       # Tests for workflow and activity
├── mise.toml            # Tool and task configuration
├── Procfile             # Process definitions for overmind
└── README.md            # This file
```

## Testing

```bash
# Run all tests
go test -v

# Run specific test
go test -v -run TestLoggerWorkflow
```

## Viewing Traces

1. Go to https://www.braintrust.dev
2. Navigate to your project
3. Find the experiment created by the eval
4. See the complete trace tree: eval → workflow → activity

## Notes

- The worker must be running before executing workflows
- Each workflow execution gets a unique ID based on the input
- Temporal server runs on `localhost:7233` by default
- The task queue is named `logger-task-queue`
