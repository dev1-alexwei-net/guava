# File Explorer

`fileexplorer` is a self-contained, server-rendered file explorer. It uses Go's `net/http` and `html/template`; it does not use a JavaScript client or an HTTP framework. The package stores a SQLite replica of the configured directory tree and renders every directory page from that replica.

## Run

From the repository root, run:

```sh
CGO_ENABLED=0 go run ./fileexplorer -root /path/to/directory -addr :8080
```

The flags are:

- `-root`: directory to expose; defaults to the current directory.
- `-addr`: listen address; defaults to the `PORT` environment variable or `:8080`. A port without a leading colon is prefixed with `:`.
- `-db`: SQLite database file. By default, the service uses a database file in the system temporary directory, keyed by the absolute root path. Keep any explicitly configured database file outside the served root.

The server handles SIGINT and SIGTERM with a five-second graceful-shutdown timeout.

## Routes

- `GET /` browses the root directory.
- `GET /d/<path>` browses a subdirectory.
- `GET /f/<path>` serves a file under the configured root.
- `POST` or `PUT /mkdir` accepts `parent` and `name`.
- `POST` or `PUT /rename` accepts `old` and `new` (the new value is a single sibling name).
- `POST` or `PUT /delete` accepts `path`.
- `POST` or `PUT /upload` accepts multipart form fields `parent` and `file`; uploads are limited to 32 MiB.

The embedded HTML page uses regular links and forms. Mutations return a newly rendered page; there are no JSON endpoints, `fetch` calls, or client-side scripts.

## Filesystem and SQLite behavior

The filesystem is synchronized into SQLite before each directory page is rendered. `Store.Sync` walks the configured root, stores slash-separated paths relative to that root, skips symlinks, and atomically replaces the `files` table. The filesystem remains authoritative; the database is a queryable replica used for directory listings. Listings include direct children only, with directories before files and names sorted within each group.

The SQLite driver is [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), which supports `CGO_ENABLED=0`. The service uses only the Go `database/sql` API and the SQLite driver.

## Development checks

From the repository root:

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
CGO_ENABLED=0 go build -o /tmp/fileexplorer ./fileexplorer/...
```

The explicit build output path avoids writing a binary over the `fileexplorer/` source directory.
