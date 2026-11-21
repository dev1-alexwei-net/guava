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
- `GET /f/<path>` serves a file under the configured root; append `?download=1` to download it.
- `GET /edit/<path>` opens a UTF-8 text file in the built-in editor; `POST /edit/<path>` saves it.
- `GET /confirm-delete/<path>` displays a confirmation page before a delete form is submitted.
- `POST` or `PUT /mkdir` accepts `parent` and `name`.
- `POST` or `PUT /create-file` accepts `parent` and `name` and creates a new empty file.
- `POST` or `PUT /rename` accepts `old` and `new` (the new value is a single sibling name).
- `POST` or `PUT /delete` accepts `path`; the UI first asks for confirmation on `GET /confirm-delete/<path>`.
- `POST` or `PUT /upload` accepts multipart form fields `parent` and `file`; uploads are limited to 32 MiB.

The editor accepts regular files containing valid UTF-8 text without NUL bytes, up to 1 MiB. Saves are written to a temporary file in the same directory and atomically replaced; the existing file's permission bits are retained. Larger files and non-text files are not editable, but can still be served or downloaded.

The embedded HTML page uses regular links and forms. Directory mutations return a newly rendered page; editor saves redirect back to the editor with a success message. There are no JSON endpoints, `fetch` calls, or client-side scripts.

## Responsive styling and assets

The page uses Bootstrap v5.3.8 CSS from the official [`twbs/bootstrap`](https://github.com/twbs/bootstrap) project. The stylesheet is bundled at `fileexplorer/static/bootstrap.min.css`, embedded with the HTML template, and served from `GET /static/bootstrap.min.css`. The Bootstrap MIT license is included at `fileexplorer/static/BOOTSTRAP-LICENSE.txt`.

The template uses Bootstrap's responsive layout classes and the standard viewport metadata. The stylesheet is served by this Go application; it is not loaded from a CDN. Bootstrap JavaScript and custom browser-side JavaScript are not included.

## Filesystem and SQLite behavior

The filesystem is synchronized into SQLite before each directory page is rendered. `Store.Sync` walks the configured root, stores slash-separated paths relative to that root, skips symlinks, and atomically replaces the `files` table. The filesystem remains authoritative; the database is a queryable replica used for directory listings. Paths are validated relative to the configured root, symlink components are rejected, and filesystem mutations use Go's rooted filesystem APIs to prevent escaping the root. Listings include direct children only, with directories before files and names sorted within each group.

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
