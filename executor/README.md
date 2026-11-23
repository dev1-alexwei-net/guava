# Executor

The `executor` package runs an OpenClaw Responses conversation for each regular file in a fileexplorer SQLite index. The `cmd/executor` program provides a CLI for running the package.

## Inputs

Create an `executor.Config` with:

- `DatabasePath`: an existing fileexplorer SQLite database. The executor opens it read-only and reads regular-file rows from the existing `files` table.
- `RootPath`: the filesystem root corresponding to the relative paths in that database. The database stores metadata, not file contents.
- `InstructionsPath`: the fallback path to an `instructions.md` file. A closer indexed `instructions.md` for each target file takes precedence.
- `GatewayURL`: the OpenClaw Gateway base URL, without `/v1/responses`.
- `AgentID`: the OpenClaw agent to route requests to.
- `AuthToken`: optional bearer token. Leave it empty when authentication is provided by a trusted proxy or private no-auth ingress.
- `HTTPClient`: optional HTTP client override; the package uses `http.DefaultClient` when omitted.
- `Logger`: optional structured logger. A nil logger disables package logging.

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
  -agent-id main \
  -log-level info
```

The `-database` and `-root` flags are required. `-instructions` defaults to `instructions.md` and is used only when the index has no applicable ancestor instruction file. `-gateway-url` defaults to `http://127.0.0.1:18789`, `-agent-id` defaults to `main`, and `-log-level` defaults to `info`. Supported log levels are `error`, `warn`, `info`, `debug`, and `trace`. TLS certificate and hostname verification is skipped by default; pass `-verify-tls` to enable standard verification. For shared-secret Gateway authentication, set `OPENCLAW_AUTH_TOKEN` in the command's environment; the CLI sends it as a bearer token and does not put it in the argument list. Omit it when authentication is handled by a trusted proxy or private no-auth ingress.

To build a standalone binary, run `go build -o ./guava-executor ./cmd/executor`, then invoke `./guava-executor` with the same flags. The CLI exits nonzero on configuration or processing errors. SIGINT and SIGTERM cancel the active run.

## Logging

The CLI writes structured text logs to stderr. At `info` level it reports run and per-file progress, plus each Gateway response status, response byte count, fetch time, response-body read time, and total elapsed time. `debug` adds indexed-file and instructions-file read timings and conversation-turn details. `trace` includes the raw Gateway response body; use it only when that full content is wanted in the log. The bearer token is never logged. `warn` and `error` select those severities and more severe events.

Library callers can provide a `slog.Logger` through `Config.Logger`; if omitted, the executor emits no package logs.

## Standalone dashboard

Run the separate server-rendered dashboard with:

```sh
go run ./cmd/executor-dashboard -database ./fileexplorer.sqlite -root ./files -instructions ./instructions.md -gateway-url http://127.0.0.1:18789 -agent-id main -log-level info
```

Open `http://127.0.0.1:8081/`. The dashboard listens on loopback at `127.0.0.1:8081` by default. The `-database` and `-root` flags are required. The dashboard starts one executor run at a time, shows its status and the most recent 64 KiB of logs in a text area, and cancels an active run when the dashboard shuts down. Its optional full-page refresh is off by default, and `-log-level` selects the same levels as the executor CLI. It is a separate command and does not add routes to `fileexplorer`. Set `OPENCLAW_AUTH_TOKEN` when Gateway authentication is required; pass `-verify-tls` to enable certificate and hostname verification.

## Processing behavior

- The package reads only rows where `is_dir = 0`, ordered by the slash-separated `path` using SQLite binary ordering.
- It reads each current file from `RootPath` through Go's rooted filesystem API. Paths must be relative and contained by the root; directories and paths that escape the root are rejected.
- For each target file, the executor looks for an indexed `instructions.md` in the file's directory and then each parent directory up to the root. The nearest match wins; instructions are not combined across ancestors. The file is read through `RootPath`. If none is indexed, the configured `InstructionsPath` fallback is used. Indexed files named `instructions.md` are treated as instruction sources and are not processed as target files. The initial input also includes the indexed target path and its current contents.
- Each file starts a separate conversation. If assistant output text does not contain the exact marker `<complete />`, the package sends `Continue` with the previous `previous_response_id`. It does not start the next file until the marker appears.
- Database rows and source files are not modified.
- `Run` accepts a `context.Context`; cancellation and database, file, and HTTP errors stop the run and are returned to the caller.

## OpenClaw Gateway

The package sends non-streaming JSON requests to `POST {GatewayURL}/v1/responses`, with `model: "openclaw"` and the `x-openclaw-agent-id` header. The OpenClaw Responses endpoint is disabled by default; enable `gateway.http.endpoints.responses.enabled` on the Gateway.

See the [OpenClaw OpenResponses HTTP API](https://docs.openclaw.ai/gateway/openresponses-http-api) for Gateway authentication and endpoint configuration.

## Tests

The package tests use local SQLite fixtures and mock HTTP servers. They do not contact a live OpenClaw Gateway.
