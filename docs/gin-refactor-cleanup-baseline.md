# Gin refactor cleanup baseline and behavior map

Baseline captured before issues #423–#425, from `origin/main` at `1137021826cbb384ed736da8d5a3c2f206c91ab3` (2026-11-28). This is the #422 audit snapshot; later comparisons must use the same source scope and counting method.

## Repeatable Go line count

Count newline-terminated lines (the same measure as `wc -l`) in tracked, first-party Go files. Production and test lines are reported separately. Exclude any path containing a `vendor` directory, conventional generated filenames (`*_generated.go`, `*.pb.go`), and files whose first five lines contain the standard `// Code generated … DO NOT EDIT.` marker. At this baseline there are no tracked Go files excluded by those rules.

Run this unchanged from the repository root at the baseline commit, and repeat it at the final commit:

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
| Production source | 38 | 7,142 |
| Test source | 22 | 6,517 |
| **Total** | **60** | **13,659** |

Generated/vendor exclusions: zero files at this snapshot. This count excludes non-Go assets, templates, dependencies, and untracked files.

## Behavior and test map

| Guava behavior | Existing coverage |
| --- | --- |
| File explorer directory listing, direct-child ordering, serving/download, editor validation/save, upload/create/rename/delete, errors and redirects, traversal/symlink protection, preview and URL-backed preferences | `fileexplorer/server_test.go`, `fileexplorer/web_test.go`; these exercise Guava handlers and rendered HTML/response contracts. The tests do not establish actual browser rendering at different viewports. |
| Rooted local filesystem operations, scan-to-index synchronization, symlink skipping, external filesystem changes, and SQLite replica listing/replacement | `fileexplorer/server_test.go`, `fileexplorer/store_test.go`; notable checks include `TestRootedFilesystemOwnsPathPolicy`, `TestSync_MirrorsTree`, `TestSync_AddRemove`, `TestSync_SkipsSymlinks`, and `TestReplaceAllRollsBackOnFailure`. |
| Executor configuration, subdirectory scoping, account/session summaries, conversation continuation/cancellation/retries, OpenResponses requests, token limits | `executor/*_test.go`, `conversations/*_test.go`. |
| Dashboard rendering and session lifecycle: start/resume, saved configuration, subdirectories, deletion confirmation, pending-file reporting, refresh, and account settings | `cmd/executor-dashboard/dashboard_test.go`, `cmd/executor-dashboard/main_test.go`. |
| Durable session/account/progress storage and schema migration | `sessions/store_test.go`; tests create temporary SQLite databases and reopen/migrate them. |
| Task file indexing, read-only SQLite access, rooted file reads, instructions, manifest/subdirectory discovery, refresh, and work queue | `tasks/*_test.go`. |

### Gin-only versus application coverage

No current test merely asserts a generic Gin router guarantee. The HTTP tests above exercise Guava behavior through Gin-backed handlers. `internal/web/gin_test.go` exercises the shared wrapper contract: Guava's `nosniff` security header, request-value helper semantics, malformed media-type handling, and nil-safe helper behavior. The request-value helpers are thin adapters around `net/http` request APIs, so #423 may review those tests for duplication; this audit does not establish them as redundant Gin coverage. Preserve the security-header and nil-safety contracts unless the next task independently demonstrates otherwise.

Coverage gaps to keep visible in subsequent cleanup: `api` and `cmd/executor` have no test files; `internal/version` also has none. The file explorer's HTML tests verify server-rendered content and accessibility/state attributes, not browser-level layout/rendering. Do not count those as Gin guarantees or infer visual verification from them.

## SQLite and local-files checks

- `go test ./fileexplorer`: exercises temporary SQLite stores plus real temporary local directory trees; covers listings, transactional replacement, mirroring/add-remove, symlinks, and handler filesystem mutations.
- `go test ./tasks`: exercises read-only SQLite index queries and temporary rooted-file reads.
- `go test ./sessions`: exercises the separate SQLite session/account/progress store, reopen persistence, and migrations.
- `go test ./...`: runs all package tests above.
- `CGO_ENABLED=0 go build -o /tmp/guava-fileexplorer-422 ./fileexplorer/...`: confirms the SQLite-backed file explorer builds with cgo disabled, as documented in `fileexplorer/README.md`.
- `go vet ./...` and `test -z "$(gofmt -l .)"`: project static and formatting checks; CI runs formatting, vet, and all Go tests. PR CI additionally builds the executor image for amd64/arm64.

Checks on this baseline: Go 1.27.4; formatting, `go vet ./...`, `go build ./...`, `go test ./...`, and the cgo-disabled file explorer build all passed. The initial shell PATH did not include Go, so the first invocation returned `go: not found`; rerunning with the installed workspace Go toolchain passed. No code/test failure was observed.

The SQLite and filesystem tests use temporary fixtures, not production data. There is no separate interchangeable local-files backend: the file explorer operates on a rooted local directory and keeps a SQLite index replica; task readers consume that index read-only. No production-data or cross-process persistence smoke test was run.
