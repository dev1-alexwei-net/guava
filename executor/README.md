# Executor

The `executor` package runs an OpenClaw Responses conversation for each regular file in a fileexplorer SQLite index. The `cmd/executor` program provides a CLI for running the package.

## Inputs

Create an `executor.Config` with:

- `DatabasePath`: an existing fileexplorer SQLite database. The executor opens it read-only and reads regular-file rows from the existing `files` table.
- `RootPath`: the filesystem root corresponding to the relative paths in that database. The database stores metadata, not file contents.
- `InstructionsPath`: the fallback path to an `instructions.md` file. A closer indexed `instructions.md` for each target file takes precedence.
- `GatewayURL`: the OpenClaw Gateway base URL, without `/v1/responses`.
- `FileExplorerURL`: the fileexplorer base URL used to refresh the file index, without the internal refresh path. Use a hostname reachable from wherever the executor runs, such as the container name on a container network.
- `AgentID`: the OpenClaw agent to route requests to.
- `AuthToken`: optional bearer token. Leave it empty when authentication is provided by a trusted proxy or private no-auth ingress.
- `HTTPClient`: optional HTTP client override; the package uses `http.DefaultClient` when omitted.
- `Logger`: optional structured logger. A nil logger disables package logging.
- `CLIArgs`: optional raw dump of the command line the server was started with, for example `strings.Join(os.Args, " ")`. It is carried as-is in the session summary and never parsed; leave it empty when the executor has no command line to report.

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
		FileExplorerURL:  "http://127.0.0.1:8080",
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
  -fileexplorer-url http://127.0.0.1:8080 \
  -agent-id main \
  -log-level info
```

The `-database` and `-root` flags are required. `-instructions` defaults to `instructions.md` and is used only when the index has no applicable ancestor instruction file. `-gateway-url` defaults to `http://127.0.0.1:18789`, `-fileexplorer-url` defaults to `http://127.0.0.1:8080`, `-agent-id` defaults to `main`, and `-log-level` defaults to `info`. Both URL flags take an HTTP(S) base URL without credentials, a query, or a fragment; point `-fileexplorer-url` at the fileexplorer service reachable from the executor's network (for example, the container hostname in container deployments). Supported log levels are `error`, `warn`, `info`, `debug`, and `trace`. TLS certificate and hostname verification is skipped by default; pass `-verify-tls` to enable standard verification. For shared-secret Gateway authentication, set `OPENCLAW_AUTH_TOKEN` in the command's environment; the CLI sends it as a bearer token and does not put it in the argument list. Omit it when authentication is handled by a trusted proxy or private no-auth ingress.

To build a standalone binary, run `go build -o ./guava-executor ./cmd/executor`, then invoke `./guava-executor` with the same flags. The CLI exits nonzero on configuration or processing errors. SIGINT and SIGTERM cancel the active run.

## Logging

The CLI writes structured text logs to stderr. At `info` level it reports run and per-file progress, plus each Gateway response status, response byte count, fetch time, response-body read time, and total elapsed time. `debug` adds indexed-file and instructions-file read timings and conversation-turn details. `trace` includes the raw Gateway response body; use it only when that full content is wanted in the log. The bearer token is never logged. `warn` and `error` select those severities and more severe events.

Library callers can provide a `slog.Logger` through `Config.Logger`; if omitted, the executor emits no package logs.

## Standalone dashboard

Run the separate server-rendered dashboard with:

```sh
go run ./cmd/executor-dashboard -database ./fileexplorer.sqlite -root ./files -instructions ./instructions.md -gateway-url http://127.0.0.1:18789 -fileexplorer-url http://127.0.0.1:8080 -agent-id main -log-level info
```

Open `http://127.0.0.1:8081/`. The dashboard listens on loopback at `127.0.0.1:8081` by default. Set `-host` and `-port` to bind elsewhere; these default to the `HOST` and `PORT` environment variables, or `127.0.0.1` and `8081` when unset. For example, use `-host 0.0.0.0 -port 8081` in a container. The `-database` and `-root` flags are required. `-fileexplorer-url` defaults to `http://127.0.0.1:8080` and takes the same HTTP(S) base URL as the executor CLI. `-addr` remains as a deprecated compatibility option and overrides `-host` and `-port` when supplied. The dashboard starts one executor run at a time, shows a session summary card (agent-id, raw CLI-args dump, status, started time, and duration) and the most recent 64 KiB of logs in a text area, and cancels an active run when the dashboard shuts down. It uses bundled Bootstrap for responsive styling, with a server-rendered light/dark toggle and no JavaScript. Its optional full-page refresh is off by default, and `-log-level` selects the same levels as the executor CLI. It is a separate command and does not add routes to `fileexplorer`. Set `OPENCLAW_AUTH_TOKEN` when Gateway authentication is required; pass `-verify-tls` to enable certificate and hostname verification.

## Refreshing the fileexplorer index

`HTTPRefresher` refreshes the fileexplorer index over HTTP. fileexplorer re-synchronizes its SQLite index whenever a directory page is rendered, so a single GET to the base URL is a complete refresh. `Refresh` issues a context-bound GET to the configured base URL, drains and closes the HTML response body without parsing or retaining it, and returns an error that includes the response status when the response is not 2xx. `Executor.Run` calls it before the initial index listing and between file routines, so files created during a conversation can enter the same run after fileexplorer synchronizes its index. Cancellation propagates through the context.

The adapter depends only on the base URL and an HTTP client; it does not touch the queue or SQLite index implementations, and `Refresh(ctx)` satisfies the `Refresher` contract from [story #109](https://github.com/dev1-alexwei-net/guava/issues/109), so the queue coordinator can use it without depending on HTTP details.

```go
refresher, err := executor.NewHTTPRefresher("http://127.0.0.1:8080", nil)
if err != nil {
	log.Fatal(err)
}

if err := refresher.Refresh(context.Background()); err != nil {
	log.Fatal(err)
}
```

## Work queue

`WorkQueue` is the append-only, in-memory work queue scoped to a single executor run. `NewWorkQueue` starts an empty queue; `Enqueue` accepts one ordered file snapshot and appends only the paths not already discovered during the run. Already queued items keep their FIFO position; each new batch is appended as a group, sorted by binary (byte-wise) path order, the same ordering the fileexplorer index uses. Path identity is the normalized relative path, so the same file discovered in different raw spellings is queued once. A snapshot containing an absolute path, an empty path, or a path that escapes the root fails as a whole and leaves the queue unchanged.

`Pop` removes and returns the oldest queued path, `Len` and `Empty` report the queue state, and `Pending` returns a copy of the queued paths in FIFO order. The queue holds its state in memory only: it adds no shared-database table and is not shared between runs.

```go
queue := executor.NewWorkQueue()
if added, err := queue.Enqueue([]string{"zeta.txt", "alpha.txt"}); err != nil {
	log.Fatal(err)
}

for !queue.Empty() {
	path, ok := queue.Pop()
	if !ok {
		break
	}
	// process path
}
```

## Session summary

`Summary` is a read-only snapshot of the executor session, returned by `Executor.SessionSummary`. The accessor is guarded by a mutex, so it is safe to call from any goroutine, and before, during, or after a run:

- `AgentID`: the OpenClaw agent the executor was configured with.
- `CLIArgs`: the raw dump of the command line the server was started with, carried exactly as configured and never parsed into fields.
- `Status`: one of `idle` (before the first run), `running` (while `Run` is active), and the terminal state after a run ends: `completed`, `failed`, or `stopped` (a run canceled through its context).
- `StartedAt`: when the most recent run started; zero before the first run.

```go
summary := runner.SessionSummary()
if summary.Status == executor.StatusRunning {
	// session is active
}
```

Per-file outcomes, discovered/processed/queued counts, current file, and turn counters are not part of the summary and are deferred to a follow-up story.

## Processing behavior

- The package reads only rows where `is_dir = 0`, ordered by the slash-separated `path` using SQLite binary ordering.
- It reads each current file from `RootPath` through Go's rooted filesystem API. Paths must be relative and contained by the root; directories and paths that escape the root are rejected.
- For each target file, the executor looks for an indexed `instructions.md` in the file's directory and then each parent directory up to the root. The nearest match wins; instructions are not combined across ancestors. The file is read through `RootPath`. If none is indexed, the configured `InstructionsPath` fallback is used. Indexed files named `instructions.md` are treated as instruction sources and are not processed as target files. The initial input also includes the indexed target path and its current contents.
- Each file starts a separate conversation routed to a stable per-file Gateway session using the explicit `x-openclaw-session-key` header, set to `agent:<AgentID>:guava:<relative-path>`. If assistant output text does not contain the exact marker `<complete />`, the package sends `Continue` with the previous `previous_response_id`. It does not start the next file until the marker appears. When a turn fails with an output-token cutoff and returns no response id, the package still sends `Continue` with the same explicit session key, so the Gateway routes it to the cut-off turn's session.
- `Run` refreshes fileexplorer and discovers the current regular-file paths before selecting work, then refreshes again between file routines. Each discovery batch is ordered by SQLite binary path order and appended behind already pending paths. A path is processed at most once per run; a newly discovered path that sorts earlier than pending work still goes to the tail of that run's queue.
- Queue and completion state are in memory and scoped to one `Run`. A later run starts fresh; failed runs stop at the first error, and restarting does not resume prior progress.
- Database rows and source files are not modified by the executor. The configured fileexplorer GET may of course refresh its own index as part of its normal page behavior.
- `Run` accepts a `context.Context`; cancellation and database, file, and HTTP errors stop the run and are returned to the caller.

## Dynamic discovery interfaces

`Refresher`, `FileLister`, and `ProcessFile` isolate the dynamic discovery loop described in [story #109](https://github.com/dev1-alexwei-net/guava/issues/109). `Coordinator` refreshes the index before each selection, appends unseen paths through `WorkQueue`, and invokes the processor in FIFO order. The coordinator does not depend on HTTP or SQLite, so its ordering and failure behavior is tested with in-memory fakes in `coordinator_test.go`. `Executor.Run` composes the HTTP refresher, read-only SQLite index, and rooted file processor behind those interfaces.

## OpenClaw Gateway

The package sends non-streaming JSON requests to `POST {GatewayURL}/v1/responses`, with `model: "openclaw"` and the `x-openclaw-agent-id` header. The OpenClaw Responses endpoint is disabled by default; enable `gateway.http.endpoints.responses.enabled` on the Gateway.

See the [OpenClaw OpenResponses HTTP API](https://docs.openclaw.ai/gateway/openresponses-http-api) for Gateway authentication and endpoint configuration.

## Tests

The package tests use local SQLite fixtures and mock HTTP servers. They do not contact a live OpenClaw Gateway.
