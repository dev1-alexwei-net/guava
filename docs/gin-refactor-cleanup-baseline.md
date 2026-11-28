# Gin refactor cleanup baseline and behavior map

Baseline captured after the Gin migration stack, at commit `879d67499c8d059664aef2795d1830f4b42b9984` (PR #429 head, 2026-11-28). This is the #422 audit snapshot before cleanup tasks #423–#425. Repeat the count at this commit for the baseline and at the final cleanup commit using the same scope and command.

## Repeatable Go line count

Count newline-terminated lines (the same measure as `wc -l`) in tracked, first-party Go files. Report production and test lines separately. Exclude paths containing a `vendor` directory, conventional generated filenames (`*_generated.go`, `*.pb.go`), and files whose first five lines contain the standard `// Code generated … DO NOT EDIT.` marker. No tracked Go files are excluded at this baseline.

Run this unchanged from the repository root at the baseline commit and at final verification:

```sh
python3 - <<'PY'
import pathlib, subprocess

paths = subprocess.check_output(["git", "ls-files", "-z", "--", "*.go"]).split(b"\0")
lines = {"source": 0, "tests": 0}
files = {"source": 0, "tests": 0}
for raw in filter(None, paths):
    name = raw.decode()
    path = pathlib.Path(name)
    if "vendor" in path.parts or path.name.endswith(("_generated.go", ".pb.go")):
        continue
    data = path.read_bytes()
    if any(line.startswith(b"// Code generated ") and line.rstrip().endswith(b"DO NOT EDIT.")
           for line in data.splitlines()[:5]):
        continue
    kind = "tests" if path.name.endswith("_test.go") else "source"
    lines[kind] += data.count(b"\n")
    files[kind] += 1

print(f"source: {files['source']} files, {lines['source']} lines")
print(f"tests:  {files['tests']} files, {lines['tests']} lines")
print(f"total:  {sum(files.values())} files, {sum(lines.values())} lines")
PY
```

| Scope | Tracked Go files | Lines |
| --- | ---: | ---: |
| Production source | 38 | 7,193 |
| Test source | 22 | 6,598 |
| **Total** | **60** | **13,791** |

Generated/vendor exclusions: zero files at this snapshot. This count excludes non-Go assets, templates, dependencies, and untracked files.

## Behavior and test map

| Guava behavior | Existing coverage |
| --- | --- |
| File-explorer routes and methods, directory listing/order, file serving/download, editor validation/save, upload/create/rename/delete/move, errors/redirects, traversal and symlink protection, preview and URL-backed preferences | `fileexplorer/server_test.go`, `fileexplorer/web_test.go`; tests exercise Guava handlers and response/HTML contracts. `TestGinRoutesPreserveMethodsAndWildcardParameters` protects the migrated route surface. These tests do not establish actual browser rendering at different viewports. |
| Rooted local filesystem policy, scan-to-index synchronization, symlink skipping, external filesystem changes, and SQLite replica listing/replacement/rollback | `fileexplorer/server_test.go`, `fileexplorer/store_test.go`; notable checks include `TestRootedFilesystemOwnsPathPolicy`, `TestSync_MirrorsTree`, `TestSync_AddRemove`, `TestSync_SkipsSymlinks`, and `TestReplaceAllRollsBackOnFailure`. |
| Executor configuration and subdirectory scoping; session/account summaries; conversation continuation, cancellation, retries, OpenResponses requests, and token limits | `executor/*_test.go`, `conversations/*_test.go`. |
| Dashboard/session lifecycle: start/resume, saved configuration, subdirectories, deletion confirmation, pending-file reporting, refresh, account settings, and route-method compatibility | `cmd/executor-dashboard/dashboard_test.go`, `cmd/executor-dashboard/main_test.go`. |
| Durable session/account/progress storage and schema migration | `sessions/store_test.go`; tests use temporary SQLite databases and exercise reopen/migration behavior. |
| Task file indexing, read-only SQLite access, rooted file reads, instructions, manifest/subdirectory discovery, refresh, and work queue | `tasks/*_test.go`. |

### Gin-only versus application coverage

The Gin-backed tests assert Guava contracts rather than merely checking a default Gin guarantee: application route methods/wildcard mapping, Guava's `nosniff` security header, and shared request-helper behavior. The request-value helpers are thin adapters around `net/http` request APIs; their tests may be reviewed for duplicated standard-library coverage, but this audit does not classify them as generic Gin guarantees. Preserve application security-header and nil-safety behavior unless a later task demonstrates otherwise.

Coverage gaps to keep visible during cleanup: `api` and `cmd/executor` have no test files; `internal/version` also has none. File-explorer HTML tests verify server-rendered content and accessibility/state attributes, not browser-level layout/rendering. Do not count those as visual verification or as Gin guarantees.

Documentation mismatch on this snapshot: `fileexplorer/README.md` still says the package does not use an HTTP framework, although the Gin migration is in the #427–#429 stack. Align that statement with the migrated implementation in follow-up documentation work.

## SQLite and local-files checks

- `go test ./fileexplorer`: exercises temporary SQLite stores and real temporary local directory trees; covers listings, transactional replacement, mirroring/add-remove, symlinks, and handler filesystem mutations.
- `go test ./tasks`: exercises read-only SQLite index queries and temporary rooted-file reads.
- `go test ./sessions`: exercises the separate SQLite session/account/progress store, persistence, and migrations.
- `go test ./...`: runs all package tests above.
- `CGO_ENABLED=0 go build -o /tmp/guava-fileexplorer-422 ./fileexplorer/...`: confirms the SQLite-backed file explorer builds with cgo disabled, as documented in `fileexplorer/README.md`.
- `go vet ./...` and `test -z "$(gofmt -l .)"`: project static and formatting checks. CI also builds the executor image for amd64/arm64 on pull requests.

Checks at the audit commit: Go 1.27.4; formatting, `go vet ./...`, `go build ./...`, `go test ./...`, and the cgo-disabled file-explorer build all passed. The SQLite and filesystem tests use temporary fixtures; no production-data or cross-process persistence smoke test was run.

The file explorer uses a configured rooted local directory as its source of truth and a SQLite replica for listings; task readers use the index read-only. There is no separate interchangeable local-files backend.
