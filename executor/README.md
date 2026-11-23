# Executor

The `executor` package runs an OpenClaw Responses conversation for each regular file in a fileexplorer SQLite index. The `cmd/executor` program provides a CLI for running the package.

## Inputs

Create an `executor.Config` with:

- `DatabasePath`: an existing fileexplorer SQLite database. The executor opens it read-only and reads regular-file rows from the existing `files` table.
- `RootPath`: the filesystem root corresponding to the relative paths in that database. The database stores metadata, not file contents.
- `InstructionsPath`: the fallback path to an `instructions.md` file. A closer `instructions.md` in the target file’s filesystem ancestry takes precedence, whether or not it is indexed.
- `GatewayURL`: the OpenClaw Gateway base URL, without `/v1/responses`.
- `AgentID`: the OpenClaw agent to route requests to.
- `AuthToken`: optional bearer token. Leave it empty when authentication is provided by a trusted proxy or private no-auth ingress.
- `HTTPClient`: optional HTTP client override; the package uses `http.DefaultClient` when omitted.

## Library example

```go
package main

import (
	"context"
	"log"

	"github.com/dev1-alexwei-net/guava/executor"
)

func main() {
	runner, err := executor.New(executor.Config{
		DatabasePath:     "/var/lib/fileexplorer.sqlite",
		RootPath:         "/srv/files",
		InstructionsPath: "./instructions.md",
		GatewayURL:       "http://127.0.0.1:18789",
		AgentID:          "main",
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := runner.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

## Run from the command line

From the repository root, run:

```sh
go run ./cmd/executor \
  -database ./fileexplorer.sqlite \
  -root ./files \
  -instructions ./instructions.md \
  -gateway-url http://127.0.0.1:18789 \
  -agent-id main
```

The `-database` and `-root` flags are required. `-instructions` defaults to `instructions.md` and is used only when no ancestor instruction file exists on disk. `-gateway-url` defaults to `http://127.0.0.1:18789`, and `-agent-id` defaults to `main`. TLS certificate and hostname verification is skipped by default; pass `-verify-tls` to enable standard verification. For shared-secret Gateway authentication, set `OPENCLAW_AUTH_TOKEN` in the command's environment; the CLI sends it as a bearer token and does not put it in the argument list. Omit it when authentication is handled by a trusted proxy or private no-auth ingress.

To build a standalone binary, run `go build -o ./guava-executor ./cmd/executor`, then invoke `./guava-executor` with the same flags. The CLI exits nonzero on configuration or processing errors. SIGINT and SIGTERM cancel the active run.

## Processing behavior

- The package reads only rows where `is_dir = 0`, ordered by the slash-separated `path` using SQLite binary ordering.
- It reads each current file from `RootPath` through Go's rooted filesystem API. Paths must be relative and contained by the root; directories and paths that escape the root are rejected.
- For each target file, the executor searches its containing directory and every filesystem parent through the filesystem root for `instructions.md`; the nearest regular file wins, and instructions are not combined across ancestors. The instruction file does not need to be indexed and may be above `RootPath`. If no ancestor instruction exists on disk, the configured `InstructionsPath` fallback is used. Indexed files named `instructions.md` are treated as instruction sources and are not processed as target files. The initial input also includes the indexed target path and its current contents.
- Each file starts a separate conversation. If assistant output text does not contain the exact marker `<complete />`, the package sends `Continue` with the previous `previous_response_id`. It does not start the next file until the marker appears.
- Database rows and source files are not modified.
- `Run` accepts a `context.Context`; cancellation and database, file, and HTTP errors stop the run and are returned to the caller.

## OpenClaw Gateway

The package sends non-streaming JSON requests to `POST {GatewayURL}/v1/responses`, with `model: "openclaw"` and the `x-openclaw-agent-id` header. The OpenClaw Responses endpoint is disabled by default; enable `gateway.http.endpoints.responses.enabled` on the Gateway.

See the [OpenClaw OpenResponses HTTP API](https://docs.openclaw.ai/gateway/openresponses-http-api) for Gateway authentication and endpoint configuration.

## Tests

The package tests use local SQLite fixtures and mock HTTP servers. They do not contact a live OpenClaw Gateway.
