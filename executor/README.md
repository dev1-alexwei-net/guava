# Executor

The `executor` package runs an OpenClaw Responses conversation for each regular file in a fileexplorer SQLite index. It is a Go library; it does not provide a command-line executable.

## Inputs

Create an `executor.Config` with:

- `DatabasePath`: an existing fileexplorer SQLite database. The executor opens it read-only and reads regular-file rows from the existing `files` table.
- `RootPath`: the filesystem root corresponding to the relative paths in that database. The database stores metadata, not file contents.
- `InstructionsPath`: the path to the `instructions.md` file.
- `GatewayURL`: the OpenClaw Gateway base URL, without `/v1/responses`.
- `AgentID`: the OpenClaw agent to route requests to.
- `AuthToken`: optional bearer token. Leave it empty when authentication is provided by a trusted proxy or private no-auth ingress.
- `HTTPClient`: optional HTTP client override; the package uses `http.DefaultClient` when omitted.

## Example

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

Set `AuthToken` in the config when the Gateway uses shared-secret authentication. The executor sends it as `Authorization: Bearer <token>`; it does not read environment variables itself.

## Processing behavior

- The package reads only rows where `is_dir = 0`, ordered by the slash-separated `path` using SQLite binary ordering.
- It reads each current file from `RootPath` through Go's rooted filesystem API. Paths must be relative and contained by the root; directories and paths that escape the root are rejected.
- For each file, the initial input combines the contents of `InstructionsPath`, the indexed path, and the file's current contents.
- Each file starts a separate conversation. If assistant output text does not contain the exact marker `<complete />`, the package sends `Continue` with the previous `previous_response_id`. It does not start the next file until the marker appears.
- Database rows and source files are not modified.
- `Run` accepts a `context.Context`; cancellation and database, file, and HTTP errors stop the run and are returned to the caller.

## OpenClaw Gateway

The package sends non-streaming JSON requests to `POST {GatewayURL}/v1/responses`, with `model: "openclaw"` and the `x-openclaw-agent-id` header. The OpenClaw Responses endpoint is disabled by default; enable `gateway.http.endpoints.responses.enabled` on the Gateway.

See the [OpenClaw OpenResponses HTTP API](https://docs.openclaw.ai/gateway/openresponses-http-api) for Gateway authentication and endpoint configuration.

## Tests

The package tests use local SQLite fixtures and mock HTTP servers. They do not contact a live OpenClaw Gateway.
