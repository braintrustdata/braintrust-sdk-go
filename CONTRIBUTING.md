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