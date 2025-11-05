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