# File Explorer

`fileexplorer` is a self-contained, server-rendered file explorer. It uses Go's `net/http` and `html/template`; it does not use a JavaScript client or an HTTP framework. The package stores a SQLite replica of the configured directory tree and renders every directory page from that replica.

## Run

From the repository root, run:

```sh
CGO_ENABLED=0 go run ./fileexplorer -root /path/to/directory -host 0.0.0.0 -port 8080
```

The flags are:

- `-root`: directory to expose; defaults to the current directory.
- `-host`: host/interface to bind; defaults to the `HOST` environment variable or all interfaces. Use `127.0.0.1` to restrict access to loopback.
- `-port`: TCP port to bind; defaults to the `PORT` environment variable or `8080`.
- `-addr`: deprecated compatibility option for a complete listen address or bare port; when set, it overrides `-host` and `-port`.
- `-db`: SQLite database file. By default, the service uses a database file in the system temporary directory, keyed by the absolute root path. Keep any explicitly configured database file outside the served root.

The server handles SIGINT and SIGTERM with a five-second graceful-shutdown timeout.

## Design

`main.go` is the composition root: it resolves the host and port flags and their `HOST`/`PORT` environment defaults, opens the SQLite `Store`, builds an `application`, starts `http.Server`, and handles SIGINT/SIGTERM with a five-second graceful-shutdown timeout. The application closes its rooted filesystem handle and the caller closes the database on both normal shutdown and startup/server errors.

`server.go` owns the application, route table, request handlers, and HTTP error/redirect mapping. The composed objects keep the responsibilities explicit:

- `rootedFS` validates paths and performs file operations through Go's `os.Root` API; symlink components are rejected.
- `scanner` walks the rooted filesystem, and `synchronizer` replaces the SQLite replica through `Store.ReplaceAll` only after a successful scan.
- `Store` owns SQLite schema, replica replacement, and direct-child queries.
- `mutations` owns file/directory changes and atomically replaces editor saves while preserving permission bits.
- `renderer` builds page data and URLs, executes the HTML template, and serves the embedded stylesheet.

Preview selection is URL state, not a browser session: `/preview/<path>` determines which file is shown, so the server needs no session store or client-side state. The preview's word-wrap preference follows the same pattern: wrap is on by default, and the non-default `wrap=off` query parameter rides every link and redirect the way the selection does. The preview pane's collapsed state follows the same pattern: the pane is expanded by default, and the non-default `pane=collapsed` query parameter hides the preview column at the `lg` viewport and up while small screens keep the stacked pane and its existing hide control.

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
- `GET /preview/<path>` renders the parent directory with the preview pane active for that file; the pane is visible exactly when the path is a preview path, so the URL always matches the state. The `wrap=off` query parameter switches the pane to non-wrapped rendering; the default (no parameter) is wrap on. The `pane=collapsed` query parameter hides the preview column at the `lg` viewport and up and gives the listing the full row; removing the parameter on the same `/preview/<path>` target restores the pane with the same selected file. The default (no parameter) is expanded.

The **New folder** action creates new directories with an effective mode of `0777`. The application reapplies this mode after creation, so the process umask does not remove the write bits. This intentionally makes new directories writable by all users; existing directories and files are not changed. If applying the mode fails, the new directory is removed where possible and the action reports the normal directory-creation error.

The editor accepts regular files containing valid UTF-8 text without NUL bytes, up to 1 MiB. Saves are written to a temporary file in the same directory and atomically replaced; the existing file's permission bits are retained. Larger files and non-text files are not editable, but can still be served or downloaded.

Directory pages include a preview area. When a directory is rendered without a selected file, the preview area shows a concise empty state explaining how to select a file; the state renders the same way for empty directories. Clicking a regular file's name navigates to `/preview/<path>`; that route renders the parent directory with a Bootstrap card showing the selected file's contents (UTF-8 text up to 512 KiB) above the listing. The selected file's row is marked with a visible Selected indicator next to its name and exposed with an `aria-current` state, so keyboard and assistive-technology users can identify it without relying on color alone. Directory names continue to navigate into their directory, and Edit and Download remain explicit actions that use the raw file route. Hiding the preview is a plain navigation back to the directory, and each row's button reflects the current state: Preview when the pane is closed, Hide preview when it is open. Because the selection lives in the URL rather than in server memory, the route is always consistent with the state the page shows, and no client-side state or scripts are needed. Non-text and oversized files render an explanatory note with an explicit Download file button in the preview pane, so the file can be obtained without returning to the listing; the selected file's pane header likewise offers a direct Download action next to Hide preview. A Wrap control in the pane header makes word wrap an explicit user choice: it switches the preview between wrapped rendering and raw non-wrapped lines that scroll horizontally inside the pane, and it shows its active state to assistive technology. The choice is carried by the same URL-state pattern, so toggling wrap preserves the selected file, the directory, and any other query values on the page. A Collapse preview control in the pane header hides the pane on large screens (`lg` and up) without leaving the directory; the listing header then offers an Expand preview control that restores the pane for the same selected file. Both controls appear only on large screens, where the two-column layout applies: on small screens the stacked pane and the Hide preview action remain unchanged, and the pane's collapsed state is still URL state, so it rides theme toggles, navigation, editor saves, and mutations like wrap does. The pane uses Bootstrap's responsive grid and utility classes, so it works on mobile devices.

The embedded HTML page uses regular links and forms. Directory mutations return a newly rendered page; editor saves redirect back to the editor with a success message. There are no JSON endpoints, `fetch` calls, or client-side scripts.

## Responsive styling and assets

The page uses Bootstrap v5.3.8 CSS from the official [`twbs/bootstrap`](https://github.com/twbs/bootstrap) project. The stylesheet is bundled at `fileexplorer/static/bootstrap.min.css`, embedded with the HTML template, and served from `GET /static/bootstrap.min.css`. The Bootstrap MIT license is included at `fileexplorer/static/BOOTSTRAP-LICENSE.txt`.

The template uses Bootstrap's responsive layout classes and the standard viewport metadata. The stylesheet is served by this Go application; it is not loaded from a CDN. Bootstrap JavaScript and custom browser-side JavaScript are not included.

## Filesystem and SQLite behavior

Before each directory page is rendered, the application's `synchronizer` asks its `scanner` to walk the configured rooted filesystem. The scanner stores slash-separated relative paths and skips symlinks; after a successful scan, `Store.ReplaceAll` transactionally replaces the SQLite replica. The filesystem remains authoritative, and the database is used for directory listings. `rootedFS` validates paths, rejects symlink components, and performs operations through Go's rooted filesystem API. Listings include direct children only, with directories before files and names sorted within each group.

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
